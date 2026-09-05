# libucl emitters — implementation spec for a pure-Go port

Reference: vstakhov/libucl v0.9.4 (`/home/claude/libucl-go/libucl`), files
`src/ucl_emitter.c` (721 lines), `src/ucl_emitter_utils.c` (562 lines),
`src/ucl_emitter_streamline.c` (178 lines), and the emitter declarations in
`include/ucl.h` (~lines 1419–1583). Ground truth for exact bytes is
`/home/claude/libucl-go/goucl/testdata/basic/*.{config,json,compact,yaml,comments}.golden`,
plus `libucl/tests/generate.res` and `libucl/tests/streamline.res`. Every claim
below that could plausibly be gotten wrong was checked against one of these
files, or against a small C/Go program compiled for this spec (§3). Where the
C source and a golden appear to disagree, **the golden wins** — this document
says so explicitly at each such point.

The msgpack emitter (`ucl_emit_msgpack_*`, `ucl_emitter_print_*_msgpack`) is
out of scope; it is covered by `docs/msgpack.md`. §7 says only where it plugs
into the vtable described here.

---

## 1. Architecture

### 1.1 The four/five emitter "kinds"

```c
typedef enum ucl_emitter {
    UCL_EMIT_JSON = 0,     /* pretty JSON */
    UCL_EMIT_JSON_COMPACT, /* compact JSON, no whitespace */
    UCL_EMIT_CONFIG,       /* human-readable "nginx-style" config */
    UCL_EMIT_YAML,         /* embedded-YAML-ish */
    UCL_EMIT_MSGPACK,      /* binary, out of scope here */
    UCL_EMIT_MAX
} ucl_emitter_t;
```

### 1.2 `ucl_emitter_context` and the vtable

```c
struct ucl_emitter_functions {
    int (*ucl_emitter_append_character)(unsigned char c, size_t nchars, void *ud);
    int (*ucl_emitter_append_len)(unsigned const char *str, size_t len, void *ud);
    int (*ucl_emitter_append_int)(int64_t elt, void *ud);
    int (*ucl_emitter_append_double)(double elt, void *ud);
    void (*ucl_emitter_free_func)(void *ud);
    void *ud;
};

struct ucl_emitter_operations {
    void (*ucl_emitter_write_elt)(struct ucl_emitter_context *ctx,
                                  const ucl_object_t *obj, bool first, bool print_key);
    void (*ucl_emitter_start_object)(struct ucl_emitter_context *ctx,
                                     const ucl_object_t *obj, bool first, bool print_key);
    void (*ucl_emitter_end_object)(struct ucl_emitter_context *ctx, const ucl_object_t *obj);
    void (*ucl_emitter_start_array)(struct ucl_emitter_context *ctx,
                                    const ucl_object_t *obj, bool first, bool print_key);
    void (*ucl_emitter_end_array)(struct ucl_emitter_context *ctx, const ucl_object_t *obj);
};

struct ucl_emitter_context {
    const char *name;                              /* "json", "config", ... */
    int id;                                         /* ucl_emitter_t */
    const struct ucl_emitter_functions *func;        /* sink: the "writer" */
    const struct ucl_emitter_operations *ops;        /* format vtable */
    unsigned int indent;                             /* current nesting depth */
    const ucl_object_t *top;                         /* the root object being emitted */
    const ucl_object_t *comments;                    /* optional, parser-saved comments */
};
```

There is exactly **one** `ucl_emitter_operations` table per format
(`ucl_standartd_emitter_ops[UCL_EMIT_JSON]`, `[..._JSON_COMPACT]`,
`[..._CONFIG]`, `[..._YAML]`, `[..._MSGPACK]`), built by the
`UCL_EMIT_TYPE_OPS`/`UCL_EMIT_TYPE_IMPL` macros. For JSON, JSON_COMPACT,
CONFIG and YAML all five vtable functions are thin wrappers that call one of
two shared, format-parametrised implementations:

- `ucl_emitter_common_elt(ctx, obj, first, print_key, compact)` — writes one
  scalar/object/array value (dispatches on `obj->type`).
- `ucl_emitter_common_start_object` / `_end_object` / `_start_array` /
  `_end_array` — open/close a container and walk its children.

The **only** thing that differs between JSON, JSON_COMPACT and YAML at the
call-site level is the boolean `compact` flag baked in by the macro
(`UCL_EMIT_TYPE_IMPL(json, false)`, `(json_compact, true)`, `(yaml, false)`);
CONFIG shares the same functions too (`UCL_EMIT_TYPE_IMPL(config, false)`) but
internally every one of those shared functions branches on
`ctx->id == UCL_EMIT_CONFIG` to run CONFIG-only logic. So: **there are really
only two code paths — "CONFIG" and "everything else (JSON/JSON_COMPACT/YAML)"
— with `compact` as a secondary flag inside the second path.** A Go port
should feel free to mirror this (one function per concern, with an
`isConfig`/`compact` pair of bools) rather than literally instantiating five
vtables — see §8.

### 1.3 Top-level entry points

```c
unsigned char *ucl_object_emit(const ucl_object_t *obj, enum ucl_emitter emit_type);
unsigned char *ucl_object_emit_len(const ucl_object_t *obj, enum ucl_emitter emit_type, size_t *outlen);
bool ucl_object_emit_full(const ucl_object_t *obj, enum ucl_emitter emit_type,
                          struct ucl_emitter_functions *emitter,
                          const ucl_object_t *comments);
```

`ucl_object_emit`/`_len` allocate an in-memory sink (`ucl_object_emit_memory_funcs`)
and call `ucl_object_emit_full` with `comments = NULL`. `ucl_object_emit_full`
is the true root:

```c
ctx = ucl_emit_get_standard_context(emit_type);   /* picks the static vtable */
my_ctx = *ctx;
my_ctx.func = emitter;
my_ctx.indent = 0;
my_ctx.top = obj;
my_ctx.comments = comments;
my_ctx.ops->ucl_emitter_write_elt(&my_ctx, obj, /*first=*/true, /*print_key=*/false);
```

Note the **root object is emitted through the exact same `write_elt` →
`ucl_emitter_common_elt` path as every nested value** — there is no special
"top-level" function. Top-level-ness is detected inline by comparing
`ctx->top == obj` (pointer equality) at the two places that care:

```c
#define UCL_EMIT_IDENT_TOP_OBJ(ctx, obj) \
    ((ctx)->top != (obj) || ((ctx)->id == UCL_EMIT_JSON_COMPACT || (ctx)->id == UCL_EMIT_JSON))
```

Read this as: **"should this object print its own `{`/`}` (or `[`/`]`)
wrapper?"** — yes unless it *is* the root *and* the format is CONFIG or YAML.
So:

| format | root object gets `{`/`}` wrapper? |
|---|---|
| JSON, JSON_COMPACT | yes, always |
| CONFIG, YAML | **no** — root object's key/value pairs are printed directly at indent 0 |

This macro is also evaluated for arrays in `ucl_emitter_common_start_array`
(`ctx->indent++` unconditionally happens there regardless — arrays are
**always** bracketed, even at the root, in every format; only *object*
wrapping is suppressed at the root for CONFIG/YAML).

