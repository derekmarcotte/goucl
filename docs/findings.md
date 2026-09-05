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
