# libucl JSON-Schema (draft-4) validator — port spec

Source read in full: `libucl/src/ucl_schema.c` (1106 lines), `libucl/include/ucl.h` (schema section,
lines 1586-1664), `libucl/tests/test_schema.c`, `libucl/tests/schema/*.json` (+ `.disabled`).
Version: libucl v0.9.4. This document describes **exactly what libucl does**, including every
place it deviates from the official JSON Schema draft-4 spec — match this file, not the spec.

---

## 1. Public API surface

```c
enum ucl_schema_error_code {
    UCL_SCHEMA_OK = 0,               /* no error */
    UCL_SCHEMA_TYPE_MISMATCH,        /* type of object is incorrect */
    UCL_SCHEMA_INVALID_SCHEMA,       /* schema is invalid */
    UCL_SCHEMA_MISSING_PROPERTY,     /* one or more missing properties */
    UCL_SCHEMA_CONSTRAINT,           /* constraint found */
    UCL_SCHEMA_MISSING_DEPENDENCY,   /* missing dependency */
    UCL_SCHEMA_EXTERNAL_REF_MISSING, /* cannot fetch external ref  -- DECLARED BUT NEVER EMITTED */
    UCL_SCHEMA_EXTERNAL_REF_INVALID, /* invalid external ref       -- DECLARED BUT NEVER EMITTED */
    UCL_SCHEMA_INTERNAL_ERROR,       /* something bad happened */
    UCL_SCHEMA_UNKNOWN               /* generic error */
};

struct ucl_schema_error {
    enum ucl_schema_error_code code; /* error code */
    char msg[128];                   /* formatted message, fixed-size buffer */
    const ucl_object_t *obj;         /* the object (schema or data node) that triggered the error */
};

bool ucl_object_validate(const ucl_object_t *schema,
                         const ucl_object_t *obj, struct ucl_schema_error *err);

bool ucl_object_validate_root(const ucl_object_t *schema,
                              const ucl_object_t *obj,
                              const ucl_object_t *root,
                              struct ucl_schema_error *err);

bool ucl_object_validate_root_ext(const ucl_object_t *schema,
                                  const ucl_object_t *obj,
                                  const ucl_object_t *root,
                                  ucl_object_t *ext_refs,
                                  struct ucl_schema_error *err);
```

**Deviation #1**: `UCL_SCHEMA_EXTERNAL_REF_MISSING` and `UCL_SCHEMA_EXTERNAL_REF_INVALID` exist in
the enum but `ucl_schema.c` never constructs an error with either code (confirmed by grep over the
whole file). All `$ref` fetch/parse failures — even for a genuinely missing/unreachable external
document — are reported as `UCL_SCHEMA_INVALID_SCHEMA`. A faithful Go port must not emit dedicated
"external ref" error codes for these cases; keep the two enum values for API completeness but never
produce them from the validator itself.

### Entry points and their relationship

- `ucl_object_validate(schema, obj, err)` == `ucl_object_validate_root_ext(schema, obj, /*root=*/schema, /*ext_refs=*/NULL, err)`.
  Root schema is `schema` itself (used to resolve `$ref` starting with `#`).
- `ucl_object_validate_root(schema, obj, root, err)` == `ucl_object_validate_root_ext(schema, obj, root, NULL, err)`.
  Lets you validate a sub-schema (`schema`) while resolving internal `$ref`s against a different
  document (`root`) — used internally for `$ref` recursion and definitions.
- `ucl_object_validate_root_ext(schema, obj, root, ext_refs, err)`:
  - If `ext_refs == NULL`, allocates a fresh empty `UCL_OBJECT` to serve as the **external ref
    cache** for the duration of this call and unref's it before returning (so cross-call caching
    of fetched remote schemas requires the caller to pass its own persistent `ext_refs` object).
  - Delegates to the internal recursive `ucl_schema_validate(schema, obj, try_array=true, err, root, ext_refs)`.

`err` may be `NULL` throughout; every error-producing call site guards on `err != NULL` before
writing into it, so passing `NULL` just discards diagnostics (return value is still correct).

### Error message formats (produced with `vsnprintf` into a **128-byte** buffer — truncation is
possible for long strings/paths; the Go port should mimic a fixed/bounded buffer or at least not
assume unlimited length):

| Code | Exact `fmt` string | printf args |
|---|---|---|
| INVALID_SCHEMA | `"additionalProperties attribute is invalid in schema"` | — |
| INVALID_SCHEMA | `"required attribute is invalid in schema"` | — |
| CONSTRAINT | `"object has not enough properties: %u, minimum is: %u"` | obj->len, minmax |
| CONSTRAINT | `"object has too many properties: %u, maximum is: %u"` | obj->len, minmax |
| CONSTRAINT | `"object has non-allowed property %s"` | key |
| CONSTRAINT | `"object has missing property %s"` | required name — **note: code is MISSING_PROPERTY, not CONSTRAINT** (see table row below) |
| MISSING_PROPERTY | `"object has missing property %s"` | required name |
| MISSING_DEPENDENCY | `"dependency %s is missing for key %s"` | dep name, key |
| INVALID_SCHEMA | `"multipleOf must be greater than zero"` | — |
| CONSTRAINT | `"number %.4f is not multiple of %.4f, remainder is %.7f"` | val, constraint, remainder(val,constraint) |
| CONSTRAINT | `"number is too big: %.3f, maximum is: %.3f"` | val, constraint |
| CONSTRAINT | `"number is too small: %.3f, minimum is: %.3f"` | val, constraint |
| CONSTRAINT | `"string is too big: %u, maximum is: %" PRId64` | obj->len, constraint |
| CONSTRAINT | `"string is too short: %u, minimum is: %" PRId64` | obj->len, constraint |
| INVALID_SCHEMA | `"cannot compile pattern %s"` | pattern string |
| CONSTRAINT | `"string doesn't match regexp %s"` | pattern string |
| CONSTRAINT | `"duplicate values detected while uniqueItems is true"` | — |
| UNKNOWN | `"cannot allocate tree node"` | — (malloc failure path; irrelevant in Go) |
| INVALID_SCHEMA | `"items attribute is invalid in schema"` | — |
| INVALID_SCHEMA | `"additionalItems attribute is invalid in schema"` | — |
| CONSTRAINT | `"array has not enough items: %u, minimum is: %u"` | obj->len, minmax |
| CONSTRAINT | `"array has too many items: %u, maximum is: %u"` | obj->len, minmax |
| CONSTRAINT | `"array has undefined item"` | — (additionalItems:false and extra item present) |
| INVALID_SCHEMA | `"Type attribute is invalid in schema"` | — (unrecognised type string) |
| TYPE_MISMATCH | `"Invalid type of %s, expected %s"` | actual type name, expected type name |
| CONSTRAINT | `"object is not one of enumerated patterns"` | — (enum failure) |
| INVALID_SCHEMA | `"reference %s is invalid, missing path component"` | ref path segment |
| INVALID_SCHEMA | `"reference %s is invalid, invalid item number"` | ref path segment |
| INVALID_SCHEMA | `"reference %s is invalid, item number %d does not exist"` | ref segment, index |
| INVALID_SCHEMA | `"reference %s is invalid, contains primitive object in the path"` | ref segment |
| INTERNAL_ERROR | `"cannot allocate memory"` | — (malloc; irrelevant in Go) |
| INVALID_SCHEMA | `"cannot fetch reference %s: %s"` | url, underlying fetch/parse error |
| INVALID_SCHEMA | `"reference %s is invalid, empty path component"` | full ref string |
| INVALID_SCHEMA | `"reference %s is invalid, cannot find specified object"` | full ref string |
| CONSTRAINT | `"object has more values than defined: %ld"` | maxValues constraint (UCL-only, see §2) |
| CONSTRAINT | `"object has less values than defined: %ld"` | minValues constraint (UCL-only, see §2) |
| INVALID_SCHEMA | `"schema is %s instead of object"` | type name of the (non-object) schema node |

