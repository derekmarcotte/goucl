# JSON Schema validator — implementation notes

`schema.go` ports `libucl/src/ucl_schema.c` (1106 lines). `schema_test.go`
ports `tests/test_schema.c` + `tests/schema.test`; `schema_unit_test.go` covers
what the upstream corpus never reaches.

Spec: `docs/schema.md`. Everything that document calls out as a deliberate
libucl-vs-draft-4 deviation is reproduced exactly. This file records only the
places where the port departs from **the C**, plus what is left unimplemented.

## Test results

25 enabled suite files, 62 suites, **240 cases, all passing**. No skips, no
weakened assertions. Per file (`suites` / `cases`):

| file | suites | cases | | file | suites | cases |
|---|---|---|---|---|---|---|
| additionalItems.json | 5 | 8 | | multipleOf.json | 3 | 8 |
| additionalProperties.json | 3 | 8 | | not.json | 4 | 10 |
| allOf.json | 3 | 11 | | oneOf.json | 2 | 7 |
| anyOf.json | 2 | 7 | | pattern.json | 1 | 3 |
| dependencies.json | 3 | 16 | | patternProperties.json | 3 | 16 |
| enum.json | 3 | 9 | | properties.json | 2 | 13 |
| items.json | 2 | 5 | | ref.json | 5 | 13 |
| maxItems.json | 1 | 4 | | required.json | 2 | 3 |
| maxLength.json | 1 | 4 | | type.json | 8 | 56 |
| maxProperties.json | 1 | 4 | | uniqueItems.json | 1 | 13 |
| maximum.json | 2 | 5 | | minItems.json | 1 | 4 |
| minLength.json | 1 | 4 | | minProperties.json | 1 | 4 |
| minimum.json | 2 | 5 | | | | |

`definitions.json.disabled` and `refRemote.json.disabled` are copied into
`testdata/schema/` but not run, exactly as upstream does not run them:
`tests/schema.test` globs `schema/*.json`, which the `.disabled` suffix
excludes. `TestSchemaDisabledSuitesAreSkipped` pins that they stay excluded and
stay present. `refRemote` needs live network fetches and draft-4 `id` scope
changes, neither of which libucl implements; `definitions` covers a keyword
libucl gives no special handling.

The suite files are loaded with **this package's own parser**, not
`encoding/json`, so the harness exercises the parser on the same bytes the C
harness fed to `ucl_parser_add_string`.

## Regex

`pattern` and `patternProperties` use `regexp.CompilePOSIX`, not
`regexp.Compile`.

The C uses `regcomp(..., REG_EXTENDED | REG_NOSUB)` — POSIX ERE, match/no-match
only, unanchored, case-sensitive. `CompilePOSIX` is the closest Go equivalent:
it rejects the Perl-only syntax POSIX ERE does not have (`\d`, `\w`, `\b`,
`(?:...)`, `(?i)`, non-greedy quantifiers, lookaround). Plain `regexp.Compile`
would *accept* those and give them RE2's meaning, which is not glibc's — under
glibc ERE `\d` matches a literal `d` — so a schema written for libucl would
silently validate differently. Failing loudly is the safer mismatch.

RE2's leftmost-first vs POSIX's leftmost-longest cannot change a boolean
match/no-match result, which is all `REG_NOSUB` ever needed, so that difference
is inert here. The only ERE constructs RE2 genuinely cannot express are
collating symbols `[.x.]` and equivalence classes `[=x=]`, which glibc also
only handles with full locale collation data; in practice both refuse them.

**Suites affected: none.** Every pattern in the corpus (`^a*$`, `f.*o`, `a*`,
`aaa*`, `[0-9]{2,}`, `X_`, `^v`, …) is common to both flavours;
`TestSchemaCorpusPatternsCompile` walks all 25 files and asserts it, so a
future fixture that breaks the assumption fails loudly.

Compiled patterns are cached (`sync.Map`), including failures. The C
recompiles per call; compilation is deterministic, so this is a pure
optimisation.

## Departures from the C

Policy: reproduce everything `docs/schema.md` documents. For C defects that
document does *not* mention — found by reading the source — reproduce the ones
that are inert, and fix the ones that produce wrong validation results for
realistic schemas. Every fix below is invisible to the upstream corpus (checked
case by case) and has a regression test naming it.

1. **Exclusive bounds leak between keywords.** `ucl_schema_validate_number`
   declares `bool exclusive = false` *outside* the keyword loop and only ever
   assigns it. So in `{"maximum": 10, "exclusiveMaximum": true, "minimum": 5}`,
   processing `maximum` sets `exclusive`, and the later `minimum` — which has
   no `exclusiveMinimum` of its own — silently becomes exclusive and rejects 5.
   Each bound reads its own flag here. Corpus: `maximum.json`/`minimum.json`
   never combine the two bounds. Test: `TestSchemaExclusiveBoundsDoNotLeak`.

2. **JSON-pointer array indices above 0 never resolve.**
   `ucl_schema_resolve_ref_component`'s array branch is
   `i = 0; while (res) { if (i == num) break; res = res->next; }` — `i` is
   never incremented, and array elements are vector entries whose `next` is
   NULL, so only `#/…/0` ever resolves and every other index reports
   `"item number %d does not exist"`. This port indexes properly. Corpus:
   `ref.json` only uses `#/items/0`. Test: `TestSchemaRefArrayIndex`.

