# Upstream behaviours the port must decide about

Defects and surprises found in libucl 0.9.4 while building the differential
harness. Each needs a deliberate call: *replicate* (bug-for-bug, because
something observable depends on it) or *diverge* (and document).

Nothing here is speculative — every item was reproduced against the compiled
C library in `../libucl/build`.

---

## 1. Relative `.include` resolves against the process CWD

`basic/14.in` contains `.include(try=true) "./1.in"`. The path is resolved
against the working directory of the process, not the directory of the file
doing the including. Upstream's golden only matches when `1.in` is *absent*
from the cwd — ctest runs from the build directory, so the `try=true`
not-found path is what got recorded.

**Decision: replicate.** It is observable and fixtures depend on it.
`tools/gen-goldens.sh` pins the cwd for this reason.

## 2. `-M` (UCL_PARSER_DISABLE_MACRO) is not "treat macros as comments"

The flag's documented intent is to skip macros, but a disabled `.include` is
then lexed as an ordinary key and rejected:

    'key must begin with a letter', character: '.'

This is a hard parse failure on 8 of 26 fixtures (9, 13, 14, 15, 16, 18, 19,
23, load). Upstream never notices: `basic.test` runs `-M` with output to
/dev/null and checks the exit status of only the *first* invocation.

**Decision: replicate.** Captured as `*.nomacro.err` goldens.

## 3. A comment as the first line inside an array corrupts the next element

Not a comment-emission problem, and not specific to SAVE_COMMENTS — this is a
parser bug, reproduced with flags of 0. When a comment is the first thing
inside an array and the following content is **indented**, the parser fails to
advance the value start past the leading whitespace, so the next element is
lexed as a string:

    arr [
      # c
      1,
      2
    ]                    ->  ["  1", 2]      first element is a STRING

    arr [
      # c
      {a=1},
      2
    ]                    ->  ["  {a=1}", 2]  the object is swallowed too

    arr [
      /* c */
      1,
      2
    ]                    ->  hard parse error:
                             'string value must not be empty', character: '0x0a'