`err->obj` is always set to the most semantically relevant node: the schema keyword node for
schema-shape errors, or the data node for data-constraint errors (see each call site above; a Go
port can copy this 1:1 by keeping the same "subject" argument at each `create_error` call).

The exact numeric formatting (`%.3f`, `%.4f`, `%.7f`) is part of observable behavior only if you
plan to string-match error messages in tests; the upstream test driver (`test_schema.c`) does
**not** assert on message text, only on the pass/fail boolean (see §6), so exact float formatting
is not load-bearing for compatibility with the existing test corpus, but keep it close for
consistency/debuggability.

---

## 2. Draft-4 keywords implemented — exact semantics

Top-level dispatch order inside `ucl_schema_validate(schema, obj, try_array, err, root, ext_refs)`
(this order **is** observable behavior — e.g. which error wins when multiple keywords fail):

0. If `schema->type != UCL_OBJECT` → `INVALID_SCHEMA "schema is %s instead of object"`, fail
   immediately. (A schema that is e.g. `true`/`false`/a string is always an error — draft-4 boolean
   schemas are **not supported**.)
1. **`try_array` handling (UCL-only extension, see below)** — if true, checks `minValues`/
   `maxValues` then validates every element of the UCL "implicit array" (duplicate-key linked list,
   `obj->next` chain) individually with `try_array=false`, and returns without evaluating any other
   keyword at this level (the keywords below only ever run with `try_array=false`, i.e. on a single
   concrete data node).
2. **`enum`** (if array) — must match one entry via deep comparison (see below), else CONSTRAINT.
3. **`allOf`** (if array) — every sub-schema must validate (`try_array=true` recursion); first
   failure short-circuits and its error is propagated.
4. **`anyOf`** (if array) — sub-schemas tried in order; stops at first success. If none succeed,
   fails with the **error from the last attempted sub-schema** (whatever `err` was left holding).
   If one succeeds, `err->code` is explicitly reset to `UCL_SCHEMA_OK` (but `err->msg`/`err->obj`
   are **not** cleared — stale bytes may remain; only `code` is reset).
5. **`oneOf`** (if array) — must match **exactly one**. Implementation walks the whole array
   unconditionally: once a match is found (`ret==true`), every subsequent sub-schema is still
   evaluated only to check it does *not* also match; the loop `break`s early only when a *second*
   match is found (then `ret=false`, i.e. failure "matched more than one"). If nothing ever matched,
   `ret` stays false ("matched none"). Either failure mode returns false; there is **no distinct
   error message differentiating "0 matched" from ">1 matched"** — whatever error was left by the
   last sub-schema attempt is reported.
6. **`not`** (only honored if it is a `UCL_OBJECT`, i.e. `not: [...]` or `not: "x"` is silently
   ignored) — validating `not`'s subschema against the same object with `try_array=true`; if it
   *succeeds*, this schema fails (no error message is set for this specific failure — `err` retains
   whatever the (successful) inner validation left, which is `UCL_SCHEMA_OK`/stale content, so a
   `not` failure reports as if there were **no error info**, just `false`). If the inner validation
   fails (i.e. `not` is satisfied), `err->code` is reset to `UCL_SCHEMA_OK` the same way as `anyOf`.
7. **`$ref`** — see §4. Resolved and recursively validated (with the *same* `try_array` the caller
   passed in) **before** the `type` check and before per-type validation. A schema can combine
   `$ref` with sibling keywords (`properties`, `type`, etc.) — draft-4/2019 draft debates about
   `$ref` "overriding" siblings are moot here: libucl evaluates `$ref` and then **continues on to
   evaluate `type` and the per-type validator using the *original* schema object**, so `$ref` is
   purely additive, not exclusive. (This differs from strict draft-4-compatible tooling that treats
   `$ref` as the sole keyword; libucl does not.)
8. **`type`** — see below; failing this stops before any per-type validation.
9. Dispatch on **`obj->type`**:
   - `UCL_OBJECT` → object validator (`properties`, `additionalProperties`, `required`,
     `minProperties`, `maxProperties`, `patternProperties`, `dependencies`).
   - `UCL_ARRAY` → array validator (`items`, `additionalItems`, `uniqueItems`, `minItems`,
     `maxItems`).
   - `UCL_INT` / `UCL_FLOAT` → number validator (`multipleOf`, `maximum`/`exclusiveMaximum`,
     `minimum`/`exclusiveMinimum`).
   - `UCL_STRING` → string validator (`maxLength`, `minLength`, `pattern`).
   - anything else (`UCL_BOOLEAN`, `UCL_NULL`, `UCL_TIME`, `UCL_USERDATA`) → **no further keyword
     is applied**, always passes at this stage (booleans/null only ever get checked by `type`/
     `enum`/`allOf` etc., never by a body validator).

### `try_array` / implicit arrays / `minValues` / `maxValues` — **UCL-specific extension, not in draft-4**

UCL configs allow duplicate keys, which the parser threads into a singly linked list via each
object's `->next` pointer (an "implicit array"). Every "validate this value against this schema"
call site in the codebase (property values, array `items`, `allOf`/`anyOf`/`oneOf`/`not` members,
`dependencies` subschemas, `$ref` targets) invokes `ucl_schema_validate(..., try_array=true, ...)`.
When `try_array` is true:

```c
if (!ucl_schema_validate_values(schema, obj, err)) return false;
LL_FOREACH(obj, cur) {
    if (!ucl_schema_validate(schema, cur, false, err, root, external_refs)) return false;
}
return true;
```

- `ucl_schema_validate_values` checks two **non-standard, UCL-only schema keywords**:
  - `maxValues` (int): walks the `->next` chain; if the chain has more than `constraint` elements
    (`i > constraint`, so exactly `constraint+1` is already the failure — off-by-one, see error msg
    which prints `constraint` not `constraint+1`), fails with CONSTRAINT
    `"object has more values than defined: %ld"`.
  - `minValues` (int): fails with CONSTRAINT `"object has less values than defined: %ld"` if the
    chain has fewer than `constraint` elements.
  - Both are looked up only if present **and** of type `UCL_INT`; otherwise silently skipped (no
    error for wrong-typed `minValues`/`maxValues`).
