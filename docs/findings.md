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

## 3. Comment emission produces output libucl cannot reparse

With `-C` (SAVE_COMMENTS), emitting `basic/9.in` yields config text that fails
to parse back:

    Error occurred (phase 2): error while parsing <unknown>: line: 9,
    column: 4 - 'string value must not be empty', character: '#'

The emitter places comments inside an array literal where the parser will not
accept them. This is a round-trip bug in libucl, invisible upstream for the
same reason as #2.

**Decision: replicate the emitted bytes, and record the reparse failure as a
known upstream defect** (`9.comments.err`). Emitting byte-identical output is
the compatibility contract; emitting *reparseable* output is not something
libucl itself provides. Revisit if the application relies on comment
round-tripping — a fixed mode would then be an additive, opt-in divergence.

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