The trigger is the indentation, not the comment. All of these are fine:

    arr [ 1, 2 ]                 ok
    arr [\n  1,\n  2\n]          ok        (no comment)
    arr [\n# c\n1,\n2\n]         ok        (comment, but content at column 0)
    arr [\n  1,\n  # c\n  2\n]   ok        (comment after the first element)

Comments everywhere else are well covered: at top level, before or after keys,
and inside objects, they parse and re-emit correctly. 25 of 26 fixtures
round-trip cleanly in save-comments mode.

Confirmed present at upstream HEAD (adef4bb, tag 0.9.4) — `git fetch` shows no
commits after our pin, so this is unfixed upstream, not something we are
behind on.

**Where it bites us:** no fixture's *input* contains an indented comment inside
an array, so nothing hits this on the way in. `basic/9.in` reaches it only
through the harness's emit-then-reparse cycle: repeated `.include`s make
`prefix1` an implicit array, saved comments are emitted inside it, and the
reparse then fails. That is the sole source of `9.comments.err`.

**Decision: DIVERGE — parse this correctly.** Reasons:

* It is data corruption, not a contract. No caller can be relying on
  `1` silently becoming `"  1"`.
* Comment support is a main reason to choose UCL over JSON, so quietly
  inheriting a comment-related parser bug is the wrong default.
* The cost is one expectation, not a golden: `9.comments.err` currently
  records the failure and would need to become a success case. No `.golden`
  file depends on the buggy path.

Keep a regression test that documents the C's behaviour alongside ours, so the
divergence stays deliberate and visible rather than looking like an accident.

## 4. msgpack negative-integer width selection is off by one power of two

`ucl_emitter_print_int_msgpack` picks the encoding width by comparing
`abs(val)` against `INT8_MAX`/`INT16_MAX`/`INT32_MAX` rather than the matching
`_MIN` magnitude. So `-128`, `-32768` and `-2147483648` — each representable
in the narrower width — are promoted to the next wider encoding.

**Decision: replicate.** `test_msgpack.c` asserts emit→parse→emit *byte*
identity, so a "correct" encoder would fail the round-trip test.

## 5. Double formatting casts through a 32-bit int

The emitter's "is this value an exact integer" check casts the double through
a 32-bit `int`. Integral doubles outside `[-2147483648, 2147483647]` therefore
miss the `%.1lf` branch and fall through to `%lf` (6 decimals) — so `1.0`
emits as `1.0`, but a large integral double does not follow the same rule.

**Decision: replicate.** See `emitter.md` §3 for the exact Go recipe.
Note infinity/NaN spelling differs between C and Go (`inf`/`nan` vs
`+Inf`/`NaN`) and needs explicit handling.

## 6. The goldens are a double round-trip, not a single emit

`test_basic.c` parses, emits, then **reparses and emits again**; the golden is
the second emit. It also prints one unconditional trailing newline that the
emitter did not produce, and always sets KEY_LOWERCASE, registers `ABI` =
`unknown`, and sets file variables from the input filename.

**Consequence:** a Go test that merely parses and emits once will not
reproduce these bytes. The harness must be mirrored exactly. Details in the
header comment of `tools/gen-goldens.sh`.

## 7. libucl's msgpack encoder can emit documents its own decoder rejects

Verified against the reference build (`UCL_PARSE_MSGPACK`):

| bytes | meaning | libucl decode |
|---|---|---|
| `80` | empty map | rejected: "read assoc value when no container represented" |
| `90` | empty array | rejected: same |
| `81 a1 6b a0` | `{"k": ""}` — zero-length last value | rejected: "cannot insert object with no key" |
| `81 a1 6b a1 76` | `{"k": "v"}` | accepted |

Both are reachable from libucl's own encoder: an empty root is what empty
input produces, and duplicate keys can *turn* a valid document into the
zero-length-last-value shape, which is how differential fuzzing surfaced it.

**Consequence:** decode → encode → decode is **not** a property this codec
has, and no round-trip test may assume it. Byte identity for
encode → decode → encode still holds and is tested; that is the property the
C's own `test_msgpack.c` asserts, and it is the one we match.

**Decision: replicate.** Encoding is byte-identical to the C, including the
shapes the C then refuses to read back. Fixing the encoder would break the
byte-identity contract that `test_msgpack.c` pins. The fuzz invariant is
written as "never panic", not "always round-trips", for this reason.

## 8. Container headers must come from contents, not the tracked length

When decoding wire-level duplicate keys, the C inflates `obj->len` past the
number of pairs it actually stores. Re-emitting from that tracked length
declares more pairs than it writes — e.g. `82 a1 61 01`, a two-pair map
carrying one pair — which is undecodable.

**Decision: diverge, narrowly.** Container headers are computed from the real
contents (`hash.Len()`, the non-nil array element count). Trees built through
the object API are unaffected, so byte identity is preserved everywhere it is
observable; only the C's self-inconsistent case differs, and there the C's
output is unreadable by any decoder including its own.

## 9. A truncated `\u` escape reads one byte past the string

`ucl_unescape_json_string` tracks the remaining length in `len`, but `len` is
not decremented for the backslash itself, so it drifts one ahead of the read
cursor `h`. The four-digit guard is `if (len > 3)`, which therefore passes when
only three bytes are actually left, and the loop reads `h[0]` through `h[3]`.

For the input `0 \u000` the C emits a decoded character built from `000` plus
whatever byte follows the value, instead of falling through to its own
short-escape path and emitting a literal `u`.

It is a logic bug rather than a demonstrable memory-safety one: the C unescapes
in place inside a buffer the parser already owns, which is usually larger than
the value, so the read lands in allocation slack. AddressSanitizer stays quiet
even when the chunk handed to `ucl_parser_add_chunk` is exactly sized, because
the string being unescaped is the parser's copy, not the caller's.

**In Go it is fatal, not cosmetic.** Slices are exactly sized and bounds
checked, so the same arithmetic panics with an index out of range — a denial of
service on any input containing a truncated escape.

**Decision: diverge, and bound the read.** The guard keeps the original
condition and adds `h+4 <= len(s)`, so behaviour is unchanged wherever the C
was reading real bytes and only differs where it was reading past the value.
Truncated escapes now take the C's own short-escape path.

Found by `FuzzParseString`, which reached it in seconds. Worth reporting
upstream as a correctness bug; it is not a crash there.