- Then the schema is validated against **every** element of the duplicate-key chain individually
  (with `try_array=false` so this doesn't recurse forever).
- For ordinary parsed JSON (no duplicate keys), the chain always has exactly one element, so this
  step is a no-op pass-through — **the Go port only needs to implement this if it intends to
  support UCL's duplicate-key semantics**; if the Go validator's object model has no equivalent of
  implicit arrays (e.g. it's driven purely off `encoding/json`-shaped trees), this whole layer
  collapses to "validate the single value" and `minValues`/`maxValues` become dead/no-op keywords.
  Document this decision either way.
- Items are only appended once, so `minValues`/`maxValues` are documented as needed for spec
  fidelity but there is **no test-suite coverage** for them (they don't appear in any
  `tests/schema/*.json` file).

### `type`

`ucl_schema_type_is_allowed(type, obj, err)`:
- `type` absent → always allowed.
- `type` is an array → **allowed if the object matches ANY of the listed types** (recurses per
  element; returns true on first match, otherwise falls through to `return false` at the bottom —
  note: if none match, **no error is recorded** for the array form, only single-string type checks
  set an error; a Go port should decide whether to still surface *some* type-mismatch message for
  the array case, but stock libucl leaves `err` from whatever the last mismatch check was, i.e.
  typically empty/stale since single-string branch inside the recursive call already tried to set
  an error on each failed alternative — the *last* alternative's error persists in `err`, since
  each recursive call to `ucl_schema_type_is_allowed` for a losing alternative does call
  `ucl_schema_create_error` with TYPE_MISMATCH before returning false, and this error is only
  overwritten by the *next* alternative attempted, so effectively you get the error for the LAST
  type in the `type` array that didn't match).
- `type` is a string → resolved via `ucl_object_string_to_type` (case-insensitive `strcasecmp`):
  `"object"→UCL_OBJECT`, `"array"→UCL_ARRAY`, `"integer"→UCL_INT`, `"number"→UCL_FLOAT`,
  `"string"→UCL_STRING`, `"boolean"→UCL_BOOLEAN`, `"null"→UCL_NULL`, `"userdata"→UCL_USERDATA`
  (userdata is a libucl-internal wrapper type, irrelevant to a JSON-shaped Go port). Unknown string
  → `INVALID_SCHEMA "Type attribute is invalid in schema"`.
  - Exact type match → allowed.
  - **Compatibility exceptions** (deviation from strict draft-4 "integer means integer, number
    means integer-or-number"): if `obj->type == UCL_TIME` and schema type is `"number"` → allowed
    (UCL has a native duration/time scalar type that libucl treats as a float for schema purposes).
    If `obj->type == UCL_INT` and schema type is `"number"` → allowed (so `"integer"` is strict —
    only literal integers pass — but `"number"` accepts int, float, and time). A schema type of
    `"integer"` against a float value, or vice versa, is a straight mismatch (matches draft-4).
  - Otherwise → `TYPE_MISMATCH "Invalid type of %s, expected %s"` using
    `ucl_object_type_to_string` names (`"object"`, `"array"`, `"string"`, `"boolean"`, `"null"`,
    `"int"`... — confirm exact strings if you rely on message text; not exercised by the test
    corpus).
- `type` present but neither array nor string (e.g. a number/bool) → falls through all branches,
  returns `false` at the end **with no error message set** (silent failure — the caller
  `ucl_object_validate` will just report `false`/`UCL_SCHEMA_OK`-or-stale `err`). Edge case, no
  test coverage; worth a `// TODO/matches-C` note in the Go code rather than "fixing" it.

### `enum`

Only honored if `schema.enum` is a `UCL_ARRAY`. Object is valid if `ucl_object_compare(candidate,
obj) == 0` for any enum member, else CONSTRAINT `"object is not one of enumerated patterns"`.
See §"comparison semantics" below for exactly what "deep equal" means (it is **not** a generic deep
equality — comparison short-circuits on structural difference and is *not* commutative-safe for
NaN/edge floats, but is fine for JSON-shaped data).

### Object keywords (`ucl_schema_validate_object`)

Schema keys are walked in **hash-table iteration order** (`ucl_object_iterate` over the schema
object) — i.e. **not** declaration order and not deterministic across implementations; a Go port
using an ordered map (declaration order) will not change pass/fail outcomes (each keyword's effect
is independent / commutative within the object validator; the only cross-keyword ordering that
matters — additionalProperties-after-properties-loop, required-last — is hard-coded outside the
iteration loop, see below), so implement with map iteration or a fixed logical order; either is
observably equivalent.

- **`properties`** (object): for every declared property name that also exists in `obj`, recursively
  validate (`try_array=true`) the sub-value against the sub-schema. Properties in the schema that
  are absent from `obj` are simply skipped here (their presence/absence is `required`'s job).
- **`additionalProperties`**:
  - `false` → deny any property not explicitly covered by `properties` or `patternProperties`.
  - an object → that schema validates every such "leftover" property's **value**.
  - anything else (string/number/array/…) → `INVALID_SCHEMA "additionalProperties attribute is
    invalid in schema"`, hard fail (schema-shape error, independent of the data).
  - Only evaluated at all if `allow_additional==false` or `additional_schema!=NULL`; if
    `additionalProperties` is absent or `true`, this whole block is skipped — **default is "allow
    additional properties"**, matching draft-4.
  - Algorithm: iterate every key in `obj`; if the schema's `properties` has that key, it's covered;
    else if any `patternProperties` regex matches the key (see §3), it's covered; else it's
    "additional" and is checked against `false`/schema per above.
- **`required`** (array of strings): a value is `UCL_ARRAY` or else
  `INVALID_SCHEMA "required attribute is invalid in schema"`. Checked **after** the
  properties/additionalProperties/patternProperties pass (only if that pass didn't already fail),
  in required-array order; first missing property is reported and stops
  (`MISSING_PROPERTY "object has missing property %s"`). Names are matched via plain
  `ucl_object_lookup` (== plain key lookup, whatever case-sensitivity the object hash uses —
  default libucl object hashing is case-sensitive).
- **`minProperties`** / **`maxProperties`** (int-like): only checked if
  `ucl_object_toint_safe` succeeds on the schema value, i.e. the schema value's type is `UCL_INT`,
  `UCL_FLOAT`, or `UCL_TIME` (float/time get truncated to int64 by C cast — "losing decimal
  points", per the source comment). **A non-numeric `minProperties`/`maxProperties` schema value is
  silently ignored — not a schema error** (deviation: draft-4 requires these to be non-negative
  integers; libucl doesn't validate the schema shape here at all, it just no-ops on type mismatch).
  Compares against `obj->len` (property count). CONSTRAINT on violation, with `%u`-formatted counts.
- **`patternProperties`** (object of `pattern → subschema`): for each declared pattern, for each
  property in `obj` whose key matches (see §3), validate that property's value against the pattern's
  subschema (`try_array=true`). Multiple patterns can each independently match and validate the same
  property (patterns are not mutually exclusive; a key matching two patterns must satisfy both).
- **`dependencies`** (object): see `ucl_schema_validate_dependencies` below — only entered if the
  schema value at this key is itself a `UCL_OBJECT` (a `dependencies` value of a different UCL type
  is simply not recognized as this keyword at all by the dispatch `strcmp` + type guard, i.e. it's
  silently ignored, no error).

`ucl_schema_validate_dependencies(deps, obj, err, root, ext_ref)`: for every key `k` declared in
`deps`, if `obj` has a property named `k`:
  - if `deps[k]` is a `UCL_ARRAY` (property-dependency form): every array element (stringified via
    `ucl_object_tostring`) must name a property present in `obj`, else
    `MISSING_DEPENDENCY "dependency %s is missing for key %s"` (dependency name, then the key that
    triggered the check). Stops at first missing dependency.
  - if `deps[k]` is a `UCL_OBJECT` (schema-dependency form): the **whole `obj`** (not just property
    `k`) is validated against that sub-schema (`try_array=true`).
  - **any other type for `deps[k]`** (string, number, bool) → silently ignored, no effect, no error
    (deviation from draft-4, which requires array-of-strings or a schema).

### Array keywords (`ucl_schema_validate_array`)

- **`items`**:
  - as an **array** of schemas (positional/tuple validation): walk `obj`'s elements and the
    schema's `items` array **in lockstep**; for `i`-th schema entry, if there is a corresponding
    `i`-th data element, validate it (`try_array=false`). If the schema array is shorter than the
    data array, the first data index beyond the schema array's length is recorded as
    `first_unvalidated` (used below for `additionalItems`). If the schema array is *longer* than
    the data array, the extra schema entries are simply never matched against anything (no error —
    matches draft-4: `items` tuple entries with no corresponding data element impose no constraint).
  - as an **object** (single schema applied to all): every element of `obj` is validated against it
    (`try_array=false`), regardless of `additionalItems`.
  - anything else → `INVALID_SCHEMA "items attribute is invalid in schema"`.
  - absent `items` → no constraint (equivalent to `items: {}`).
- **`additionalItems`**: only meaningful when `items` was an **array** (tuple form); the object-form
  and absent-`items` cases never populate `first_unvalidated`, so this block is effectively a no-op
  then (draft-4-consistent: `additionalItems` only applies alongside tuple-style `items`).
  - `false` → if there are items beyond the tuple length, CONSTRAINT `"array has undefined item"`.
  - a schema (object) → every item at/after the tuple length is validated against it.
    **Implementation quirk**: the loop that walks additional items does
    `elt = ucl_array_find_index(obj, idx); while (elt) { validate(elt); elt =
    ucl_array_find_index(obj, idx++); }` — because `idx++` is a post-increment used *inside* the
    very first re-lookup, the **first additional item is validated twice** (redundant, not
    incorrect — validation has no side effects, so this only wastes a little work; a Go port
    should validate each additional item exactly once and will still match every test outcome).
  - anything else (string/number/bool typed value other than a real boolean) → `INVALID_SCHEMA
    "additionalItems attribute is invalid in schema"`.
  - default (absent) → additional items allowed, unconstrained.
- **`uniqueItems`** (bool): only acted on if present and literally `UCL_BOOLEAN`. If true, every
  pair of elements must be pairwise-distinct per `ucl_object_compare` (deep, type-sensitive — see
  §comparison below); implemented via a binary search tree keyed by `ucl_object_compare`, so it's
  effectively O(n log n) rather than O(n²); first collision found (tree insertion order = array
  iteration order) → CONSTRAINT `"duplicate values detected while uniqueItems is true"`, `err->obj`
  set to the **second (later)** occurrence.
- **`minItems`** / **`maxItems`** (int-like, same `ucl_object_toint_safe`-based rules and silent-
  skip-if-wrong-type deviation as `minProperties`/`maxProperties`): compares against `obj->len`
  (array element count). CONSTRAINT with `%u`-formatted counts.

### Number keywords (`ucl_schema_validate_number`)

All comparisons are done in `double` (via `ucl_object_todouble`, so `UCL_INT` values are widened to
double — large int64 values near 2^53 could lose precision; not a concern for realistic schema
tests but worth a comment in Go if using big.Int/precise int64 semantics).

- **`multipleOf`** (number > 0 required, else `INVALID_SCHEMA "multipleOf must be greater than
  zero"`): valid iff `fabs(remainder(val, constraint)) <= 1e-16` (`alpha = 1e-16`). Uses C99
  `remainder()` (IEEE remainder, result in `[-constraint/2, constraint/2]`), **not** `fmod`. Go's
  `math.Remainder` matches C's `remainder` semantics exactly — use that, not `math.Mod`. Failure →
  CONSTRAINT `"number %.4f is not multiple of %.4f, remainder is %.7f"`.
- **`maximum`** / **`exclusiveMaximum`**: `exclusiveMaximum` is only honored if present and
  literally `UCL_BOOLEAN` (a non-boolean `exclusiveMaximum`, e.g. a number as in later JSON-Schema
  drafts, is **silently treated as absent/false** — draft-4-correct behavior, but note this
  validator does *not* support the draft-6+ numeric `exclusiveMaximum` form at all). Fails if
  `val > constraint || (exclusive && val >= constraint)`. CONSTRAINT `"number is too big: %.3f,
  maximum is: %.3f"`.
- **`minimum`** / **`exclusiveMinimum`**: symmetric. Fails if `val < constraint || (exclusive &&
  val <= constraint)`. CONSTRAINT `"number is too small: %.3f, minimum is: %.3f"`.
- Note the schema iteration loop guards each branch with `elt->type == UCL_FLOAT || elt->type ==
  UCL_INT` for the constraint value itself (`multipleOf`/`maximum`/`minimum`); a non-numeric schema
  value for any of these three keywords is **silently ignored** (not a schema error), same
  soft-fail style as `minProperties` etc.

### String keywords (`ucl_schema_validate_string`)

- **`maxLength`** / **`minLength`**: only if the schema value's type is exactly `UCL_INT` (unlike
  the numeric constraints above, **no float/time coercion here** — a `maxLength: 2.0` schema value
  would be silently ignored since its UCL type is `UCL_FLOAT`, not `UCL_INT`). Compares against
  `obj->len`, the **byte length** of the UCL string (not a rune/codepoint count — multi-byte UTF-8
  characters count as multiple "length" units, matching how `strlen`-based C computes it; replicate
  with `len(string)` in Go, not `utf8.RuneCountInString`, to match byte semantics). CONSTRAINT
  `"string is too big: %u, maximum is: %" PRId64` / `"string is too short: %u, minimum is: %"
  PRId64`.
- **`pattern`**: only if the schema value's type is `UCL_STRING`. See §3 for regex flavor. Failure
  to even *compile* the pattern → `INVALID_SCHEMA "cannot compile pattern %s"` (this is treated as
  a hard validation failure at validate-time, not a separate up-front schema-compile phase — i.e.
  compilation is attempted fresh on every validation call, no caching). No match →
  CONSTRAINT `"string doesn't match regexp %s"`.

### `allOf` / `anyOf` / `oneOf` / `not`

Already covered in the top-level dispatch order above (§2 items 3-6). All four operate at the
top-level `ucl_schema_validate` regardless of `obj`'s type — i.e. they are **not gated by `type`**
and run even for e.g. `allOf` combined with array data. This matches draft-4 (these are
type-agnostic combinators).

### `$ref` / `definitions`

See §4 for the full resolution algorithm. `definitions` itself carries **no special handling** —
it's just an ordinary object property that happens to be a conventional place to park subschemas
that `$ref` points at; libucl does not, e.g., pre-validate the shape of `definitions` or forbid
extra properties there (it only becomes relevant because `#/definitions/foo`-style pointers resolve
into it via the generic object/array walker).

### Keywords **NOT implemented** (present in draft-4 but absent from `ucl_schema.c`):

- **`default`** — never referenced anywhere in `ucl_schema.c`. No default-value injection, no
  default-aware validation skip. Pure documentation-only keyword if present in a schema; completely
  inert.
- **`format`** — not implemented at all (no `date-time`/`email`/`uri`/etc. string-format checks).
- **`title` / `description`** — inert (never read), same as spec (annotations only).
- **`id`** (draft-4 schema identification / base-URI change for `$ref` resolution scope) — **not
  implemented**. libucl's `$ref` resolver has no concept of a changing base URI/scope from nested
  `id` keywords — this is confirmed by the disabled `refRemote.json.disabled` test file, which
  contains a commented-out "change resolution scope" test case using `id` (see §6/§7 — this test
  suite is entirely disabled in the CMake test runner, not just that one case).
- **Draft-6+ keywords** are naturally absent since this is a draft-4-only implementation:
  `const`, `contains`, `propertyNames`, `if`/`then`/`else`, `$id` (vs draft-4 `id`), boolean schemas
  (`true`/`false` as a whole schema value), numeric `exclusiveMinimum`/`exclusiveMaximum` (draft-4
  keeps these as booleans paired with `minimum`/`maximum`, which is what libucl implements).
- **Draft-4's `"type"` can theoretically be absent-with-implicit-`"any"`** — matches (absent `type`
  is always-allowed, per §"type" above).

---

## 3. Regex flavor: POSIX ERE, not PCRE — porting risk to Go's RE2

Both `pattern` (string validator) and `patternProperties`/pattern-matching-of-keys use **POSIX.2
extended regular expressions** via the C library `<regex.h>` (`regcomp`/`regexec`), specifically:

```c
regcomp(&re, pattern_string, REG_EXTENDED | REG_NOSUB)
regexec(&re, subject_string, 0, NULL, 0)   /* == 0 means "matches" */
```

- `REG_EXTENDED`: POSIX ERE syntax (`(...)`, `|`, `+`, `?`, `{m,n}`, character classes
  `[[:alpha:]]`, `^`/`$` anchors, `.`; **no** `\d`, `\w`, `\s`, `\b`, lookaround, backreferences,
  non-greedy quantifiers, or named groups — those are PCRE/Perl extensions POSIX ERE does not have).
- `REG_NOSUB`: only match/no-match is needed, no capture groups are ever extracted (so match
  position/groups are irrelevant to the port — a plain boolean "does this regex match anywhere in
  the string" is all that's needed).
- Matching is a **search** (regexec finds the pattern *anywhere* in the string, unless the pattern
  itself anchors with `^`/`$`), exactly like Go's `regexp.MatchString` (RE2 is also
  search-by-default, unanchored unless `^`/`$` present) — confirmed by the test case
  `"regexes are not anchored by default"` in `patternProperties.json` (pattern `"[0-9]{2,}"` matches
  key `"a31b"` because "31" appears inside it, not because the whole key is numeric).
- Matching is **case-sensitive**, no `REG_ICASE` flag is used anywhere — confirmed by test:
  pattern `"X_"` matches `"a_X_3"` but not `"a_x_3"`.
  is used anywhere — confirmed by test: pattern `"X_"` matches `"a_X_3"` but not `"a_x_3"`.
- No `(?i)`-style inline flags are used or needed in the test corpus.

### Mapping to Go

Go's `regexp` package (RE2 syntax) is a good match for the *character* of POSIX ERE, but it is a
**different, richer grammar** (a superset in some ways — RE2 supports `\d`, `\w`, non-capturing
groups `(?:...)`, Unicode classes — and a strict subset in others — RE2 has **no** POSIX character
equivalence classes `[=a=]`, collating symbols `[.foo.]`, or backreferences, same as POSIX ERE
lacks Perl backreferences too so that's not a regression). Concretely:

- `regexp.MustCompile` / `regexp.Compile` + `.MatchString(s)` replaces `regcomp`+`regexec` directly;
  return `false, err` on compile failure maps 1:1 to the `INVALID_SCHEMA "cannot compile pattern
  %s"` path.
- **POSIX bracket expressions** `[[:alpha:]]`, `[[:digit:]]`, `[[:space:]]`, etc. — RE2 **does**
  support the POSIX named classes inside `[...]` (documented in `regexp/syntax`), so these transfer
  directly; no porting risk here.
- **Interval expressions `{m,n}`** — supported identically in both.
- **Risk: POSIX ERE backreference-free guarantee is actually a non-issue** since POSIX ERE (as
  opposed to BRE) never had backreferences either — so no test in this corpus can rely on them, and
  none do (checked all `pattern.json`/`patternProperties.json` cases above: `^a*$`, `f.*o`, `a*`,
  `aaa*`, `[0-9]{2,}`, `X_`, plus in `ref.json`'s escaped-pointer test the *strings* `"tilda~field"`
  etc. are keys, not patterns).
- **Real risk to flag**: if a *user-supplied* schema (outside the bundled test corpus) uses POSIX
  ERE constructs RE2 cannot express — namely (a) **collating symbols/equivalence classes**
  `[.ch.]`/`[=a=]` (obscure, locale-dependent, essentially never used in the wild), or (b) patterns
  that rely on **greedy-vs-POSIX "leftmost-longest" semantics** differing from RE2's
  leftmost-first (Perl-like) semantics for *ambiguous* alternations — irrelevant here since
  `REG_NOSUB` mode only cares about match/no-match, not which alternative/subgroup matched, so
  leftmost-longest-vs-leftmost-first cannot change the boolean outcome for pure existence-matching.
  Net effect: **for boolean match/no-match purposes (which is all this validator ever needs), RE2 is
  a safe, behavior-preserving substitute for POSIX ERE**, with the sole caveat of POSIX
  collating-symbol/equivalence-class syntax (`[.x.]`, `[=x=]`) which RE2 rejects at compile time —
  such a schema pattern would need to fail identically to how it should be handled (glibc's
  `regcomp` actually also does not support these without full locale collation tables compiled in;
  practically both C and Go will simply fail to compile such an exotic pattern, which the port
  should surface as the same `INVALID_SCHEMA "cannot compile pattern %s"`).
- Anchoring nuance: Go's RE2 `^`/`$` by default match string start/end (not line start/end unless
  `(?m)`), same as POSIX ERE's default (non-multiline) `regcomp` behavior — no discrepancy.
- **No pattern caching** in the C source (compiled fresh every call) — the Go port may add an
  `*regexp.Regexp` cache keyed by pattern string as a pure performance optimization without changing
  observable behavior (compile errors are deterministic per pattern string).

---

## 4. `$ref` resolution algorithm

Entry point: whenever `ucl_schema_validate` sees a `$ref` key (any type — no check that it's a
string; `ucl_object_tostring` is called unconditionally, which on a non-string UCL value returns
`NULL` or a stringified form depending on type, likely producing a `NULL` ref string and an early,
probably crashing or no-match path in the C version — **treat a non-string `$ref` as
`INVALID_SCHEMA`-worthy in the Go port** as a defensive improvement; no test exercises this).

```
ucl_schema_resolve_ref(root, ref, err, ext_ref, &nroot) -> schema-object | NULL
```

### Step 1 — split the reference into "document" and "fragment"

- If `ref[0] == '#'`: this is a **same-document** reference. `p := ref[1:]` (the fragment, without
  the pointer's leading `#`). No document fetch occurs; the current `root` is used directly.
- Else: `ref` names an **external document**, optionally with a `#fragment` suffix.
  - Find the **last** `#` in the string (`strrchr`, i.e. rightmost `#`, not leftmost — matters only
    for URLs that could pathologically contain multiple `#`, essentially never in practice).
  - `url := ref[:hashIndex]` if a `#` was found, else `url := ref` (whole string, no fragment →
    fragment defaults to `""`, meaning "the whole fetched document").
  - Look up `url` as a literal string key in the **`ext_ref` cache** (a `UCL_OBJECT` passed through
    every validate call, functioning as `map[url]parsedDocument`, threaded through the whole
    validate tree and passable by the caller via `ucl_object_validate_root_ext`, or a
    validate-call-scoped empty map if the caller used the simpler entry points). Cache is keyed
    by the **raw url substring exactly as it appears before `#`**, no normalization.
  - **Cache miss** → fetch:
    - if `url` contains `"://"` anywhere → treat as a network URL, fetch via `ucl_fetch_url`
      (libcurl or platform fetch, gated by the `ENABLE_URL_INCLUDE` CMake option — in a build
      without that enabled this call presumably fails/is stubbed; irrelevant to the Go port's
      default posture — **remote HTTP(S) `$ref` fetching is an out-of-scope / opt-in feature you
      should treat as "not supported" by default**, matching how the upstream project itself gates
      it behind a build flag and further disables its own test suite for it, see below).
    - else → treat as a **local file path**, fetch via `ucl_fetch_file` (reads the file from disk
      relative to CWD/absolute path as given — no base-URI-relative resolution logic; the `id`
      keyword that would normally let JSON Schema documents rebase relative `$ref`s is not
      implemented at all, see §2).
    - Either fetch failure → `INVALID_SCHEMA "cannot fetch reference %s: %s"` (url, fetch error
      text or `"unknown"`).
    - On fetch success, parse the fetched bytes with a **fresh `ucl_parser`** (accepts UCL syntax,
      which is a JSON superset, so plain `.json` schema files parse fine). Parse failure →
      `INVALID_SCHEMA "cannot fetch reference %s: %s"` (url, parser error).
    - On parse success, the resulting document is **inserted into `ext_ref`** keyed by `url`
      (mutates the caller's cache object in place — this is why `ucl_object_validate_root_ext`
      exposes `ext_refs` as an out-parameter the caller can reuse across multiple top-level
      `ucl_object_validate*` calls to amortize repeated remote fetches) and used as `ext_obj`.
  - **Cache hit** → reuse the previously-parsed document as `ext_obj`.
  - `p := ref[hashIndex+1:]` (fragment after the last `#`) if a `#` was present, else `p := ""`.
  - **This entire branch (external document fetching) is explicitly out of scope for test parity**:
    the upstream test file exercising it, `tests/schema/refRemote.json.disabled`, is disabled (the
    `.disabled` suffix means the shell test runner's `for i in ${TEST_DIR}/schema/*.json` glob never
    picks it up — confirmed in `tests/schema.test`). **Recommendation: implement local-file `$ref`
    fetching as a straightforward file read + recursive parse (useful, low-risk, and it's the
    "else" branch with no special deps), but treat network `$ref` fetching as unimplemented/optional
    and not something the Go test suite needs to cover — matching upstream's own posture of shipping
    it disabled.**

### Step 2 — establish the resolution root for this reference

```
res := ext_obj if ext_obj != nil else root
*nroot = res
```

This `nroot` becomes the `root` used for any **further nested `$ref`s found while validating inside
the target of this ref** — i.e. once you've jumped into an external document, subsequent bare `#/…`
refs found *within that document's own subschemas* resolve against that document, not the original
caller's root. (Same-document `#/…` refs always keep `nroot == root`, the original root, since
`ext_obj` stays `NULL` in that path.)

### Step 3 — walk the JSON-Pointer-shaped fragment

- If fragment is empty (`p == ""`) → return `res` as-is (the whole document/root is the target —
  this is exactly what `"$ref": "#"` means, and how `ref.json`'s "root pointer ref" test works).
- Else, if `p[0] == '/'`, skip that leading slash (i.e. `#/foo/bar` and `#foo/bar` are treated
  identically once the leading `#` is stripped — a pointer that does *not* start with `/` after the
  `#` still gets walked component-by-component the same way; **no special-case rejection of a
  malformed non-`/`-leading fragment**).
- Split the remaining string on `/` into components (byte spans into the original C string, no
  copying). For each component in turn, `ucl_schema_resolve_ref_component(cur, component, len,
  err)`:
  - if `cur` is a `UCL_OBJECT` → `ucl_object_lookup_len(cur, component, len)` — an **exact,
    literal, byte-for-byte key lookup**. **Critically: no JSON Pointer `~0`/`~1` unescaping is ever
    performed** — a component containing a literal `~0` or `~1` is looked up *as those literal
    characters*, not unescaped to `~`/`/` respectively. Confirmed directly by `ref.json`'s
    "escaped pointer ref" test suite: schema keys are literally `"tilda~field"`, `"slash/field"`,
    `"percent%field"`, and the refs `"#/tilda~0field"`, `"#/slash~1field"`, `"#/percent%25field"`
    are expected to **fail to resolve** to those keys (all three test cases assert `"valid":
    false`) — i.e., because `"tilda~0field" != "tilda~field"` as raw strings, `"slash~1field" !=
    "slash/field"`, and `"percent%25field" != "percent%field"`, the pointer never finds the target
    key, `ucl_schema_resolve_ref` returns `NULL`, and the enclosing `$ref` validation fails. Note
    the failure mode here is a **schema-resolution error causing overall validation to report
    `false`** (which happens to coincide with the test's expected `valid:false`, since the *data*
    given — a string — would ALSO have failed a `{"type":"integer"}` check had the ref resolved
    correctly; the test's assertions can't fully distinguish "resolved-but-type-mismatched" from
    "failed-to-resolve", but the C source is unambiguous on inspection: **do not implement RFC 6901
    `~0`/`~1` unescaping, and do not implement `%`-decoding either** — match this literal-bytes
    behavior exactly).
    - missing key → `INVALID_SCHEMA "reference %s is invalid, missing path component"`.
  - if `cur` is a `UCL_ARRAY` → the component is parsed as an unsigned integer index via `strtoul`
    (base 10); if trailing non-digit characters remain that are neither `/` nor end-of-string →
    `INVALID_SCHEMA "reference %s is invalid, invalid item number"`; otherwise walks the array's
    linked list counting up to that index; **out-of-range index** →
    `INVALID_SCHEMA "reference %s is invalid, item number %d does not exist"`.
  - if `cur` is anything else (a scalar) → path continues into a non-container →
    `INVALID_SCHEMA "reference %s is invalid, contains primitive object in the path"`.
  - An **empty path component** (two consecutive `/` in the pointer, i.e. `p - c == 0` when a `/`
    is encountered) → `INVALID_SCHEMA "reference %s is invalid, empty path component"`.
- After walking every component, the final `res` **must be a `UCL_OBJECT`** (i.e. the ref must
  resolve to something that is itself a valid schema-shaped object) — if it's `NULL` or any other
  UCL type → `INVALID_SCHEMA "reference %s is invalid, cannot find specified object"`. (This means
  `"$ref": "#/items/0"` pointing at, say, `{"type": "integer"}` is fine since that's an object; but
  `$ref` can never point directly at a scalar/array leaf and successfully use it as a schema.)

### Step 4 — recurse

`ucl_schema_validate(refSchema, obj, try_array, err, nroot, ext_ref)` — validated with the **same**
`try_array` flag the `$ref`-bearing call itself received, and the possibly-updated `root` (`nroot`)
so that further refs *inside* the target schema resolve relative to wherever that schema document
lives.

### Cycles and recursion depth

**There is no explicit cycle detection or recursion-depth counter anywhere in `ucl_schema.c`.**
A schema like `{"$ref": "#"}` at the top level, if actually invoked, recurses into
`ucl_schema_validate` against the exact same `(schema, obj)` pair forever — true infinite recursion
/ stack overflow, **not bounded by data depth**, because the target of the ref-triggered recursive
call is the *whole schema again* validated against the *same* data node (not a sub-node). What
*does* naturally terminate in the corpus's actual test cases (`ref.json`'s "root pointer ref" and
"nested refs") is that the `$ref` only ever appears **nested inside a `properties` entry** (or
similar container-descending keyword), so each recursive step through the ref also steps one level
deeper into the *data* tree (via the enclosing `properties`/`items` validation call), and since UCL
data is a finite tree (no cycles possible in parsed data), recursion is bounded **in every test case
that exists**, but **not in general** for an adversarial/buggy schema. **Recommendation for the Go
port**: replicate the "no built-in limit" behavior for spec fidelity (don't silently change pass/
fail results), but consider adding an optional, generous hard recursion-depth guard (e.g. a
few hundred/thousand frames) purely as a DoS/stack-overflow safety net that returns
`UCL_SCHEMA_INTERNAL_ERROR`-equivalent rather than crashing the process — call this out explicitly
as an intentional *addition* beyond upstream behavior if you do it, since it's not something libucl
itself does.

---

## 5. How validation errors are produced, and short-circuit vs. collect

**Short-circuits — this validator never collects multiple errors.** Every validate function
returns a single `bool`, and the single `struct ucl_schema_error *err` (nullable) is populated with
at most one error at a time — each subsequent failed check simply overwrites whatever was in `err`
before it (there is exactly one `err` object threaded through the whole recursive call tree, never
copied or stacked). The very first constraint violation encountered (in the fixed evaluation order
laid out in §2) stops the whole validation and that failure's message is what ends up in `err`,
**except**:

- `anyOf`/`not` deliberately **reset `err->code` to `UCL_SCHEMA_OK`** when the combinator itself
  ultimately succeeds (even though intermediate sub-schema attempts may have written failure
  info into `err` along the way) — so a caller inspecting `err` after an overall-successful
  `ucl_object_validate` will see `code == UCL_SCHEMA_OK`, but **`err->msg` and `err->obj` are left
  as stale bytes from the last failed sub-attempt** (only `.code` is explicitly cleared, not the
  rest of the struct) — a real footgun if calling code checks `err->msg` without also checking
  `err->code == UCL_SCHEMA_OK` first. A Go port should represent "no error" unambiguously (e.g. a
  `nil` `*SchemaError` return, or an explicit `Code` field, populated fresh each call rather than
  mutated in place) to sidestep this entirely rather than replicate the stale-fields footgun,
  **as long as the boolean pass/fail outcome for every test case in the corpus is unchanged** (it
  will be — the test driver `test_schema.c` never inspects `err` except when `match` already
  differs from `expected valid`, i.e. only for **debugging a failing test**, not as part of the
  pass/fail assertion itself — see §6).
- Object/array "loop over children" validators (`properties`, `patternProperties`, `items`,
  `additionalProperties`/`additionalItems`, `dependencies`) stop at the **first** child that fails,
  they do not attempt to validate every property/item and collect all violations — this is a
  genuine "fail fast" validator end-to-end, not an "accumulate all errors" one. If you want a
  Go API that *can* collect multiple errors (nice for developer UX), make that a deliberate,
  clearly-separate addition/mode — the default/primary API should preserve fail-fast semantics
  since that's what the ported test corpus's pass/fail expectations were written against (they only
  assert the boolean, so a Go implementation that collects internally but still returns "invalid"
  correctly would also pass the ported tests — collecting is a safe *superset* enhancement here,
  unlike the `$ref` semantics above where behavior must match exactly).

`err->obj` is set to whichever UCL node is "the subject" of the failing check (documented per
message in §1's table / inline in §2) — useful for pointing a user at the offending part of either
the schema or the data.

---

## 6. Test driver structure (for a table-driven Go test)

Source: `tests/test_schema.c`, driven by shell wrapper `tests/schema.test`.

- The wrapper globs **`${TEST_DIR}/schema/*.json`** (a plain shell glob — **`.json.disabled` files
  are never matched** and thus never run; this is how `definitions.json.disabled` and
  `refRemote.json.disabled` are excluded from the suite without deleting them). Each matched file
  is piped whole into **stdin** of a single invocation of the compiled `test_schema` binary; the
  process **exit code** (0 == all suites in that file passed) is what the shell script checks per
  file, and the overall `ctest` target fails if *any* file's run has a nonzero exit code.
- **File shape**: each `*.json` file is a **top-level JSON/UCL array of "suite" objects**. Each
  suite object has exactly three required keys (missing any → the whole run aborts with "Bad test
  case" and `EXIT_FAILURE`, i.e. the test binary doesn't even keep going to try other suites once
  one is malformed — same as this repo's official json-schema-test-suite format):
  ```jsonc
  {
    "description": "<string, human-readable suite name>",
    "schema": { /* a JSON Schema draft-4 document, the "schema" for every test below */ },
    "tests": [
      {
        "description": "<string, human-readable case name>",
        "data": <any JSON value — the instance to validate>,
        "valid": <bool — expected ucl_object_validate() result>
      },
      ...
    ]
  }
  ```
- **Execution semantics**: for each suite (in file order), for each test case (in array order),
  call `ucl_object_validate(schema, data, &err)` and compare the boolean result against the case's
  `"valid"` field. **First mismatch anywhere aborts the entire process** (`return EXIT_FAILURE`
  immediately from `perform_test`/`perform_tests`/`main`'s loop) — i.e. this is not a "run
  everything and report a summary of failures" harness; it's fail-fast at the level of the whole
  binary invocation. On mismatch it prints the case description, expected-vs-actual as
  "valid"/"invalid", `err.msg`, then re-emits both `data` and `schema` as UCL/config-format text via
  `ucl_object_emit(..., UCL_EMIT_CONFIG)` for debugging, to stdout.
- Note `err` is a **single `struct ucl_schema_error` allocated once per file** (`memset` to zero at
  the very start of `perform_tests`, not per-test-case) and reused/overwritten across every suite
  and test case in that file — consistent with the fail-fast, non-isolated nature of the C harness;
  irrelevant to the Go port as long as each Go subtest constructs its own fresh error value.

### Recommended Go table-driven shape

- Load each `tests/schema/*.json` file (skip `*.json.disabled`) as fixture data (embed via
  `go:embed`, or read from disk relative to a known tests/schema dir — decide based on repo
  layout).
- Parse into a Go struct mirroring the shape above:
  ```go
  type suite struct {
      Description string          `json:"description"`
      Schema      json.RawMessage `json:"schema"`
      Tests       []struct {
          Description string          `json:"description"`
          Data        json.RawMessage `json:"data"`
          Valid       bool            `json:"valid"`
      } `json:"tests"`
  }
  ```
  (Using `encoding/json` against these fixtures is safe even though the production parser is a UCL
  parser, since every fixture file here is valid strict JSON — confirmed by inspection, no UCL-only
  syntax sugar like unquoted keys/comments/`.include` is used anywhere in `tests/schema/*.json`.)
- Unlike the C harness, **do not fail-fast across the whole file** — run every suite and every case
  as its own Go `t.Run(...)` subtest (nest as `file/suite-description/case-description`), and let
  `go test` report every failing case, not just the first. This is a strict improvement over the
  upstream harness and doesn't change what "pass" means per case.
- Assert only the boolean `Validate(schema, data) == tc.Valid` outcome, matching what the upstream
  suite actually checks — do **not** assert on exact error message text/format as a hard
  requirement (upstream itself never does), though it's reasonable to log `err` on failure for
  debuggability exactly like the C harness does.
- The two `.disabled` files should still be **read and understood** by whoever maintains the Go
  suite (they document `id`-based scope-changing refs and remote-URL refs) but should be either
  skipped outright or — if you do want coverage of local same-process "remote" fetch fallback
  behavior — adapted into a form that doesn't require live network access (e.g. serve fixtures from
  an in-memory `ext_refs`-equivalent or a `httptest.Server`), entirely optional and not required for
  parity with the shipped, enabled test corpus.

---

## 7. Recommended Go design

**Types**

```go
type ErrorCode int
const (
    SchemaOK ErrorCode = iota
    SchemaTypeMismatch
    SchemaInvalidSchema
    SchemaMissingProperty
    SchemaConstraint
    SchemaMissingDependency
    SchemaExternalRefMissing // reserved, never produced — keep for API parity
    SchemaExternalRefInvalid // reserved, never produced — keep for API parity
    SchemaInternalError
    SchemaUnknown
)

type SchemaError struct {
    Code ErrorCode
    Msg  string
    Obj  *Object // whichever node (schema or data) triggered it
}
func (e *SchemaError) Error() string { return e.Msg }
```

**Entry points** — mirror the three C functions directly, since they encode a real, used
capability (root-relative validation + a reusable external-ref cache), not just API sugar:

```go
func Validate(schema, obj *Object) (bool, *SchemaError)
func ValidateRoot(schema, obj, root *Object) (bool, *SchemaError)
func ValidateRootExt(schema, obj, root *Object, extRefs *Object) (bool, *SchemaError)
```

Return `(bool, *SchemaError)` rather than a mutate-in-place out-param — idiomatic Go, and sidesteps
the "stale err fields after anyOf success" footgun noted in §5 for free (a fresh, possibly-`nil`
`*SchemaError` per top-level call, never reused/mutated across sibling checks — internally you can
still thread a single mutable pointer through the recursive walk for efficiency, just make sure the
final return only surfaces the error that corresponds to the actual `false` result, and returns
`nil` cleanly on success regardless of what any transient inner sub-schema attempt produced).

**Internal recursive core** — one function taking the same five logical parameters as
`ucl_schema_validate`, `validate(schema, obj *Object, tryArray bool, ctx *validateCtx) *SchemaError`
where `ctx` carries `root *Object` and `extRefs map[string]*Object` (a plain Go map is a fine,
simpler replacement for the C `ucl_object_t*` used as a string-keyed cache — same semantics, no
need to round-trip through the UCL object model for this internal bookkeeping unless you want
`extRefs` to remain externally inspectable/passable as an `*Object` for API parity with
`ValidateRootExt`, in which case keep it as `*Object` at the API boundary and convert to/from a map
internally, or just use `*Object` throughout for simplicity/consistency with the rest of this port).

**Per-keyword dispatch**: implement as a straight-line function per §2's order (not a generic
"keyword registry"/plugin system — the keyword set is fixed, small, and has real cross-keyword
ordering dependencies as documented in §2 and §5, so a rigid ordered implementation is both simpler
and more obviously correct/portable than an extensible-registry design). One function per JSON
type-body (`validateObject`, `validateArray`, `validateNumber`, `validateString`), matching the
C file's structure closely enough that reviewers can diff against `ucl_schema.c` line-by-line.

**Object model dependency**: this validator needs, at minimum, from whatever `Object` type the rest
of the Go port already has: a type tag matching UCL's type enum (object/array/int/float/string/
bool/null/time/userdata), ordered-or-unordered child iteration for objects (order doesn't matter
per §2), array indexing, a `Compare(a, b *Object) int` matching `ucl_object_compare`'s exact
semantics (type-tag-first, then per-type: byte-string compare, numeric double-subtraction compare,
bool-as-int compare, elementwise array compare, key-driven object compare — reimplement this
verbatim, it's short), and — only if you choose to support UCL's duplicate-key "implicit array"
semantics per §2's `try_array`/`minValues`/`maxValues` discussion — a `Next()`/chain accessor. If
the Go port's data model is JSON-only (no duplicate-key chaining), explicitly drop `try_array`'s
looping (every value has exactly one element) and treat `minValues`/`maxValues` as recognized-but-
inert keywords (document this choice at the call site).

**Regex**: use `regexp.Compile` per pattern, no persistent state required for correctness (§3); add
an internal `map[string]*regexp.Regexp` compile-cache only as a perf optimization, guarded so a
first-time compile failure is still surfaced as `SchemaInvalidSchema` on every call (don't cache
errors silently in a way that changes which call reports the failure).

**`$ref`**: implement §4's algorithm directly — a `resolveRef(root *Object, ref string, extRefs
*Object) (target *Object, newRoot *Object, err *SchemaError)` helper, including the **deliberate
non-implementation of `~0`/`~1`/`%XX` unescaping** (raw literal key lookup only) and the **absence
of a recursion-depth guard** (or an optional, clearly-labeled-as-an-addition depth guard — your
call, see §4). Gate real network fetching behind an explicit, injectable fetcher interface
(`type RefFetcher interface { Fetch(url string) ([]byte, error) }`) defaulting to "local file only,
network disabled" to match upstream's own default-off posture for `ENABLE_URL_INCLUDE`, and to
avoid surprising a Go consumer with implicit outbound network calls from a schema-validation
library call.

**Testing**: implement exactly the table-driven harness sketched in §6, embedding
`tests/schema/*.json` (excluding `.disabled`) via `go:embed` from a location the Go module controls
(copy the fixtures in, since this repo doesn't appear to be a git submodule of upstream — verify at
implementation time whether `libucl/` is vendored-in-place or symlinked and choose the embed source
accordingly).