Wait — re-reading `ucl_emitter_common_start_array`: it does **not** consult
`UCL_EMIT_IDENT_TOP_OBJ` at all; it always prints `[`/`[\n` and always
`ctx->indent++`. Only `ucl_emitter_common_start_object`/`_end_object` consult
the macro. So a bare top-level UCL array (rather than the usual top-level
object) is *always* bracketed in every format, including CONFIG.

### 1.4 The tree-walking algorithm

`ucl_emitter_common_elt` (the workhorse — read it once, it explains
everything):

```c
static void ucl_emitter_common_elt(ctx, obj, first, print_key, compact) {
    if (ctx->id != UCL_EMIT_CONFIG && !first) {
        /* leading separator before this element — see §2 per format */
    }
    ucl_add_tabs(func, ctx->indent, compact);      /* indent */
    /* look up ctx->comments for `obj`; maybe print a "before" comment (§5) */
    switch (obj->type) {
    case UCL_INT:      print_key(); append_int(...);    finish_object(...); break;
    case UCL_FLOAT:
    case UCL_TIME:     print_key(); append_double(...); finish_object(...); break;
    case UCL_BOOLEAN:  print_key(); "true"/"false";      finish_object(...); break;
    case UCL_STRING:   print_key(); write string (format+flag dependent); finish_object(...); break;
    case UCL_NULL:     print_key(); "null";              finish_object(...); break;
    case UCL_OBJECT:   common_start_object(...); common_end_object(...); break;
    case UCL_ARRAY:    common_start_array(...);  common_end_array(...);  break;
    case UCL_USERDATA: print_key(); write_json(ud->emitter(obj) or "null"); finish_object(...); break;
    }
    /* maybe print an "after" comment (§5) */
}
```

`ucl_emitter_common_start_object` walks the object's hash table
(`ucl_hash_iterate`, one call per **distinct key**) and, for each key, either:

- **CONFIG**: `LL_FOREACH(cur, elt)` over the `->next` duplicate-key chain,
  calling `ucl_emitter_common_elt` **once per duplicate**, each with
  `print_key = true`. Duplicate keys are rendered as **repeated `key = value;`
  (or `key { ... }` / `key [ ... ]`) lines**, one after another, with no
  special separator between them (each one self-terminates — see §2.3).
- **JSON / JSON_COMPACT / YAML**: if `cur->next != NULL` (there *is* a
  duplicate chain), the whole chain is wrapped into one **synthetic array**:
  a comma (if not the first key) + indent, then
  `ucl_emitter_common_start_array(ctx, cur, /*first=*/true, /*print_key=*/true, compact)`
  walks `cur, cur->next, cur->next->next, ...` as the array's implicit-array
  elements (see next paragraph), then `common_end_array`. If there is no
  duplicate (`cur->next == NULL`), it's emitted as a normal single element.

  **This is the single most important cross-format difference to get right**:
  the same duplicate-key input renders as N repeated `key = ...;` lines under
  CONFIG, but as ONE `"key": [ ...N values... ]` array under JSON/YAML. Verified
  against `libucl/tests/generate.res` line `key4 [...]` immediately followed
  by `key4 = true;` (CONFIG — two *separate* `key4` entries, one an explicit
  array, one a bool) vs. `testdata/basic/8.yaml.golden`'s
  `section: [ {...}, {...}, {...}, {...} ]` (YAML — four duplicate `section`
  object values collapsed into one array under one `section:` key).

`ucl_emitter_common_start_array` walks either:

- `obj->type == UCL_ARRAY` (an *explicit* array): `ucl_object_iterate(obj, &iter, true)`
  over its elements, each emitted with `print_key = false`.
