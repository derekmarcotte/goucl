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

**Applies to every layer, including the idiomatic one.** `DESIGN.md` originally
proposed re-rooting includes at an `fs.FS` in the new API, on the grounds that
this is a footgun. That was rejected: behaviour a UCL document can observe is
the language, not a defect to design around, so a document that works under the
C works identically here. An opt-in sandbox remains possible, because an option
changes no default. See `DESIGN.md` §6.2.

## 2. `-M` (UCL_PARSER_DISABLE_MACRO) never reached its own skip path

The flag is documented as "Treat macros as comments", and
`ucl_skip_macro_as_comment` exists to do exactly that. Nothing calls it: its
only caller is the `UCL_STATE_MACRO_NAME` arm, and both places that enter that
state guard on `!(flags & UCL_PARSER_DISABLE_MACRO)`. With the flag set the
parser never reaches the skip path, the leading `.` is lexed as an ordinary
key, and the document is rejected with

    'key must begin with a letter', character: '.'

So the feature is unreachable and the function is dead code. It fails on 8 of
26 fixtures. Upstream never noticed because `basic.test` discards `-M` output
and checks the exit status of only its first invocation.

**Decision: DIVERGE — skip the macro, as documented.** Originally recorded as
*replicate*, on the grounds that it was released 0.9.4 behaviour. Reversed: a
flag that rejects every document containing a macro is useless to callers, and
being bug-compatible with a bug we are fixing on the reference checkout is
incoherent.

Combining the fix with `SAVE_COMMENTS` also required not recording the skipped
macro. `macro_save` passed the macro body to `ucl_save_comment`, but that range
begins after the `.` is consumed and carries no comment delimiter, unlike every
other call site. Emitting wrote bare macro text back out as though it were a
comment, and reparsing failed with `'invalid character in a key'`. A skipped
macro is not a comment, so it is dropped. This cannot regress anyone: the path
has never been reachable.

Fixed on the reference checkout's branch `fix-disable-macro`, and adopted here.
`TestParserDisableMacroSkipsMacroLines` pins it, and the nine `*.nomacro.err`
expectations became ordinary goldens.

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

Confirmed present at upstream HEAD (adef4bb, which is one merge past the 0.9.4
tag at 058286f) — `git fetch` shows no commits after our pin, so this is
unfixed upstream, not something we are behind on.

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

## 10. Correction: two of the three "out-of-bounds reads" are not out of bounds

`docs/notes/parser.md` records three places where the C reads outside its buffer and
this port bounds instead. Investigating them against the reference build with
AddressSanitizer, only one survives.

**Not a defect — `p[-1]` in `ucl_skip_comments` and `ucl_parse_macro_arguments`.**
Both were described as reading before the buffer on the first byte. Neither
can. In `ucl_skip_comments` the lookbehind sits after two `ucl_chunk_skipc`
calls that consumed `/*`, so `p-1` is at worst the `*`. In
`ucl_parse_macro_arguments` the branch is only reached in state 1, which
requires an opening quote to have been consumed. Our own note says as much --
"which is what the C reads in practice (the `/` of `/*`, or the opening
quote)" -- i.e. in bounds. The Go code is defensive, not compensating.

**Not demonstrated — the backwards space strip in `ucl_parse_value`.** The scan
has no explicit lower bound, but `ucl_parse_value` skips leading whitespace
before recording the atom start, so the scan meets a non-whitespace byte and
stops. No input was found that walks it past the buffer, including 3,000,000
mutated fixtures under ASAN with each chunk in an exactly-sized allocation.

**Not demonstrated — `*out_len = dstlen` in `ucl_check_variable_safe`.** It
assigns where the registered-variable path accumulates with `+=`, which looks
like an under-count of the destination size, but a variable handler returning
replacements far longer than the text they replace did not overflow anything
under ASAN.

**Real — the truncated `\u` escape (finding #9).** Confirmed by reading the
code and by differing output. Fixed on the reference checkout's branch
`fix-truncated-unicode-escape` with `tests/basic/utf_escape_trunc.in`.

The bounding in the Go port stays regardless: it costs nothing, and in a
bounds-checked language the same arithmetic is a panic rather than a wrong
byte. But only one of these is a real defect, and claiming three
would have been wrong.

### Unresolved: a possible leak of saved comments

An ASAN run with LeakSanitizer reported `ucl_save_comment` allocations
surviving `ucl_parser_free` on malformed input, roughly one run in twenty.
`ucl_parser_free` does unref `parser->comments`, and the reported input did not
reproduce in isolation, so this is **not confirmed** and may be an artefact of
the harness. Recorded so it is not lost; needs a minimal reproducer before it
is worth reporting.

## 11. A repeated key holding arrays loses all but the first on JSON emission

A key given more than once forms an implicit array — libucl's own term, from
its README: "if we have two duplicate objects with the same priority then we
have a multi-value key (implicit array)". The JSON emitters render that chain
as a JSON array, and do so correctly for scalars and for objects. For values
that are themselves arrays, they emit only the first.

Verified against the reference build. The chain is intact in the tree in every
case; only the emitted JSON differs:

| document | chain | `UCL_EMIT_JSON_COMPACT` | `UCL_EMIT_CONFIG` |
|---|---|---|---|
| `x = 1; x = 2;` | 2 | `{"x":[1,2]}` | both values |
| `x = [1]; x = [2];` | 2 | **`{"x":[1]}`** | both values |
| `x { a = 1; } x { b = 2; }` | 2 | `{"x":[{"a":1},{"b":2}]}` | both values |

The config emitter is correct in all three. The loss is specific to the JSON
path and to array-valued chains.

This is not the lossiness `doc/api.md` already warns about — "JSON ... can
possibly lose some original data, such as implicit arrays". That note is about
losing the *distinction* between a repeated key and an array literal, which is
unavoidable in JSON and which the scalar row above shows working as intended.
Here an entire value disappears, and inconsistently: the same document shape
survives when its values are scalars or objects.

**Decision: replicate, for now.** The port reproduces it, because the goldens
come from the C and because no caller has been shown to depend on either
behaviour. It is a genuine defect by the rule this register follows — it
produces a wrong answer rather than a surprising one — so it is a candidate
for the same treatment as findings #2, #3 and #9: fix here, patch the
reference checkout,
regenerate the goldens from a reference build carrying the fix.

Fixing it has a cost worth stating plainly: our JSON output would stop being
byte-identical to the C's for this input class, which is the property the
golden corpus exists to prove. That trade wants deciding deliberately rather
than in passing, so it is recorded and left alone.

Not yet reduced to a minimal patch.