3. **`additionalProperties` asks the wrong question of `patternProperties`.**
   The sweep at the bottom of `ucl_schema_validate_object` calls
   `ucl_schema_test_pattern(obj, key(pelt), /*recursive=*/true)`, which reports
   whether **any** key of the object matches the pattern — not whether the key
   currently being examined does. One matching key therefore exempts every
   other key from the `additionalProperties` check. Fixed to test the key at
   hand. Corpus: `additionalProperties.json`'s only patternProperties suite has
   no data with both a matching and a non-matching extra key. Test:
   `TestSchemaAdditionalPropertiesPatternIsPerKey`.

4. **An uncompilable `patternProperties` key is reported, not swallowed.**
   `ucl_schema_test_pattern` returns NULL when `regcomp` fails, which is
   indistinguishable from "no match": the pattern then constrains nothing, and
   with `additionalProperties: false` it silently rejects *everything*. Here
   both `pattern` and `patternProperties` report
   `INVALID_SCHEMA "cannot compile pattern %s"` (the message the C already uses
   for `pattern`). This is the behaviour that surfaces an RE2/ERE mismatch
   instead of hiding it. Test: `TestSchemaPatternCompileFailure`.

5. **A non-string `$ref` is a schema error.** The C passes the value straight to
   `ucl_object_tostring`, which yields NULL for a non-string, and then indexes
   `ref[0]`. Reported as
   `INVALID_SCHEMA "$ref is %s instead of string"`, a message with no C
   counterpart. Test: `TestSchemaRefNonString`.

6. **Recursion-depth guard (an addition, not a fix).** `ucl_schema.c` has no
   cycle detection and no depth counter; a top-level `{"$ref": "#"}` recurses
   forever and overflows the stack. `maxSchemaDepth = 1024` returns
   `SchemaInternalError` instead. Far above anything a real schema reaches, and
   no corpus case comes close. `ref.json`'s "root pointer ref" terminates
   because the enclosing `properties` step descends the *data* on every hop.
   Tests: `TestSchemaRecursionGuard`, `TestSchemaRefWholeDocument`.

7. **Each additional item is validated once.** The C's `additionalItems` loop
   uses a post-increment inside its own re-lookup and so validates the first
   additional item twice. Validation has no side effects; observably identical.
   Already sanctioned by `docs/schema.md` §2.

8. **`uniqueItems` uses a pairwise scan, not a BST.** The C builds a binary
   search tree keyed by `ucl_object_compare`. That comparator is not a total
   order (see `Compare`'s doc comment: object comparison is asymmetric, strings
   order by length before content), so a tree walk can miss a duplicate a
   direct comparison finds. The pairwise scan cannot miss one and reports the
   same later-of-the-pair element. Every `uniqueItems.json` case agrees.

9. **Empty `anyOf`.** The C leaves `ret` uninitialised when the `anyOf` array is
   empty, so the outcome is undefined there. Read as "matched nothing" → fail.

10. **Success returns no error.** The C resets only `err->code` after a
    successful `anyOf`/`not`, leaving `err->msg`/`err->obj` holding stale bytes
    from a failed sub-attempt. This API returns `(true, nil)` on success, which
    removes the footgun without changing any boolean outcome
    (`docs/schema.md` §5 endorses this). Test:
    `TestSchemaSuccessReturnsNoError`.

Everything else is bug-for-bug, including: the `maxValues` off-by-one; the
silent no-op on a wrongly-typed `minProperties`/`maxItems`/`maximum`/`minimum`/
`multipleOf`/`uniqueItems`/`not`/`dependencies` entry; `maxLength`/`minLength`
accepting only `UCL_INT` while the numeric keywords also take floats and times;
byte-counted string lengths; `$ref` being additive rather than exclusive; the
absence of RFC 6901 `~0`/`~1` unescaping and of percent-decoding; `required`
still being checked after the `additionalProperties` sweep has already failed
(so it overwrites the message); and the 128-byte message budget.

## Not implemented

- **`format`**, **`default`**, **`title`/`description`**, **draft-4 `id`** —
  never read by `ucl_schema.c`. Inert here too.
  `TestSchemaUnimplementedKeywordsAreInert` pins that they neither fail nor
  constrain.
- **Boolean schemas** (`true`/`false` as a whole schema) — a non-object schema
  is `INVALID_SCHEMA "schema is %s instead of object"`.
- **Draft-6+**: `const`, `contains`, `propertyNames`, `if`/`then`/`else`,
  `$id`, numeric `exclusiveMinimum`/`exclusiveMaximum`. All inert.
- **`UCL_SCHEMA_EXTERNAL_REF_MISSING` / `_INVALID`** — present in the enum for
  API parity, never produced, matching the C.
- **Remote `$ref` fetching** — `LocalRefFetcher`, the default, reads local
  files and refuses anything containing `://`. libucl gates URL fetching behind
  the `ENABLE_URL_INCLUDE` build option and ships `refRemote.json` disabled;
  a validation call should not reach the network unasked. Callers who want it
  pass their own `RefFetcher` to `ValidateRootExtFetch`, which is the one entry
  point with no C counterpart. Local-file refs work out of the box, resolved
  against the process working directory exactly as `ucl_fetch_file` does —
  there is no base-URI rebasing because there is no `id` support.
- **`minValues`/`maxValues`** are implemented (they are UCL-only keywords over
  the duplicate-key implicit array, which this port's object model does have),
  but no fixture exercises them; `TestSchemaMinMaxValues` and
  `TestSchemaImplicitArrayValidatesEveryElement` do.

## Requests to other components

None. Nothing outside `schema.go`, `schema_test.go`, `schema_unit_test.go` and
`testdata/schema/` was touched, and no bug was found in the object API, the
parser or the emitters while building on them.