- otherwise (an *implicit* array — i.e. `obj` is itself one link of a
  duplicate-key `->next` chain, passed in by the object-walker above): walks
  `cur = obj; while (cur) { emit(cur); cur = cur->next; }`, again
  `print_key = false` for every element (the shared key was already printed
  once, by the array's own `print_key` argument, before `[` was opened).

So: **the `->next` pointer chain (duplicate keys under one hash bucket) and
the `iter`-based walk of a real `UCL_ARRAY`'s elements are structurally
different linked lists, but `ucl_emitter_common_start_array` handles both
through the same function by branching on `obj->type`.** A Go port's object
model needs the equivalent of both: a `Next *Object` sibling pointer for
duplicate keys, and a genuine ordered element list for arrays.

---

## 2. Per-format rendering rules

Indentation is **always 4 spaces per level, never tabs**, added by:

```c
static inline void ucl_add_tabs(func, unsigned int tabs, bool compact) {
    if (!compact && tabs > 0) {
        func->ucl_emitter_append_character(' ', tabs * 4, func->ud);
    }
}
```
(the function is misleadingly named — it emits spaces, not tab characters).
`ctx->indent` starts at 0 and is incremented on entering a container, before
its children are walked, and decremented again in the matching `end_object`/
`end_array` before the closing brace/bracket is printed — so a container's
own closing delimiter is indented one level *less* than its children, at the
same level as its opening line.

`compact` (true only for JSON_COMPACT) suppresses **all** whitespace:
`ucl_add_tabs` is a no-op, and every place that would emit `"\n"`, `", "`,
or `": "` emits nothing / a single `,` / a single `:` instead.

### 2.1 Keys — quoting and key/value separator

`ucl_emitter_print_key(print_key, ctx, obj, compact)`:

```c
if (!print_key) return;

if (ctx->id == UCL_EMIT_CONFIG) {
    if (obj->flags & UCL_OBJECT_NEED_KEY_ESCAPE)
        ucl_elt_string_write_json(obj->key, obj->keylen, ctx);   /* "quoted" */
    else
        func->ucl_emitter_append_len(obj->key, obj->keylen, ud); /* bare */

    if (obj->type != UCL_OBJECT && obj->type != UCL_ARRAY)
        append(" = ");
    else
        append(" ");   /* single space before `{` / `[` — NOT "= " */
}
else if (ctx->id == UCL_EMIT_YAML) {
    if (obj->keylen > 0 && (obj->flags & UCL_OBJECT_NEED_KEY_ESCAPE))
        ucl_elt_string_write_json(obj->key, obj->keylen, ctx);
    else if (obj->keylen > 0)
        append(obj->key, obj->keylen);                           /* bare */
    else
        append("null");                                          /* empty key */
    append(": ");
}
else /* JSON, JSON_COMPACT */ {
    if (obj->keylen > 0)
        ucl_elt_string_write_json(obj->key, obj->keylen, ctx);    /* ALWAYS quoted */
    else
        append("null");
    append(compact ? ":" : ": ");
}
```

Key facts:
- **CONFIG**: bare key unless `UCL_OBJECT_NEED_KEY_ESCAPE` is set on the
  object (a parser-time flag meaning "this key isn't a valid bare UCL
  identifier and must be re-quoted"), e.g. `generate.res`: `"k=3" = true;`
  (key contains `=`) vs. unquoted `/usr/local/bin/pkgconf = "...";` (`/` *is*
  a legal bare key character in libucl's char table). If the value is itself
  an object or array, the separator is a **single space**, not `" = "` — e.g.
  `generate.res`: `key4 [` (array value) and `libucl/tests/generate.res`
  `section {` pattern in `8.config.golden`.
- **YAML**: same escape-flag logic, but an **empty key renders as the bare
  word `null`** (unquoted) as the key text, always followed by `": "`.
- **JSON/JSON_COMPACT**: keys are **always** JSON-double-quoted (the
  `NEED_KEY_ESCAPE` flag is irrelevant here — strict JSON requires quotes
  unconditionally), empty key → bare `null`. Separator is `": "` pretty,
  `":"` compact.

### 2.2 Element separator (leading comma) — JSON/JSON_COMPACT/YAML only

At the top of `ucl_emitter_common_elt`, `_start_object` and `_start_array`,
identical logic runs (this is the *only* place a comma is ever printed for
these formats — CONFIG never runs this block):

```c
if (ctx->id != UCL_EMIT_CONFIG && !first) {
    if (compact) {
        append(",");
    } else if (ctx->id == UCL_EMIT_YAML && ctx->indent == 0) {
        append("\n");                 /* top-level YAML: bare newline, NO comma */
    } else {
        append(",\n");
    }
    ucl_add_tabs(func, ctx->indent, compact);   /* NOT run for the ctx->indent==0 YAML case above except via the outer function — see note */
}
```
(`ucl_add_tabs` is called right after in `_start_object`/`_start_array`, but
in `_common_elt` it's called unconditionally afterward regardless of this
branch — same visible effect: indent always follows the separator.)

So separators are **leading**: each non-first sibling prints its own
`,`/`,\n`/`\n` *before* its own content, never a trailing one. The very last
sibling of a container prints no separator at all — the container's
`end_object`/`end_array` supplies its own closing newline (see §2.4).

The YAML `ctx->indent == 0` special case applies **only** to the object
walker's direct children (the root object's own key/value pairs, since a
bare-newline-separated top level is idiomatic YAML). It does *not* recurse
into nested objects/arrays (their indent is always > 0). Verified:
`testdata/basic/3.yaml.golden`'s and `2.yaml.golden`'s top-level keys
(`packagesite: ...`, `squaretest: ...`, `key6: 315360000.0`, ...) are
separated by **plain newlines with no commas**, while everything nested one
level down (inside `alias: { ... }`, inside `key1: [ ... ]`, etc.) uses
ordinary `,\n`.

### 2.3 CONFIG: entry terminator (trailing, per-element — no leading comma at all)

CONFIG never uses the leading-separator block above (skipped by the
`ctx->id != UCL_EMIT_CONFIG` guard). Instead, **every** element — scalar,
object, or array — unconditionally appends its own terminator immediately
after its own content, via `ucl_emitter_finish_object`:

```c
static void ucl_emitter_finish_object(ctx, obj, compact, bool is_array) {
    if (ctx->id == UCL_EMIT_CONFIG && obj != ctx->top) {
        if (obj->type != UCL_OBJECT && obj->type != UCL_ARRAY) {
            if (!is_array) append(";\n");   /* scalar directly in an object: ";\n" */
            else            append(",\n");   /* scalar directly in an array: ",\n" */
        } else {
            append("\n");                    /* object/array value: bare "\n" */
        }
    }
}
```

`is_array` here is `!print_key` at the scalar call sites (i.e. "was this
scalar printed without a key, meaning it's an array element") and a literal
`true`/`false` passed explicitly by `_end_object`/`_end_array` (`false` for
object members, `true` for array elements) when a nested container itself
closes.

So, for CONFIG:
- a `key = scalar` inside an object → trailing `";\n"`.
- a bare scalar inside an array → trailing `",\n"` (**note: this makes
  CONFIG-format arrays comma-terminated per-element, trailing comma included
  on the last element too** — see the worked example below,
  `libucl/tests/generate.res`'s `key4 [...]` block ends `10.100000,\n]` with
  a comma right before the closing bracket).
- a nested `key { ... }` or `key [ ... ]` (object/array value) → trailing
  bare `"\n"` after its own closing `}`/`]`.
- **the root object itself is exempt** (`obj != ctx->top` guard) — nothing is
  appended after the very last top-level entry beyond what that entry itself
  already produced.

This is why CONFIG has no notion of "is this the last sibling" logic at
all — every element, first or last, terminates itself the same way.

### 2.4 Opening/closing containers, empty containers

`ucl_emitter_common_start_object`:
```
[leading separator, non-CONFIG only, §2.2]
print_key(...)
if (UCL_EMIT_IDENT_TOP_OBJ(ctx, obj)) {         /* skip wrapper only for CONFIG/YAML root */
    append(compact ? "{" : "{\n")
    ctx->indent++
}
[walk children — §1.4]
```

`ucl_emitter_common_end_object`:
```
if (UCL_EMIT_IDENT_TOP_OBJ(ctx, obj)) {
    ctx->indent--
    if (compact)            append("}")
    else {
        if (ctx->id != UCL_EMIT_CONFIG)  append("\n")   /* JSON/YAML: own closing newline */
        ucl_add_tabs(ctx->indent)
        append("}")
    }
}
finish_object(ctx, obj, compact, /*is_array=*/false)    /* §2.3, CONFIG only */
```

`_start_array` / `_end_array` are identical except: (a) there is **no**
`UCL_EMIT_IDENT_TOP_OBJ` guard — arrays *always* get `[`/`]` and always
`ctx->indent++`/`--`, even at the document root; (b) `finish_object(..., is_array=true)`.

**Empty container behaviour** (verified against
`testdata/basic/14.config.golden`, `.yaml.golden`, `.json.golden` and
`8.config.golden`/`.yaml.golden`'s `array`):

| format | empty object | empty array |
|---|---|---|
| CONFIG | `key {\n}\n` (no blank line — closing `}` immediately follows the opening line, at the *same* indent as `key`) | `key [\n]\n` (same shape) |
| JSON (pretty) | `"key": {\n\n    },` — **one blank line inside** | `"key": [\n\n    ],` — same |
| JSON_COMPACT | `"key":{},` | `"key":[],` |
| YAML | `key: {\n\n}` — blank line inside | `key: [\n\n]` — blank line inside |

The blank line for non-CONFIG, non-compact formats is not a special case —
it falls out of the general rule mechanically: `start_object` always prints
`"{\n"`; when there are zero children, nothing else is printed by the walk;
then `end_object`, since `ctx->id != UCL_EMIT_CONFIG`, unconditionally prints
one more `"\n"` *before* the indent+`"}"` (the "own closing newline" line
above) — giving `"{\n" + "\n" + "    }"`. CONFIG skips that unconditional
newline (comment: *"newline is already added for this format"* — true for
non-empty CONFIG containers, since the last child already terminated its own
line — but for an *empty* CONFIG container there was no child to add that
newline, so CONFIG containers correctly get no blank line either way,
"by accident" of the guard being keyed off format rather than emptiness).

### 2.5 Worked examples

All four checked against `testdata/basic/2.*` and `8.*` goldens and
`libucl/tests/generate.res`. Given (conceptually) an object:

```
section1 = { param1 = "value" }
key1 = [1.0, "some string"]
```

**CONFIG** (`2.config.golden` — root object unwrapped, 4-space nesting, `;`
after scalars, no separator between entries beyond each one's own
terminator):
```
section1 {
    param1 = "value";
}
key1 [
    1.0,
    "some string",
]
```
(note the trailing comma before `]` — see §2.3.)

**JSON** (`2.json.golden` — root object wrapped in `{ }`, comma-then-newline
between siblings, no trailing comma):
```
{
    "section1": {
        "param1": "value"
    },
    "key1": [
        1.0,
        "some string"
    ]
}
```

**JSON_COMPACT** (`2.compact.golden` — no whitespace anywhere):
```
{"section1":{"param1":"value"},"key1":[1.0,"some string"]}
```

**YAML** (`2.yaml.golden` — root object unwrapped, top-level siblings
separated by a bare `\n` (no comma), nested siblings by `,\n`, no trailing
comma):
```
section1: {
    param1: "value"
}
key1: [
    1.0,
    "some string"
]
```

### 2.6 Trailing newline / whole-document framing

`ucl_object_emit_full`/`ucl_object_emit` produce **exactly** the bytes
described above — for CONFIG/YAML the document is *not* terminated with any
extra trailing newline beyond what the last top-level entry's own terminator
already contributes (§2.3); a scalar last entry ends in `";\n"`, a
container last entry ends in `"\n"` after its own `}`/`]`, full stop. JSON
and JSON_COMPACT end with the root's own closing `}` and nothing after it (no
trailing newline at all, not even for pretty JSON).

**Important pitfall**: every `testdata/basic/*.golden` file in this repo
ends with one extra blank line (an extra `"\n"`) beyond this. That is
**not** emitter behavior — it's an artifact of the test harness that
generated these goldens. `libucl/tests/test_basic.c` (the C program
`tools/gen-goldens.sh` drives) does, unconditionally, after emitting to the
output file:
```c
fprintf (out, "\n");   /* or write(fd_out, "\n", 1) for the -f path */
```
So every `.config.golden`/`.json.golden`/`.compact.golden`/`.yaml.golden`/
`.comments.golden` file here has one extra trailing `\n` glued on by the test
driver, not by `ucl_object_emit*`. A Go port's own emit functions must
**not** add this; when porting the *test*, add it the same way the C harness
does, not inside the emitter.

A second pitfall in the same harness: `test_basic.c` actually **round-trips
twice** — it emits once to an in-memory buffer, re-parses that text, and
*then* emits again (this second emission is what actually reaches the
golden file). For CONFIG/JSON/YAML this is normally invisible (the format
round-trips losslessly), but it does mean the `.comments.golden` files
reflect *parse → emit-with-comments → reparse → emit-with-comments again*,
not a single pass — relevant when validating comment placement (§5).

---

## 3. Number formatting

### 3.1 Integers (`UCL_INT`)

```c
utstring_printf(buf, "%jd", (intmax_t) val);   /* val is int64_t */
```
Plain base-10, no separators, no leading zeros, `-` prefix for negatives.
**Exact Go equivalent**: `strconv.FormatInt(val, 10)` (or
`strconv.AppendInt`). No edge cases — verified
(`testdata/basic/1.config.golden`: `key1 = -3735928559;` from source
`-0xdeadbeef`).

### 3.2 Doubles (`UCL_FLOAT` and `UCL_TIME` — identical treatment)

Both types call the exact same formatter — **`UCL_TIME` has no special
duration-string rendering at emit time**; a value parsed from `5s` becomes
a plain double `5.0` and is emitted exactly like any other float. The
formatter (present three times, once per sink — memory/file/fd — byte-for-byte
identical each time):

```c
const double delta = 0.0000001;
if (val == (double) (int) val) {
    printf("%.1lf", val);                 /* branch A: "12.0" */
} else if (fabs(val - (double) (int) val) < delta) {
    printf("%.*lg", DBL_DIG, val);        /* branch B: max-precision %g, DBL_DIG=15 */
} else {
    printf("%lf", val);                   /* branch C: default %f, i.e. 6 decimals */
}
```

`DBL_DIG` is 15 on every IEEE-754-double platform (glibc, musl, and any
realistic Go target's `float64`), so this is effectively a compile-time
constant to hardcode.

**The critical, easy-to-miss subtlety**: `(int)` is a **32-bit signed int**
cast. Branch A's equality test — and, transitively, branch B's "closeness"
test, since both compare against `(double)(int)val` — only ever succeeds when
`val` is an exact integer **that also fits in `int32` range**
(`-2147483648 ..= 2147483647`). Casting a double outside that range to `int`
is undefined behavior in C; empirically, on the reference build
(gcc 12/x86-64, used to generate the goldens), any value whose truncation
doesn't fit in `int32` compares unequal by a huge margin and falls straight
through to branch C, **even when the double is a perfectly whole number**.
Verified by direct compilation (not from a golden — no test fixture happens
to exercise this, so this is derived from first principles plus a
throwaway C program, `gcc -O0`, run against this exact code):

| input | branch | output |
|---|---|---|
| `1.0` | A | `1.0` |
| `315360000.0` | A | `315360000.0` |
| `2147483647.0` (int32 max) | A | `2147483647.0` |
| `2147483648.0` (int32 max + 1) | **C** | `2147483648.000000` |
| `-2147483648.0` (int32 min) | A | `-2147483648.0` |
| `-2147483649.0` (int32 min − 1) | **C** | `-2147483649.000000` |
| `4294967296.0` (2^32) | **C** | `4294967296.000000` |
| `0.01` | C | `0.010000` |
| `123.2` | C | `123.200000` |
| `-1e-10` | B | `-1e-10` |
| `1.0000000001` (within delta of 1) | B | `1.0000000001` |
| `0.99999999996` (within delta of 1, *from below*) | C, not B! | `1.000000` |
| `1e20` | C | `100000000000000000000.000000` (full expansion, never scientific) |
| `1.5e300` | C | a ~300-digit full decimal expansion, never scientific |
| `inf` / `-inf` / `nan` | C (branch A's cast is itself UB for these) | `inf` / `-inf` / `nan` (lowercase, glibc's printf spelling) |
| `0.0` / `-0.0` | A | `0.0` / `-0.0` |

The `0.99999999996` row is worth dwelling on: `(int)0.99999999996` truncates
**toward zero**, i.e. to `0`, not to the nearest integer `1`. So the
"closeness" test in branch B is `fabs(0.99999999996 - 0) < 1e-7`, which is
false — it does **not** count as "close to an integer" just because it
rounds up to one when displayed; branch C then applies its own 6-decimal
rounding and happens to print `1.000000` anyway. Do not implement branch B's
comparison using `math.Round`; it must be truncation-toward-zero
(`math.Trunc` for the in-range case).

Confirmed against real goldens for the in-range cases: `1.config.golden` /
`2.config.golden` (`key1 = 1.0;`, `key2 = 60.0;`, `key6 = 315360000.0;` —
branch A), `2.config.golden` (`key5 = 0.010000;` — branch C),
`8.config.golden` (`param = 123.200000;` — branch C), `1.config.golden`
(`key1 = -1e-10;` — branch B), `generate.res`/`streamline.res` (`key0 =
0.100000;`, `9.999000`, `10.100000`, `key9 = 3.140000;` — all branch C).

**Go recipe**, verified byte-identical against the C outputs above by direct
compilation of both `gcc` and `go1.27` on the same inputs (§ "Verification"
below):

```go
func formatDouble(val float64) string {
    const delta = 0.0000001
    const int32Min, int32Max = -2147483648.0, 2147483647.0

    inRange := val >= int32Min && val <= int32Max
    trunc := math.NaN()          // forces both comparisons below to fail
    if inRange {
        trunc = math.Trunc(val)  // truncate toward zero, like C's (int) cast
    }

    switch {
    case inRange && val == trunc:
        return strconv.FormatFloat(val, 'f', 1, 64)   // "%.1lf"
    case math.Abs(val-trunc) < delta:                 // always false when out of range
        return strconv.FormatFloat(val, 'g', 15, 64)  // "%.*lg", DBL_DIG=15
    default:
        return strconv.FormatFloat(val, 'f', 6, 64)    // "%lf" == 6 decimals
    }
}
```

Deliberately: rather than replicate C's undefined behavior for out-of-int32
doubles (which is not meaningfully specifiable and isn't exercised by any
golden), this pins the out-of-range case to always take branch C, which is
what every observed out-of-range case actually did. If a future golden shows
otherwise, revisit this.

**Infinity/NaN**: Go's `strconv.FormatFloat` spells these `+Inf`/`-Inf`/`NaN`
— **not** a match for C's (lowercase, no `+`) `inf`/`-inf`/`nan`. No golden
here exercises this (UCL has no infinity/NaN literal, and it would take a
constructed object, e.g. via `1/0` style computed input — none of the test
fixtures do this), but if byte fidelity to the reference C build is ever
required for these, special-case explicitly:
```go
switch {
case math.IsNaN(val):
    return "nan"
case math.IsInf(val, 1):
    return "inf"
case math.IsInf(val, -1):
    return "-inf"
}
```
before the branch-A/B/C logic (C's `(int)inf` cast is itself UB, so which
branch it nominally takes is moot — glibc's printf special-cases inf/nan
before ever looking at precision, for all three of `%f`/`%lf`/`%g`).

**Verification method** (for anyone re-checking this table): a C program
using the identical three-branch logic (`gcc -O0 -lm`) and a Go program
calling `strconv.FormatFloat` with the verbs/precisions above were compiled
and run side-by-side against the same 25 input doubles, including the int32
boundary values, `1e20`, `1.5e300`, `inf`/`-inf`/`nan`, and negative zero; all
outputs matched character-for-character except the inf/nan spelling noted
above (Go tool used: `go1.27.1 linux/amd64`, present in this container at
`/usr/local/go/bin/go`; C compiler: `gcc (Debian 12.2.0-14+deb12u1)`).

---

## 4. String escaping

There are **four** distinct string-writing routines
(`src/ucl_emitter_utils.c`), selected per §4.4:

### 4.1 `ucl_elt_string_write_json` — the default, and the *only* one used by JSON/JSON_COMPACT/YAML

Used for: every string in JSON/JSON_COMPACT/YAML (values *and* keys); CONFIG
keys needing escape; CONFIG string values that are neither multiline nor
single-quoted; `UCL_USERDATA`'s emitter-callback output (always, regardless
of format).

Wraps in `"`...`"` and walks the string byte-by-byte, testing each byte
against the static 256-entry table `ucl_chartable` (`src/ucl_chartable.h`)
for any of the three flags `UCL_CHARACTER_JSON_UNSAFE | UCL_CHARACTER_DENIED
| UCL_CHARACTER_WHITESPACE_UNSAFE`. Runs of un-flagged bytes are copied
verbatim in one `append_len` call (this is purely a performance detail — the
observable result is the same as testing one byte at a time). **No UTF-8
validation or decoding happens anywhere in this function — it is a pure
byte-classification table.**

Exact per-byte table (derived directly from `ucl_chartable.h`, cross-checked
against `testdata/basic/escapes.*.golden`):

| byte(s) | flagged? | escaped as |
|---|---|---|
| `0x00` (`\0`) | JSON_UNSAFE | `\uFFFD` (**literal fallback text, not `\0`** — see note) |
| `0x01`–`0x07` | DENIED | `\uFFFD` |
| `0x08` (`\b`) | JSON_UNSAFE | `\b` |
| `0x09` (`\t`) | WHITESPACE_UNSAFE+JSON_UNSAFE | `\t` |
| `0x0A` (`\n`) | WHITESPACE_UNSAFE+JSON_UNSAFE | `\n` |
| `0x0B` (`\v`) | WHITESPACE_UNSAFE only | `\u000B` (uppercase hex, 6-char literal) |
| `0x0C` (`\f`) | WHITESPACE_UNSAFE+JSON_UNSAFE | `\f` |
| `0x0D` (`\r`) | WHITESPACE_UNSAFE+JSON_UNSAFE | `\r` |
| `0x0E`–`0x1F` | DENIED | `\uFFFD` |
| `0x20` (space) | WHITESPACE_UNSAFE | **literal space, unescaped** (explicit `case ' '` in the switch) |
| `0x22` (`"`) | JSON_UNSAFE | `\"` |
| `0x5C` (`\`) | JSON_UNSAFE | `\\` |
| `0x7F` (DEL) | DENIED | `\uFFFD` |
| everything else, including all of `0x80`–`0xFF` | *unflagged* | copied verbatim, byte-for-byte |

Note on `\uFFFD`: this is **not** a computed Unicode escape of the offending
codepoint — it is a hardcoded 6-character literal (`"\\uFFFD"`, the JSON
escape for U+FFFD REPLACEMENT CHARACTER) emitted for *any* byte that is
flagged but has no specific `case` in the switch. All such bytes (both the
never-valid C0 controls and the not-otherwise-special `0x00`) collapse to
this same fixed string; the original byte value is lost. Since all of
`0x80`-`0xFF` are unflagged, this path is reached only for genuine C0
control characters that have no named escape and for `0x7F`; ordinary UTF-8
continuation/lead bytes never take it.

Verified end-to-end against `testdata/basic/escapes.config.golden` /
`.json.golden`: source `"\r\n\b\t\f\\\"δஐὤ0￿some text"` —
the `δ` etc. escapes are decoded by the *parser* into raw UTF-8 bytes
(0xCE 0xB4 for U+03B4, etc.), and the *emitter* then re-emits those raw bytes
completely unescaped (they fall in the unflagged `0x80`–`0xFF` range) —
visible in `cat -A` output as `M-N M-4` (its octal/meta notation for
`0xCE 0xB4`). **The round-trip through the emitter does not restore `\uXXXX`
escapes — it always prefers raw UTF-8 bytes for anything not in the table
above.**

### 4.2 `ucl_elt_string_write_squoted` — CONFIG only, `UCL_OBJECT_SQUOTED` values

Used only for CONFIG-format string values whose object has the
`UCL_OBJECT_SQUOTED` flag set (the value was written with single quotes in
the source UCL, e.g. `a = 'b'`) **and** is not long enough to trigger
multiline mode (§4.3 runs its check first).

Escapes **only** `'` → `\'`. Nothing else — not backslash, not control
characters, not high bytes — is touched; the string is otherwise copied
verbatim between opening and closing `'`.

**Safety fallback**: before doing any of that, the whole string is
pre-scanned for a literal two-byte `\` immediately followed by `'` (i.e. an
already-escaped quote sequence baked into the stored string value itself,
which happens when the *parser*'s squoted-string reader left a literal
backslash in the decoded value because there is no way to represent a bare
backslash in squoted UCL). If found anywhere in the string, the **entire**
function bails out and instead calls `ucl_elt_string_write_json` (§4.1) —
i.e. falls back to ordinary double-quoted JSON-style escaping — to avoid
producing output that would parse back differently.

Verified against `testdata/basic/squote.config.golden`: `f`'s value (source
`'\0\e\\\\\\\\\` + line-continuation + `\''`) contains such a `\'` sequence
in its decoded form and is rendered
`f = "\\0\\e\\\\\\\\\\\\\\\\'";` — double-quoted, **not** single-quoted,
confirming the fallback fires. Plain cases in the same file confirm the
normal path: `a = 'b';`, `b = 'b\n\'a';` (only the quote is escaped; the `\n`
two-byte sequence from the source is passed through completely literally,
unescaped, since this routine never looks at backslash or control bytes),
`c = '';` (empty squoted string), `e = '"';` (a literal double-quote inside
single quotes needs no escaping at all here).

### 4.3 `ucl_elt_string_write_multiline` — CONFIG only, heredoc-style

Used for CONFIG string values where `ucl_maybe_long_string(obj)` is true:

```c
#define LONG_STRING_LIMIT 80
bool ucl_maybe_long_string(obj) {
    if (obj->len > 80 || (obj->flags & UCL_OBJECT_MULTILINE)) {
        return memchr(obj->value.sv, '\n', obj->len) != NULL;
    }
    return false;
}
```
i.e.: the string must contain at least one literal `\n` byte, **and** either
be longer than 80 bytes or have been parsed from an actual `<<EOD ... EOD`
heredoc in the source (`UCL_OBJECT_MULTILINE` flag). This check runs
*before* the squoted check in `ucl_emitter_common_elt`'s `UCL_STRING` case —
multiline wins over squoted if both would apply.

Output shape: `<<EOD\n` + the raw string bytes, completely unescaped + `\nEOD`
— **no trailing newline or terminator character beyond that** (the usual
CONFIG `finish_object` still appends its own `;\n` right after, per §2.3,
since this is a scalar value).

**Fallback**, checked first: if the raw string contains `"\nEOD\n"` anywhere,
or ends in `"\nEOD"`, the terminator `EOD` would prematurely close the
heredoc on re-parse — so instead the whole thing falls back to
`ucl_elt_string_write_json` (§4.1), exactly like the squoted fallback.

Verified against `testdata/basic/heredoc_eod.config.golden`: the source is a
JSON-double-quoted string containing the literal escape sequences
`\nEOD\n` (i.e., after parsing, actual newline bytes flanking the text
`EOD`), long enough to qualify for multiline mode — but because the decoded
content contains that exact `\nEOD\n` byte pattern, the fallback fires and
the golden shows it rendered as an ordinary JSON-double-quoted string
(`k = "line one ... triggers\nEOD\nline three";`), **not** as a `<<EOD`
block. This is the only fixture in this repo that exercises multiline mode,
and it exercises the fallback path, not the "happy path" — implement the
happy path (raw passthrough between `<<EOD\n` and `\nEOD`) directly from the
C source, there is no golden for it.

### 4.4 Which routine is chosen (CONFIG `UCL_STRING` case, full logic)

```c
if (ctx->id == UCL_EMIT_CONFIG) {
    if (ucl_maybe_long_string(obj))                 write_multiline(...);   /* §4.3 */
    else if (obj->flags & UCL_OBJECT_SQUOTED)        write_squoted(...);    /* §4.2 */
    else                                              write_json(...);      /* §4.1 */
} else {
    write_json(...);                                                       /* §4.1, always */
}
```

### 4.5 Binary data (`UCL_OBJECT_BINARY`)

**Not special-cased at all** by any of the four text formats (CONFIG, JSON,
JSON_COMPACT, YAML). The `UCL_STRING` case in `ucl_emitter_common_elt` never
inspects `UCL_OBJECT_BINARY` — that flag is consulted **only** by the
msgpack emitter (`ucl_emit_msgpack_elt`'s `UCL_STRING` case, which switches
between `ucl_emitter_print_binary_string_msgpack` and
`ucl_emitter_print_string_msgpack`; out of scope, see `docs/msgpack.md`). A
`UCL_STRING` object carrying raw binary bytes and the `UCL_OBJECT_BINARY`
flag, if emitted as CONFIG/JSON/YAML, goes through the ordinary escaping
rules in §4.1–4.4 exactly like text — including `\uFFFD` mangling for any
embedded C0 control bytes and complete passthrough of high (`0x80`–`0xFF`)
bytes, valid UTF-8 or not.

---

## 5. Comments (parser-saved, CONFIG format only)

**Attachment** (which comment string belongs to which object, and whether it
counts as "before" or "after" that object) is entirely parser-side machinery,
already specified in `docs/parser.md` §10 — read that first. This section
covers only how the **emitter** consumes what the parser produced.

`ctx->comments`, when non-nil, is a `UCL_OBJECT` keyed by the raw bytes of a
`ucl_object_t *` pointer, mapping to a (possibly multi-node, `DL_APPEND`-
chained) string object holding literal comment text — the parser's own
`ucl_save_comment` did zero transformation on it beyond stripping the `#`
comment's trailing newline (kept for `/* */` — see the byte-off-by-one note
below, which matters a lot for reproducing exact blank lines).

In `ucl_emitter_common_elt`, only reached when `ctx->comments != nil && ctx->id
== UCL_EMIT_CONFIG`:

```c
comment = lookup(ctx->comments, &obj)     /* keyed by obj's own pointer */
if comment != nil {
    if !(comment.flags & UCL_OBJECT_INHERITED) {
        /* "before" comment: print now, before the key/value, then forget it */
        for each node in comment (DL_FOREACH):
            append(node.text); append('\n'); ucl_add_tabs(ctx->indent)  // unconditional, every node
        comment = nil
    }
    /* else: leave `comment` set — printed AFTER the element, below */
}
[[ ... print the key/value/container as normal, per §1–§4 ... ]]
if comment != nil {   /* only true if it was INHERITED, i.e. an "after" comment */
    for each node in comment (DL_FOREACH):
        append(node.text); append('\n')
        if node.next != nil: ucl_add_tabs(ctx->indent)   /* NOT added after the LAST node */
}
```

Two asymmetries to get exactly right:
1. **"Before" comments** (`!INHERITED`) print *before* the element, and
   `ucl_add_tabs(ctx->indent)` runs after **every** node in the chain,
   including the last — because the element's own key needs to be indented
   next.
2. **"After" comments** (`INHERITED`) print *after* the element (after its
   own terminator, e.g. after a scalar's `;\n` or after a closed container's
   `}`/`]` + `\n`), and `ucl_add_tabs` runs **only between** nodes, never
   after the last one — because whatever comes next will indent itself via
   its own leading `ucl_add_tabs` call at the top of the next
   `ucl_emitter_common_elt`.

**The blank-line gotcha** (do not skip this): a `/* ... */` block comment is
saved by the parser with length `p - beg + 1` — **one byte past** the
closing `/` of `*/` (see `docs/parser.md` §3.2/§10; this is essentially an
off-by-one in the C parser, kept for fidelity). In practice that extra byte
is whatever character immediately follows the comment in the source — nearly
always the line's own `\n`. So a block comment's *stored* text frequently
already ends in `\n`. The emitter's own unconditional `append('\n')` after
each node (both branches above) then produces **two** consecutive newlines —
a blank output line — with no special-casing anywhere in the emitter itself.
A `#` line comment's stored text, by contrast, explicitly excludes its
trailing `\n` (`ucl_save_comment(parser, beg, p - beg)`, no `+1`), so it
never doubles up.

This single mechanism (extra captured byte + emitter's unconditional
newline) is what produces every blank line in
`testdata/basic/comments.comments.golden`, both at the top of the file
(before `obj {`) and mid-document (after the big nested `/* ... */` block,
before `# quoted`). **A Go port must replicate the parser's off-by-one
capture (or otherwise arrange for stored `/* */` comment text to include
that trailing byte) to reproduce these blank lines — this is not something
the emitter can fix on its own by inspecting the text**, since a `#` comment
with genuinely no trailing byte must *not* get a blank line, and the emitter
has no way to distinguish "this text happens to end in `\n`" from "insert a
blank line here" except by trusting whatever the parser stored.

Worked fragment (from `testdata/basic/comments.comments.golden`, annotated):
```
    key = "nested";              <- before-comment case: "# Nested comments" printed BEFORE this line,
                                     with ucl_add_tabs after it (so this line is correctly indented)
    /*
	adasdasdads
	...
	*/                        <- after-comment case: this whole nested /* */ block (ONE
                                     DL_FOREACH node) is printed AFTER "key = \"nested\";",
                                     with no indent before it (it directly follows the
                                     previous line's own "\n") and its own captured
                                     trailing byte (another "\n") produces the blank
                                     line that follows here, before "    # quoted"
    # quoted
    # Quotes
    /*
	key = "/* value"
	key = "*/value"
	*/
    key = "quotes";
```
(The remaining reconstruction of exactly which source fragment attaches to
which object, and why `# quoted` — textually trailing on the source's
`key = quotes # quoted` line — ends up rendered as a *leading* comment on
that same key, is parser attachment-order behavior; see `docs/parser.md` §10.)

Since `docs/parser.md` documents this fixture's C test methodology (recall
§2.6: `test_basic.c`'s `-C` mode round-trips parse → emit-with-comments →
reparse → emit-with-comments *again*), byte-exact validation against
`comments.comments.golden` requires reproducing that same double pass, not a
single emit.

---

## 6. The streaming emitter API (`ucl_emitter_streamline.c`)

A separate, small API for producing output incrementally, without ever
building/holding a complete `ucl_object_t` tree for the containers being
streamed (individual leaf values are still ordinary `ucl_object_t`s). It
wraps the *same* `ucl_emitter_context`/vtable machinery from §1 — it is not a
separate renderer, just a different driver of it — plus a private stack.

```c
struct ucl_emitter_context_streamline {
    /* verbatim copy of the standard ucl_emitter_context fields */
    const char *name; int id;
    const struct ucl_emitter_functions *func;
    const struct ucl_emitter_operations *ops;
    unsigned int indent;
    const ucl_object_t *top;
    const ucl_object_t *comments;
    /* + streamline-only: */
    struct ucl_emitter_streamline_stack *containers;   /* linked stack, LL_PREPEND/pop */
};
struct ucl_emitter_streamline_stack {
    bool is_array;
    bool empty;                 /* true until the first child of this container is written */
    const ucl_object_t *obj;    /* the container object itself (for end_object/end_array's `obj` arg) */
    struct ucl_emitter_streamline_stack *next;
};
```

Public surface (`ucl.h`), five functions:

- **`ucl_object_emit_streamline_new(obj, emit_type, emitter) -> *ctx`**
  Allocates the context (copies the standard static vtable/name/id for
  `emit_type`, `sctx->top = obj`), then immediately calls
  `ucl_object_emit_streamline_start_container(ctx, obj)` on the given root
  object. **This means the root container's pre-existing children (if `obj`
  already has some — e.g. it's a real, populated `ucl_object_t` you built up
  front) are walked and emitted immediately, synchronously, inside this
  call**, via the ordinary `ucl_emitter_start_object`/`_start_array` vtable
  entry (§1.4's tree-walk, unchanged). The streaming/incremental part only
  applies to whatever is added *after* this returns. Returns `nil` if
  `emit_type` is invalid.

- **`ucl_object_emit_streamline_start_container(ctx, obj) -> bool`**
  Pushes a new frame for `obj` (which must be `UCL_OBJECT` or `UCL_ARRAY`
  typed — anything else is API misuse, and the function returns `false`
  without pushing anything or calling into the vtable). Computes:
  - `print_key = (top frame exists) && !(top frame is an array)` — i.e. print
    this container's own `.key` unless it's a bare array element or this is
    the very first (root) container.
  - `first = (no frame existed yet)` — **true only for the very first call
    ever** (the initial root container from `_new`), **false for every
    subsequent call including a brand-new sibling container** pushed after a
    previous one was popped. This is passed straight through as the `first`
    argument to `ucl_emitter_start_object`/`start_array`, which (for
    JSON/JSON_COMPACT/YAML) controls whether a leading comma is printed
    (§2.2). **Caveat**: unlike the ordinary tree-walker, which tracks
    per-container "is this the first child of its own parent" via the
    hash-walk's own `first_key` bookkeeping, this streaming entry point has
    no equivalent — it only knows "is the container stack empty right now".
    In practice this is only visibly wrong for JSON/YAML output with *two or
    more sibling containers* pushed via `start_container`/`end_container` at
    the same nesting level: the second one *should* get a leading comma
    (`first=false`, which it correctly gets, since the stack isn't empty)
    — so actually this works out for that case. Where it can misbehave is if
    a container is popped all the way back to empty and then something else
    entirely starts again with stale expectations; the only shipped test
    (`test_streamline.c`) never exercises more than one streamed container,
    so this path is under-tested upstream. Mirror the C logic as written
    (`first = containers stack was nil`) rather than "fixing" it, for
    behavioral fidelity, unless a golden ever demonstrates otherwise.
  - Then calls `ops->ucl_emitter_start_array` or `ucl_emitter_start_object`
    (per `obj->type`) with that `first`/`print_key`, and pushes the frame
    (`empty = true` initially) onto `sctx->containers` via `LL_PREPEND`.

- **`ucl_object_emit_streamline_add_object(ctx, obj)`**
  Adds one leaf/complete value as a child of the *current* (innermost open)
  container: `is_array = top.is_array`, `is_first = top.empty` (and clears
  `top.empty` after reading it), then calls
  `ops->ucl_emitter_write_elt(ctx, obj, is_first, !is_array)` — i.e. this
  computes correct **per-container** first/print_key bookkeeping (unlike
  `start_container` above), because the `empty` flag lives on the container
  frame and is properly threaded through each `add_object` call. `obj` here
  can be any complete `ucl_object_t`, including a whole pre-built nested
  object/array subtree — it is emitted via the ordinary recursive
  `write_elt`/tree-walk, not incrementally.

- **`ucl_object_emit_streamline_end_container(ctx)`**
  Pops the current frame and calls `ops->ucl_emitter_end_array`/`_end_object`
  (matching `frame.is_array`) with `frame.obj` (the container object pointer
  originally passed to the matching `start_container`) as the `obj` argument
  — needed because `_end_object`/`_end_array` consult `obj` for the
  `UCL_EMIT_IDENT_TOP_OBJ`/finish-terminator logic (§2.3–2.4). No-op if the
  stack is already empty.

- **`ucl_object_emit_streamline_finish(ctx)`**
  Repeatedly calls `end_container` until the stack is empty (auto-closing
  any containers the caller forgot to close explicitly), then `free(sctx)`.
  The caller is still responsible for freeing its own `emitter` functions
  separately (`ucl_object_emit_funcs_free`).

Worked example, from `libucl/tests/test_streamline.c` /
`libucl/tests/streamline.res` (CONFIG format): build a root `UCL_OBJECT` with
three ordinary string keys already inserted (`key1`, `key2`, `key3`), call
`ucl_object_emit_streamline_new` (which immediately emits all three, since
they already exist on the object — this is the "dump pre-existing children"
behavior noted above), then manually stream a fourth key as an array:
```c
ar = ucl_object_typed_new(UCL_ARRAY); ar->key = "key4"; ar->keylen = 4;
ucl_object_emit_streamline_start_container(ctx, ar);   /* prints "key4 [\n", pushes frame */
ucl_object_emit_streamline_add_object(ctx, from_int(10));      /* "    10,\n" */
ucl_object_emit_streamline_add_object(ctx, from_double(10.1)); /* "    10.100000,\n" */
ucl_object_emit_streamline_add_object(ctx, from_double(9.999));/* "    9.999000,\n" */
ucl_object_emit_streamline_end_container(ctx);         /* pops frame, prints "]\n" */
ucl_object_emit_streamline_finish(ctx);                /* stack already empty, just frees */
```
produces exactly:
```
key1 = "test string";
key2 = "test \nstring";
key3 = "  test string    \n";
key4 [
    10,
    10.100000,
    9.999000,
]
```
(Note: `streamline.res` itself has **no** extra trailing blank line — unlike
`test_basic.c`, `test_streamline.c` does not append a synthetic final `\n`;
it more directly reflects true emitter output. Also note `key2`'s golden
shows `\n` inside the quotes as a literal backslash-n text sequence exactly
as the JSON escaper (§4.1) produces it — the *source* string had a real
newline byte, escaped on output as `\n`.)

---

## 7. Where msgpack plugs in (out of scope, for orientation only)

`UCL_EMIT_MSGPACK`'s vtable entry (`ucl_standartd_emitter_ops[UCL_EMIT_MSGPACK]`)
is built by the same `UCL_EMIT_TYPE_CONTENT` macro as the four text formats,
but its five functions (`ucl_emit_msgpack_elt`, `_start_obj`, `_start_array`,
`_end_object`, `_end_array` — defined at the bottom of `ucl_emitter.c`) do
**not** call any of the shared `ucl_emitter_common_*` functions described in
§1–§4 at all; they call a wholly separate family of `ucl_emitter_print_*_msgpack`
helpers (declared in `ucl.h` around line 553–614, defined in
`src/ucl_msgpack.c`) that write raw msgpack bytes directly via `ctx->func`,
with no notion of indentation, key/value separators, or string escaping as
described here. `_end_object`/`_end_array` for msgpack are no-ops (msgpack
containers are length-prefixed up front, not delimited). See `docs/msgpack.md`
for the actual wire format. The only shared surface is `ucl_emitter_context`/
`ucl_emitter_functions` (the sink) and the outer entry points in §1.3
(`ucl_object_emit`, `ucl_object_emit_full`) — `emit_type == UCL_EMIT_MSGPACK`
just selects this different vtable at the same dispatch point.

---

## 8. Recommended Go design

**Vtable → a small `format` value + two bools, not five separate function
tables.** As noted in §1.2, the C vtable's apparent five-way split is
mostly cosmetic — CONFIG, JSON, JSON_COMPACT and YAML share the *same*
underlying logic and branch internally on `ctx.id`/`compact`. Reifying that
as five Go closures/interfaces per format would duplicate a great deal of
branching that the C code itself keeps unified. Prefer:

```go
type Format int
const (
    FormatJSON Format = iota
    FormatJSONCompact
    FormatConfig
    FormatYAML
)

type emitCtx struct {
    format  Format
    w       Writer          // the sink, see below
    indent  int
    top     *Object
    comments map[*Object]*Object   // nil unless comments were requested
}
```
with a handful of methods (`writeElt`, `startObject`, `endObject`,
`startArray`, `endArray`) that internally do exactly the `ctx.id ==
FormatConfig` / `compact := format == FormatJSONCompact` branching the C
source does — this is a direct, low-risk transliteration, and it's the same
shape the sibling `docs/*.md` specs in this repo recommend for the parser
(mirror the C control flow first; refactor to something more idiomatic only
in a later pass, per this project's stated direction in `HANDOFF.md`).

If a Go interface is wanted anyway (e.g. to support a future custom/pluggable
format), a natural seam is:
```go
type emitterOps interface {
    writeElt(ctx *emitCtx, obj *Object, first, printKey bool)
    startObject(ctx *emitCtx, obj *Object, first, printKey bool)
    endObject(ctx *emitCtx, obj *Object)
    startArray(ctx *emitCtx, obj *Object, first, printKey bool)
    endArray(ctx *emitCtx, obj *Object)
}
```
mirroring `ucl_emitter_operations` exactly, with one implementation per
format (msgpack's implementation living entirely in the msgpack package, per
§7) — but back it with the *same* shared internal helper functions rather
than reimplementing the logic four times.

**The writer/sink.** C's `ucl_emitter_functions` (append-character,
append-len, append-int, append-double, free) exists mainly to abstract over
memory (`UT_string`)/`FILE *`/raw fd, three concerns Go's standard library
already unifies as `io.Writer`. Recommend a single interface:
```go
type Writer interface {
    io.Writer                       // replaces append_len (and append_character via bytes.Repeat or a small loop)
    WriteByte(c byte) error          // io.ByteWriter; covers single-character appends without an allocation
}
```
with the int/double formatting (§3) done by the *caller* (produce a `string`/
`[]byte` via `strconv`, then call `Write`) rather than as separate interface
methods — there is no behavioral reason in the C code to keep those as
pluggable callbacks (all three concrete C implementations — memory/file/fd —
literally call the identical `snprintf`/`printf` format strings; only the
sink differs). This also sidesteps needing a `free` callback at all —
ordinary Go garbage collection replaces `ucl_emitter_free_func`; a
`bytes.Buffer`-backed writer needs no explicit teardown, and a `*os.File`
sink's lifetime is the caller's problem exactly as it is for `FILE *` in C
(the C code never closes `fp`/`fd` itself either — `ucl_object_emit_file_funcs`/
`_fd_funcs` only ever free the small function-table wrapper, not the
underlying file).

For the three convenience constructors (`ucl_object_emit_memory_funcs`,
`_file_funcs`, `_fd_funcs`): a `bytes.Buffer` (or just building a `string`
via `strings.Builder`) covers the memory case directly with `String()`/
`Bytes()` replacing the `outlen` out-parameter dance; `*os.File` implements
`io.Writer` already and covers both the `FILE *` and fd cases (Go has no
reason to distinguish buffered-stdio from a raw fd the way C does).

**Duplicate-key chains.** The object model needs a `Next *Object` field
(§1.4) distinct from an array's element slice, exactly mirroring
`ucl_object_t.next`. If `docs/object-model.md` already specifies this field
for other reasons (parser-side duplicate-key handling), the emitter can
reuse it as-is — this document does not need its own copy of that field,
only to flag that the emitter's array-walk (`ucl_emitter_common_start_array`)
and object-walk (`ucl_emitter_common_start_object`) both need to traverse it,
per §1.4.

**Comments.** `ctx->comments`'s C representation (a `ucl_object_t` hash
keyed by raw pointer bytes) has no clean Go equivalent and needs none — a
plain `map[*Object]*Object` (or `map[*Object][]string]`, since all a comment
chain node ever holds is text) does the same job with less indirection; see
§5 and `docs/parser.md` §10 for the map's producer side.
