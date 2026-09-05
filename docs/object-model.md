# libucl Object Model and Hash Table — Implementation Spec for a Pure-Go Port

Reference: vstakhov/libucl v0.9.4 (`/home/claude/libucl-go/libucl`), files
`include/ucl.h`, `src/ucl_util.c`, `src/ucl_hash.c`, `src/ucl_hash.h`,
plus the shared string→value helpers that live in `src/ucl_parser.c`
(`ucl_maybe_parse_number`) and `src/ucl_internal.h` (`ucl_maybe_parse_boolean`).

This document is the complete behavioural contract for the object model. It is written
so a Go programmer never has to open the C.

**Scope note / what I deliberately skipped in `ucl_util.c`** (another agent owns these):
`ucl_mmap`/`ucl_munmap`/`ucl_realpath`/`dirname`/`basename` (lines 98–186),
`ucl_pubkey_add`, `ucl_sig_check`, `ucl_curl_write_callback`, `ucl_fetch_url`,
`ucl_fetch_file` (716–937), the whole `.include`/`.load`/`.inherit`/`.priority`
macro family (`ucl_include_url`, `ucl_include_file_single`, `ucl_include_file`,
`ucl_include_common`, `ucl_include_handler`, `ucl_includes_handler`,
`ucl_try_include_handler`, `ucl_priority_handler`, `ucl_load_handler`,
`ucl_inherit_handler`, 1008–1976), `ucl_parser_set_filevars`, the
`ucl_parser_add_file*` / `ucl_parser_add_fd*` family (1977–2124), and the
parser lifecycle functions `ucl_parser_get_object`, `ucl_parser_free`,
`ucl_parser_get_error*`, `ucl_parser_get_column/linenum`,
`ucl_parser_clear_error`, `ucl_parser_add_special_handler`,
`ucl_chunk_free`, `ucl_parser_set_include_tracer`, `ucl_parser_get_cur_file`.
I *do* cover `ucl_unescape_json_string`/`ucl_unescape_squoted_string` only by
reference (they are parser-side; they are declared in `ucl_internal.h` and used
by the lexer). Everything else in `ucl_util.c` is specified below.

---

## 1. Enumerations and constants (exact numeric values)

### 1.1 `ucl_type_t` — the object type tag

| Name | Value | Meaning |
|---|---|---|
| `UCL_OBJECT` | 0 | key/value map |
| `UCL_ARRAY` | 1 | vector |
| `UCL_INT` | 2 | `int64` |
| `UCL_FLOAT` | 3 | `float64` |
| `UCL_STRING` | 4 | byte string (may contain NUL if `UCL_OBJECT_BINARY`) |
| `UCL_BOOLEAN` | 5 | bool (stored in the integer slot) |
| `UCL_TIME` | 6 | seconds as `float64` |
| `UCL_USERDATA` | 7 | opaque pointer + dtor + emitter |
| `UCL_NULL` | 8 | null |

The numeric order matters: `ucl_object_compare` returns `o1->type - o2->type`
for mismatched types, so type ordering is exactly this enum ordering.

### 1.2 `ucl_error_t`

`UCL_EOK=0, UCL_ESYNTAX=1, UCL_EIO=2, UCL_ESTATE=3, UCL_ENESTED=4,
UCL_EUNPAIRED=5, UCL_EMACRO=6, UCL_EINTERNAL=7, UCL_ESSL=8, UCL_EMERGE=9`.

### 1.3 `ucl_emitter_t`

`UCL_EMIT_JSON=0, UCL_EMIT_JSON_COMPACT=1, UCL_EMIT_CONFIG=2, UCL_EMIT_YAML=3,
UCL_EMIT_MSGPACK=4, UCL_EMIT_MAX=5`.

### 1.4 `ucl_string_flags_t` (input to `ucl_object_fromstring_common`)

| Name | Value |
|---|---|
| `UCL_STRING_RAW` | `0x0` |
| `UCL_STRING_ESCAPE` | `1<<0` = 1 |
| `UCL_STRING_TRIM` | `1<<1` = 2 |
| `UCL_STRING_PARSE_BOOLEAN` | `1<<2` = 4 |
| `UCL_STRING_PARSE_INT` | `1<<3` = 8 |
| `UCL_STRING_PARSE_DOUBLE` | `1<<4` = 16 |
| `UCL_STRING_PARSE_TIME` | `1<<5` = 32 |
| `UCL_STRING_PARSE_NUMBER` | `INT\|DOUBLE\|TIME` = 56 |
| `UCL_STRING_PARSE` | `BOOLEAN\|NUMBER` = 60 |
| `UCL_STRING_PARSE_BYTES` | `1<<6` = 64 |

Note `UCL_STRING_PARSE_BYTES` is **not** part of `UCL_STRING_PARSE`; on its own it
never triggers parsing (see §5.1).

### 1.5 `enum ucl_duplicate_strategy`

`UCL_DUPLICATE_APPEND=0, UCL_DUPLICATE_MERGE=1, UCL_DUPLICATE_REWRITE=2,
UCL_DUPLICATE_ERROR=3`. (Consumed by the parser, but it is the parser that
produces the `next` chains described in §2.3.)

### 1.6 `enum ucl_object_keys_sort_flags`

`UCL_SORT_KEYS_DEFAULT=0, UCL_SORT_KEYS_ICASE=1<<0, UCL_SORT_KEYS_RECURSIVE=1<<1`.

### 1.7 `enum ucl_iterate_type`

`UCL_ITERATE_EXPLICIT=1<<0, UCL_ITERATE_IMPLICIT=1<<1, UCL_ITERATE_BOTH=3`.

### 1.8 Priorities

`UCL_PRIORITY_MIN 0`, `UCL_PRIORITY_MAX 15`.

---

## 2. `ucl_object_t`, field by field

```c
typedef struct ucl_object_s {
    union {
        int64_t     iv;   /* Int value of an object */
        const char *sv;   /* String value of an object */
        double      dv;   /* Double value of an object */
        void       *av;   /* Array (kvec of ucl_object_t*) */
        void       *ov;   /* Object (ucl_hash_t*) */
        void       *ud;   /* Opaque user data */
    } value;
    const char *key;               /* Key of an object */
    struct ucl_object_s *next;     /* Array handle */
    struct ucl_object_s *prev;     /* Array handle */
    uint32_t keylen;
    uint32_t len;
    uint32_t ref;
    uint16_t flags;
    uint16_t type;
    unsigned char *trash_stack[2];
} ucl_object_t;
```

`UCL_USERDATA` objects are actually allocated as
`struct ucl_object_userdata { ucl_object_t obj; ucl_userdata_dtor dtor;
ucl_userdata_emitter emitter; }` — i.e. a C "subclass" with the base struct
first. In Go these are just two extra fields on the one object struct.

### 2.1 `value` — per type

| type | slot used | meaning |
|---|---|---|
| `UCL_OBJECT` | `ov` | `*ucl_hash_t` (may be `NULL` for an empty object created by `ucl_object_typed_new(UCL_OBJECT)`; it is lazily created on first insert) |
| `UCL_ARRAY` | `av` | `*kvec` of `ucl_object_t*` (preallocated to capacity 8 by `ucl_object_new_full`; can be `NULL` after `ucl_object_copy` of an empty array, and every array API function tolerates `NULL`) |
| `UCL_INT` | `iv` | the integer |
| `UCL_FLOAT` | `dv` | the double |
| `UCL_TIME` | `dv` | seconds as a double |
| `UCL_BOOLEAN` | `iv` | 0 or 1. Everything reads it as `value.iv == true`, i.e. `== 1` |
| `UCL_STRING` | `sv` | pointer to `len` bytes (NUL-terminated unless `UCL_OBJECT_BINARY`) |
| `UCL_USERDATA` | `ud` | opaque pointer |
| `UCL_NULL` | — | unused, zero |

Array elements may be `NULL` pointers: `ucl_array_replace_index` can store
anything, and iteration/compare explicitly test for `NULL` slots.

### 2.2 `key` / `keylen`

The key is a **byte slice, not a C string**: `keylen` is authoritative and the
bytes need not be NUL-terminated (in zero-copy parsing the key points straight
into the input buffer). `key` may be `NULL` (top-level objects, array elements,
free-standing scalars); `ucl_object_key()` returns `NULL` in that case.

Keys are compared and hashed by exactly `keylen` bytes (`memcmp`), never by
`strcmp`. Note libucl uses raw pointer values as keys in the comments table
(`ucl_comments_add` inserts with `key = (const char*)&obj, keylen = sizeof(void*)`),
so key bytes are genuinely arbitrary binary.

### 2.3 `next` / `prev` — the implicit-array chain

Every object is the head of a **circular-prev / NULL-terminated-next** doubly
linked list (the `utlist.h` "DL" convention):

* A fresh object has `next == NULL` and `prev == self`.
* For a chain `h -> a -> b`: `h.next=a, a.next=b, b.next=NULL`,
  `h.prev=b` (head's prev points to the **tail**), `a.prev=h, b.prev=a`.

This chain is the representation of an **implicit array**: when a key appears
more than once and implicit arrays are enabled, the second and subsequent values
are appended to the chain of the first value, and only the *head* lives in the
hash table. `ucl_object_lookup` returns the head; the caller walks `next` (or
uses `ucl_object_iterate` with `expand_values == false`, or the safe iterator).

`DL_APPEND(head, elt)` (used by `ucl_parser_append_elt` and
`ucl_object_copy_internal`) appends a single element; `DL_CONCAT(found, elt)`
(used by `ucl_object_insert_key_common` in the non-merge, non-replace path)
splices *elt's entire chain* onto the tail of *found's* chain, fixing up
`found->prev` to elt's old tail.

The list is also used for a completely different purpose in the parser: the
parser's `trash_objs` list and the emitter's comment ordering. For the object
model, treat `next`/`prev` purely as "duplicate values for this key".

Two subtleties worth burning into the Go port:

1. `ucl_elt_append(head, elt)` special-cases `UCL_USERDATA` heads
   ("Userdata objects are VERY special!") — but the code path is byte-identical
   to the normal path because `ucl_object_userdata` embeds `ucl_object_t` first.
   Treat both branches as one.
2. `ucl_object_copy_internal` unlinks the copy (`new->next=NULL; new->prev=new;`)
   and then, only when `allow_array` is set, re-copies the whole chain.

### 2.4 `len` — semantics per type (this is *not* uniform)

| type | `len` means |
|---|---|
| `UCL_STRING` | number of bytes in `value.sv` (excluding the NUL) |
| `UCL_ARRAY` | element counter maintained by the array API — **not** the same as `ucl_array_size()`, which returns the kvec's `n`. They agree in normal use but diverge after `ucl_array_merge` (see §3.6 bug) |
| `UCL_OBJECT` | number of entries. `+1` per **new key** inserted via the object API; `-1` per `ucl_object_delete_key*`/`ucl_object_pop_key*`. The parser additionally does `+1` per *implicit-array element* appended to an existing key (`ucl_parser_append_elt` does `parser->stack->obj->len++`) — so a parsed object's `len` counts *values*, while an object built purely through `ucl_object_insert_key` counts *keys*. `ucl_object_copy` rebuilds with `insert_key` and therefore a copy of a parsed object with duplicate keys has a **smaller** `len` than the original |
| `UCL_INT`/`UCL_FLOAT`/`UCL_TIME`/`UCL_BOOLEAN`/`UCL_NULL`/`UCL_USERDATA` | normally 0, **except**: after `ucl_copy_value_trash` on a non-string, `len` is overwritten with the length of the JSON rendering; and after `ucl_object_fromstring_common(..., UCL_STRING_PARSE*)` converts a string to a number, `len` retains the original *string* length |

`len` is load-bearing in `ucl_object_compare` (§6) and in the emitter, so the
divergences above are observable, not cosmetic.

### 2.5 `ref` — reference count

Starts at 1 for every constructor. `ucl_object_ref` increments (atomically if
`HAVE_ATOMIC_BUILTINS`), `ucl_object_unref` decrements and frees at 0. See §7.4
for what is observable in Go.

### 2.6 `flags` (uint16)

Low 12 bits are the flag bits; the **high 4 bits (12..15) are the priority**.

| Flag | Value | Set by | Read by |
|---|---|---|---|
| `UCL_OBJECT_ALLOCATED_KEY` | `1<<0` | `ucl_copy_key_trash` when it allocates a key copy | Cleared by `ucl_object_insert_key_common` when it discards a stale key trash. Otherwise purely bookkeeping; **no behaviour depends on it** |
| `UCL_OBJECT_ALLOCATED_VALUE` | `1<<1` | `ucl_copy_value_trash` | Purely bookkeeping; no behaviour depends on it |
| `UCL_OBJECT_NEED_KEY_ESCAPE` | `1<<2` | `ucl_object_insert_key_common` if any key byte is in the `UCL_CHARACTER_UCL_UNSAFE` class (§5.5); also by the parser (`ucl_parser.c:1588`) | The emitter (`ucl_emitter.c:118,133`) — quotes/escapes the key |
| `UCL_OBJECT_EPHEMERAL` | `1<<3` | Never set inside libucl; set by embedders on stack-allocated objects | `ucl_object_ref` returns a **deep copy instead of bumping the refcount**; `ucl_object_dtor_free` does not free the struct; `ucl_object_copy_internal` clears the flag on the copy |
| `UCL_OBJECT_MULTILINE` | `1<<4` | Parser for heredoc-style strings (`ucl_parser.c:1937`), `.load` macro with `multiline` (`ucl_util.c:1886`) | `ucl_maybe_long_string` in the emitter: a string is emitted multiline if `(len > 80 \|\| MULTILINE) && it contains '\n'` |
| `UCL_OBJECT_MULTIVALUE` | `1<<5` | `ucl_parser_append_elt` sets it on the *head* object of an implicit array; when `UCL_PARSER_NO_IMPLICIT_ARRAYS` is on, it is set on the synthesised `UCL_ARRAY`. `.include` with `array` sets it (`ucl_util.c:1270`) | Only `ucl_parser_append_elt` itself, to decide whether an existing explicit array may be appended to rather than re-wrapped |
| `UCL_OBJECT_INHERITED` | `1<<6` | `.inherit` macro (`ucl_util.c:1967`); parser marks inherited comments (`ucl_parser.c:125`) | Duplicate resolution (`ucl_parser.c:1296`) forces `prinew = priold + 1`, i.e. a new value always beats an inherited one; the emitter skips inherited comments |
| `UCL_OBJECT_BINARY` | `1<<7` | msgpack `bin` values, csexp values, embedder code | `ucl_copy_value_trash` copies exactly `len` bytes with **no NUL terminator**; `ucl_object_tostring_safe` and `ucl_object_tostring_forced` refuse to convert (return `false`/`NULL`); the emitter emits msgpack `bin` |
| `UCL_OBJECT_SQUOTED` | `1<<8` | Parser for `'single quoted'` strings (`ucl_parser.c:1827`) | The config/YAML emitter re-emits with single quotes |

Bits 9, 10, 11 are unused. Bits 12–15 are priority.

### 2.7 Priority packing (exact)

```c
#define PRIOBITS 4
unsigned ucl_object_get_priority(const ucl_object_t *obj) {
    if (obj == NULL) return 0;
    return (obj->flags >> ((sizeof(obj->flags) * 8) - PRIOBITS));   /* >> 12 */
}
void ucl_object_set_priority(ucl_object_t *obj, unsigned priority) {
    if (obj != NULL) {
        priority &= (0x1 << PRIOBITS) - 1;   /* & 0xF  */
        priority <<= 12;
        priority |= obj->flags & 0x0FFF;
        obj->flags = priority;               /* truncated to uint16 */
    }
}
```

So: **priority = `flags >> 12`**, and setting a priority masks the input to
0..15 and rewrites the top nibble, preserving the low 12 flag bits.
`ucl_object_get_priority(NULL) == 0`. `ucl_object_set_priority(NULL, x)` is a
no-op. Priority is only *used* by the parser's duplicate-key resolution
(higher priority wins; equal priority forms an implicit array; lower is dropped).

### 2.8 `trash_stack[2]`

Indices: `UCL_TRASH_KEY = 0`, `UCL_TRASH_VALUE = 1`. These hold library-owned
heap copies of the key and the value so that the public "give me a C string"
API can hand out NUL-terminated memory even when the object was parsed
zero-copy. They are pure memory management **except** for three observable
effects, which the Go port must reproduce:

**`ucl_copy_key_trash(obj)`** — `ucl_object_key()` is exactly this:

```c
if (obj == NULL) return NULL;
if (obj->trash_stack[UCL_TRASH_KEY] == NULL && obj->key != NULL) {
    trash = malloc(keylen+1); memcpy(trash, key, keylen); trash[keylen]='\0';
    obj->key = trash;                       /* mutates a "const" object */
    obj->flags |= UCL_OBJECT_ALLOCATED_KEY;
}
return obj->trash_stack[UCL_TRASH_KEY];
```

Observable: if `obj->key == NULL`, the return is `NULL` (not `""`), and no trash
is created. If `keylen == 0` but `key != NULL`, you get `""`.

**`ucl_copy_value_trash(obj)`** — backs `ucl_object_tostring*`:

```c
if (obj == NULL) return NULL;
if (obj->trash_stack[UCL_TRASH_VALUE] == NULL) {
    if (obj->type == UCL_STRING) {
        if (flags & UCL_OBJECT_BINARY) { copy exactly len bytes, no NUL }
        else                            { copy len bytes + NUL }
        obj->value.sv = trash;
    } else {
        obj->trash_stack[UCL_TRASH_VALUE] = ucl_object_emit_single_json(obj);
        obj->len = strlen(trash);        /* <-- OVERWRITES len */
    }
    obj->flags |= UCL_OBJECT_ALLOCATED_VALUE;
}
return obj->trash_stack[UCL_TRASH_VALUE];
```

Two observable effects: (a) `len` of a non-string object is destroyed and
replaced with the rendering length; (b) **the trash is cached**, so if the value
trash already exists (e.g. because the object was created by
`ucl_object_fromstring_common` with `UCL_STRING_PARSE` and then reclassified as
`UCL_INT`), `tostring_forced` returns the *stale original string* rather than a
fresh JSON rendering of the current value.

`ucl_object_emit_single_json` renders:
`UCL_OBJECT`→`"object"`, `UCL_ARRAY`→`"array"`, `UCL_INT`→`%jd`,
`UCL_FLOAT`/`UCL_TIME`→ the double formatter below, `UCL_NULL`→`"null"`,
`UCL_BOOLEAN`→`"true"`/`"false"`, `UCL_STRING`→ the raw `len` bytes,
`UCL_USERDATA`→`"userdata"`.

Double formatter (`ucl_utstring_append_double`, needed for exact string equality):

```c
const double delta = 0.0000001;
if (val == (double)(int)val)                 printf("%.1lf", val);
else if (fabs(val - (double)(int)val) < delta) printf("%.*lg", DBL_DIG /*15*/, val);
else                                          printf("%lf", val);   /* %f -> 6 dp */
```

Note the `(int)` casts, not `(int64_t)`: values outside `int` range take the
third branch. `1.0` → `"1.0"`, `0.5` → `"0.500000"`, `1e-9` → `"1.0000000000000001e-09"`
(second branch, since it is within delta of 0).

Dead code note: `ucl_object_dtor_free` writes `UCL_FREE(obj->hh.keylen, ...)`
even though `ucl_object_t` has no `hh` member — this compiles only because
`#define UCL_FREE(size, ptr) free(ptr)` discards its first argument. Ignore it.

---

## 3. The hash table (`ucl_hash.c`) — ordering is the whole point

### 3.1 Structure

```c
struct ucl_hash_elt   { const ucl_object_t *obj; struct ucl_hash_elt *prev, *next; };
struct ucl_hash_struct{ void *hash; struct ucl_hash_elt *head; bool caseless; };
```

Two parallel structures:

1. a **khash open-addressing map** whose *key* is a `const ucl_object_t*` but
   whose hash/equality functions look **only at `obj->key[0..keylen)`** — so it
   behaves as a `map[string]*elt`;
2. a **doubly linked list** of `ucl_hash_elt` nodes rooted at `head`, in
   **insertion order**. Iteration and emission walk this list, never the map.

The map value is the `*ucl_hash_elt`, so map→list is O(1).

Hash function is MUM-hash seeded from `time(NULL)` once per process (so
*bucket* order is nondeterministic — which is exactly why the ordered list
exists). Nothing observable depends on the hash function; a Go `map[string]`
is a drop-in replacement.

### 3.2 The five operations, precisely

**`ucl_hash_create(ignore_case)`** — empty map, `head = NULL`, `caseless = ignore_case`.

**`ucl_hash_insert(h, obj, key, keylen)`**
```c
k = kh_put(h, obj, &ret);
if (ret > 0) {            /* key was NOT present */
    elt = alloc(); kh_value(h,k) = elt; DL_APPEND(h->head, elt); elt->obj = obj;
}
else if (ret < 0) return false;   /* ENOMEM */
return true;
```
**If the key is already present (`ret == 0`) this is a silent no-op** — the
stored object is *not* replaced and the list is *not* touched, yet it still
returns `true`. New keys are always appended at the **tail** of the ordered
list. Insertion order is therefore the emission order.

**`ucl_hash_search(h, key, keylen)`** — map lookup by key bytes; returns
`elt->obj`, i.e. the *head* of any implicit-array chain. `NULL` if absent or if
`h == NULL`.

**`ucl_hash_replace(h, old, new)`**
```c
k = kh_put(h, old, &ret);
if (ret == 0) {                       /* old was present */
    elt = kh_value(h,k);
    kh_del(h, k);
    k = kh_put(h, new, &ret);
    nelt = alloc(); nelt->obj = new; kh_value(h,k) = nelt;
    DL_REPLACE_ELEM(h->head, elt, nelt);   /* same position in the list */
    free(elt);
}
```
**The replacement keeps the old element's position in the ordered list.** This
is critical for emission fidelity: `key = 1; other = 2; key = 3;` (with
`UCL_DUPLICATE_REWRITE`, or a higher-priority chunk) emits `key` **first**, not
last. `DL_REPLACE_ELEM` handles head, middle, and tail positions and fixes
`head->prev` when replacing the tail.

> C bug to be aware of but *not* to reproduce: if `old` is absent, `kh_put`
> has already created a bucket keyed by `old` whose value is uninitialised
> garbage, and the function then returns without initialising it. In Go, treat
> "old not present" as a plain no-op.

**`ucl_hash_delete(h, obj)`** — looks the entry up **by `obj`'s key bytes**
(not by pointer identity!), then `DL_DELETE` from the ordered list and `kh_del`
from the map. Other elements keep their relative order. Passing a *different*
object that happens to have the same key deletes the stored one.

**`ucl_hash_reserve(h, sz)`** — capacity hint only; resizes to `sz*2` when
`sz > kh_size`. No observable behaviour. (Note the C reads `kh_size` through
the non-caseless type regardless of `caseless` — harmless, the header layout is
identical.)

**`ucl_hash_destroy(h, free_func)`** — if `free_func != NULL`, for every map
entry it walks the object's **`next` chain** and calls `free_func` on each,
then frees the list nodes and the map. Go: nothing.

**`ucl_hash_iterate2(h, &iter, *ep)`** — heap-allocates a cursor on the first
call (`iter == NULL`) positioned at `h->head`, returns `cur->obj` and advances.
When exhausted it frees the cursor, sets `*iter = NULL`, and returns `NULL`.
Sets `*ep = EINVAL` if `h == NULL`, `ENOMEM` on allocation failure, `0` otherwise.
`ucl_hash_iter_has_next(h, iter)` is `it->cur != NULL` (it dereferences `iter`
without a NULL check). `ucl_hash_iterate_free(iter)` frees an abandoned cursor.

### 3.3 The case-insensitive variant

Selected by `ucl_hash_create(true)`. Only the parser ever creates one, when
`UCL_PARSER_KEY_LOWERCASE` is set (`ucl_parser.c:690,1273`, `ucl_util.c:1228,1318`).
The **object API always creates case-sensitive hashes** —
`ucl_object_insert_key_common` does `ucl_hash_create(false)` unconditionally, and
`ucl_hash_insert_object(..., ignore_case)`'s flag is only consulted when the
hash pointer is `NULL` and a new hash must be created.

Differences, and *only* these:

* Hashing folds each byte through `lc_map[]`, which maps `0x41..0x5A` (`'A'..'Z'`)
  to `0x61..0x7A` and is the identity for every other byte — **ASCII only, no
  locale, no UTF-8 case folding, bytes ≥ 0x80 untouched**.
* Equality compares `keylen` first, then folded bytes.
* **The stored key keeps its original spelling.** Only lookup and hashing fold.
  Therefore a document with `Foo = 1; fOO = 2;` parsed with `KEY_LOWERCASE`
  emits the key as `Foo` (the first-inserted spelling) with both values merged
  under it. Ordering rules are otherwise identical.

### 3.4 `ucl_hash_sort(h, flags)` — `ucl_object_sort_keys` under the hood

Sorts the **ordered list only** (a stable bottom-up mergesort — `utlist`'s
`DL_SORT`, Simon Tatham's algorithm, which takes the left run when
`cmp(p,q) <= 0`, hence stable). The map is untouched, so lookups are unaffected;
only iteration and emission order change.

Comparator, case-sensitive (`UCL_SORT_KEYS_DEFAULT`):

```c
if (a->keylen == b->keylen) return memcmp(a->key, b->key, a->keylen);
return ((int)a->keylen) - b->keylen;
```

**Shorter keys sort first**, and only keys of equal length are compared
lexicographically. This is *not* plain lexicographic order: `"z"` sorts before
`"aa"`.

Comparator with `UCL_SORT_KEYS_ICASE`: same length-first rule, then `ucl_lc_cmp`,
which has two quirks that must be reproduced byte-for-byte to match C output:

```c
/* 4 bytes at a time */
cmp1.c[0..3] = lc_map[s[i..i+3]];  cmp2.c[0..3] = lc_map[d[i..i+3]];
if (cmp1.n != cmp2.n) return cmp1.n - cmp2.n;   /* uint32 reinterpretation! */
...
/* leftover tail, one byte at a time */
if (lc_map[s[i]] != lc_map[d[i]]) return s[i] - d[i];   /* RAW bytes, not folded */
```

* `cmp1.n`/`cmp2.n` are the four folded bytes reinterpreted as a native `uint32`.
  On little-endian hardware that means the **last** byte of the group is the most
  significant, so the sign of the difference is decided by byte `i+3` first, then
  `i+2`, `i+1`, `i` — *not* lexicographic. The `uint32 - uint32` result is then
  assigned to `int`, so it can wrap.
* The leftover-tail branch detects inequality on the **folded** bytes but returns
  the difference of the **raw** bytes.

In Go, model this as: compare 4-byte blocks by `binary.LittleEndian.Uint32` of
the folded bytes (difference as `int32(uint32a - uint32b)`), then the tail as
`int(s[i]) - int(d[i])` when folded bytes differ.

`UCL_SORT_KEYS_RECURSIVE` then walks the (now sorted) list and recurses into
every element whose type is `UCL_OBJECT`, via `elt->obj->value.ov`. It does
**not** recurse into arrays, and does not follow `next` chains.

`ucl_object_sort_keys(obj, how)` is `if (obj && obj->type == UCL_OBJECT)
ucl_hash_sort(obj->value.ov, how)` — a no-op for anything else, and it will
crash on an `UCL_OBJECT` with a `NULL` hash only if the hash functions are
reached (they all guard `hashlin == NULL`, so it is safe).

### 3.5 Ordering summary (the contract the emitter depends on)

1. New key → appended at the tail.
2. Duplicate key merged into an implicit array (`DL_CONCAT` on the value's
   `next` chain) → the key's list position is **unchanged**; the extra values
   ride along on the head's chain.
3. Duplicate key replaced (`ucl_hash_replace`) → the key keeps its **original**
   list position, with the new value.
4. Deleted key → removed from the list; nothing else moves. Re-inserting that
   key later puts it at the **tail**.
5. `ucl_object_sort_keys` reorders the list stably by (keylen, key bytes).

A Go implementation therefore needs: `map[string]*entry` plus an ordered
container of entries supporting O(1) append, in-place replace, and delete.
A slice of `*entry` with tombstones, or a `container/list`, both work; a slice
with an index map and deletion by `append(s[:i], s[i+1:]...)` is simplest and
matches the semantics exactly.

### 3.6 Arrays are a kvec, not a list

`value.av` is `kvec_t(ucl_object_t*)` = `{n, m, a}` (length, capacity, buffer).
Relevant macro semantics:

* `kv_push_safe` — append.
* `kv_prepend_safe` — `memmove` everything up one slot, store at 0 (O(n)).
* `kv_concat_safe(v1, v0)` — `memcpy(v1.a + v1.n, v0.a, n0)`, then
  `v1.n = v0.n + v1.n`.
* `kv_del(v, i)` — `memmove` the tail down one slot, `n--` (order preserving,
  no-op if `i >= n`).

So Go arrays are `[]*Object` with ordinary slice operations.

---

## 4. The object API, function by function

Throughout: "returns false/NULL on X" is exact; libucl is inconsistent about
NULL-checking and the inconsistencies are observable through the test suite.

### 4.1 Construction

| C | Semantics |
|---|---|
| `ucl_object_new()` | `ucl_object_typed_new(UCL_NULL)` |
| `ucl_object_typed_new(t)` | `ucl_object_new_full(t, 0)` |
| `ucl_object_new_full(t, prio)` | Zeroed object; `ref=1`; `type = (t <= UCL_NULL ? t : UCL_NULL)` (out-of-range types silently become `UCL_NULL`); `next=NULL`, `prev=self`; priority set per §2.7. For `UCL_ARRAY`, allocates the kvec and reserves capacity 8. For `UCL_USERDATA`, delegates to `ucl_object_new_userdata(NULL,NULL,NULL)` then sets the priority. **Does not** allocate the hash for `UCL_OBJECT` — that happens lazily on first insert |
| `ucl_object_new_userdata(dtor, emitter, ptr)` | `type=UCL_USERDATA`, `ref=1`, `prev=self`, `value.ud=ptr`, stores `dtor`/`emitter` |
| `ucl_object_fromint(iv)` | `UCL_INT`, `value.iv = iv` |
| `ucl_object_fromdouble(dv)` | `UCL_FLOAT`, `value.dv = dv` |
| `ucl_object_frombool(bv)` | `UCL_BOOLEAN`, `value.iv = bv ? 1 : 0` |
| `ucl_object_fromstring(s)` | `fromstring_common(s, 0, UCL_STRING_RAW)` — length via `strlen`, no escaping, no parsing |
| `ucl_object_fromlstring(s, len)` | `fromstring_common(s, len, UCL_STRING_RAW)` |
| `ucl_object_fromstring_common(s, len, flags)` | §5.1 |

There is **no** `ucl_object_fromtime`/`fromnull` constructor — use
`ucl_object_typed_new`.

Trivial in Go: all of these.

### 4.2 Type and identity

* `ucl_object_type(obj)` → `UCL_NULL` if `obj == NULL`, else `obj->type`.
* `ucl_object_type_to_string(t)` → `"object"`, `"array"`, `"integer"`,
  `"number"` (for **both** `UCL_FLOAT` and `UCL_TIME`), `"string"`, `"boolean"`,
  `"userdata"`, `"null"`; `"unknown"` for anything else.
* `ucl_object_string_to_type(in, *res)` → case-insensitive (`strcasecmp`) match
  against `object, array, integer, number, string, boolean, null, userdata`;
  `"number"` maps to **`UCL_FLOAT`** (there is no way to name `UCL_TIME`).
  Returns `false` and leaves `*res` untouched on no match. Dereferences `input`
  without a NULL check.
* `ucl_object_key(obj)` → `ucl_copy_key_trash(obj)`; `NULL` if `obj == NULL` or
  the object has no key. Has the side effect described in §2.8.
* `ucl_object_keyl(obj, *len)` → returns `obj->key` and writes `obj->keylen`;
  returns `NULL` without writing if either argument is `NULL`. Zero-copy, no
  trash side effect.

### 4.3 Object (map) insertion — `ucl_object_insert_key_common`

All four public entry points funnel here:

```c
ucl_object_insert_key       (top, elt, key, keylen, copy_key)  -> common(..., merge=false, replace=false)
ucl_object_insert_key_merged(top, elt, key, keylen, copy_key)  -> common(..., merge=true,  replace=false)
ucl_object_replace_key      (top, elt, key, keylen, copy_key)  -> common(..., merge=false, replace=true)
```

Algorithm, in order:

1. `if (elt == NULL || key == NULL) return false;`
2. `if (top == NULL) return false;`
3. If `top->type != UCL_OBJECT`: **`UCL_NULL` is converted in place to
   `UCL_OBJECT`**; any other type returns `false`.
4. If `top->value.ov == NULL`, create a case-**sensitive** hash.
5. `if (keylen == 0) keylen = strlen(key);` — so a genuinely empty key cannot
   be inserted through this API.
6. Scan the key bytes; if any is in `UCL_CHARACTER_UCL_UNSAFE` (§5.5), set
   `elt->flags |= UCL_OBJECT_NEED_KEY_ESCAPE` and stop scanning.
7. "Workaround": if `elt` already owns a key trash and `key` is a *different*
   pointer, free the old trash and clear `UCL_OBJECT_ALLOCATED_KEY`.
   (Go: just overwrite the key.)
8. `elt->key = key; elt->keylen = keylen;` then, if `copy_key`,
   `ucl_copy_key_trash(elt)` (which makes the object own a NUL-terminated copy).
9. `found = ucl_hash_search(top->value.ov, elt->key, elt->keylen)`.
10. **If `found == NULL`**: `ucl_hash_insert` the element (appended at the list
    tail) and `top->len++`. Returns `true` *regardless of the replace flag*.
11. **If `found != NULL` and `replace`**: `ucl_hash_replace(ov, found, elt)`
    (position preserved) then `ucl_object_unref(found)`. `top->len` is **not**
    changed.
12. **If `found != NULL` and `merge`**, four cases:
    * `found` is not an object and `elt` **is** an object → insert `found` into
      `elt` under `found`'s own key, delete `found` from the hash, then insert
      `elt`. **Note this moves the key to the tail of the ordered list** (delete
      + insert, not replace) — the one merge path that changes emission order.
    * `found` **is** an object and `elt` is not → recursively insert `elt` into
      `found` under `elt`'s key (non-merge, non-replace).
    * both are objects → iterate `elt`'s entries and recursively
      `insert_key_common(found, ref(cur), cur->key, cur->keylen, copy_key,
      merge=true, replace=false)`, then `ucl_object_unref(elt)`.
    * neither is an object → `DL_CONCAT(found, elt)`: build an implicit array.
13. **Otherwise (plain insert on an existing key)**: `DL_CONCAT(found, elt)` —
    append `elt`'s whole chain to `found`'s chain. `top->len` is **not**
    incremented (unlike the parser's path, §2.4).

The function always returns `true` once it is past the guards — including every
merge and duplicate path.

Ownership: `insert_key` **takes ownership** of `elt` (does not ref it); the
caller must not unref unless it took its own reference first.

### 4.4 Object deletion and removal

* `ucl_object_delete_keyl(top, key, keylen)` — `false` if `top`/`key` is `NULL`
  or the key is absent. Otherwise `ucl_hash_delete`, `ucl_object_unref(found)`
  (which frees the entire implicit chain), `top->len--`, `true`.
  **`keylen` is used literally — there is no `strlen` fallback for 0**, unlike
  `insert_key`. Passing 0 searches for a zero-length key.
* `ucl_object_delete_key(top, key)` — `delete_keyl(top, key, strlen(key))`.
  Crashes on `key == NULL` (the `strlen` happens before the guard).
* `ucl_object_pop_keyl(top, key, keylen)` — same lookup, removes from the hash,
  `top->len--`, and **returns the object without unref'ing** (caller owns it,
  including its whole `next` chain). `NULL` if not found.
* `ucl_object_pop_key(top, key)` — `pop_keyl(top, key, strlen(key))`.

Delete/pop only ever remove the **whole** key including all implicit-array
values; there is no API to remove one element of an implicit array.

### 4.5 Lookup

* `ucl_object_lookup_len(obj, key, klen)` — `NULL` if `obj == NULL`,
  `obj->type != UCL_OBJECT`, or `key == NULL`. Otherwise a hash lookup on
  exactly `klen` bytes. Returns the chain head. **No `strlen` fallback for
  `klen == 0`.**
* `ucl_object_lookup(obj, key)` — `NULL` if `key == NULL`, else
  `lookup_len(obj, key, strlen(key))`.
* `ucl_object_lookup_any(obj, key, ...)` — variadic, NULL-terminated list of
  alternative keys; returns the first hit. `NULL` if `obj` or `key` is `NULL`.
  In Go: `LookupAny(obj, keys ...string)`.
* `ucl_object_lookup_path(top, path)` = `lookup_path_char(top, path, '.')`.
* `ucl_object_lookup_path_char(top, path, sep)`:

```
if (path == NULL || top == NULL) return NULL;
o = NULL; skip leading `sep` characters;
for each `sep`-delimited (or end-of-string-delimited) segment [c, p):
    if (p > c) {                       /* empty segments are skipped */
        if (top->type == UCL_ARRAY) {
            index = strtoul(c, &err_str, 10);
            if (err_str && *err_str != sep && *err_str != '\0') return NULL;
            o = ucl_array_find_index(top, index);
        } else {
            o = ucl_object_lookup_len(top, c, p - c);
        }
        if (o == NULL) return NULL;
        top = o;
    }
}
return o;    /* NULL for an empty path or a path of only separators */
```

  Note: numeric indexing applies only when the *current* node is an array;
  for any other type it falls through to `lookup_len`, which yields `NULL`
  unless the node is an object. `strtoul` accepts leading whitespace, `+`/`-`
  and wraps — reproduce with care, or use `strconv.ParseUint` after replicating
  the `err_str` check (the char that stopped the conversion must be `sep` or the
  terminator).

### 4.6 Merging — `ucl_object_merge(top, elt, copy)`

`false` if either argument is `NULL`. Then:

* `top` is `UCL_ARRAY`:
  * `elt` is `UCL_ARRAY` → `ucl_array_merge(top, elt, copy)` (§4.7).
  * otherwise → append (a copy of, or a new ref to) `elt` to `top`; `true`.
* `top` is `UCL_OBJECT`:
  * `elt` is `UCL_OBJECT` → for each entry `cur` of `elt`'s hash **(via
    `ucl_hash_iterate`, i.e. chain heads only, in insertion order)**:
    `cp = copy ? ucl_object_copy(cur) : ucl_object_ref(cur)`; look `cp->key` up
    in `top`:
    * not found → `ucl_hash_insert`, `top->len++` (appended at the tail);
    * found and the found value is `UCL_OBJECT` or `UCL_ARRAY` → recurse
      `ucl_object_merge(found, cp, copy)`; on failure **return `false`
      immediately, leaking the rest**; on success `ucl_object_unref(cp)`;
    * found and it is a scalar → `ucl_hash_replace(top, found, cp)` (position
      preserved) + `unref(found)`. `top->len` unchanged.
  * `elt` is not an object → the *same* found/insert/replace logic is applied
    once using `elt`'s own key (which may be `NULL`/0 length — then the lookup
    is on a zero-length key).
* `top` is anything else → `false` ("Cannot merge trivial objects").

`ucl_object_merge` is **not** the same as `insert_key_merged`: merge never
builds implicit arrays, it replaces scalars.

### 4.7 Arrays

| C | Semantics |
|---|---|
| `ucl_array_append(top, elt)` | `false` if `top->type != UCL_ARRAY` (dereferences `top` **before** the NULL check — `top == NULL` crashes) or `elt == NULL`. Lazily allocates the kvec. Appends, `top->len++`, `true`. Takes ownership of `elt` |
| `ucl_array_prepend(top, elt)` | Same guards; O(n) shift; `top->len++` |
| `ucl_array_merge(top, elt, copy)` | `false` unless both are non-NULL `UCL_ARRAY`. `cp = copy ? copy(elt) : ref(elt)`; then `kv_concat(v_top, v_cp)`. **`cp` is never unref'd (C leak) and its element pointers are now shared with `top`.** `len` accounting is buggy — see below |
| `ucl_array_delete(top, elt)` | `NULL` if not an array or the kvec is `NULL`. Linear scan for **pointer identity**, `kv_del`, `top->len--`, returns `elt`; caller owns it. `NULL` if absent |
| `ucl_array_head(top)` | `NULL` if `top`/kvec is `NULL` or not an array; else element 0 or `NULL` if empty |
| `ucl_array_tail(top)` | Same, last element. (Note: dereferences the kvec pointer to build `vec` before checking `top == NULL`) |
| `ucl_array_pop_last(top)` | Removes and returns the last element, `top->len--`; `NULL` if empty/not an array. Caller owns the result |
| `ucl_array_pop_first(top)` | Same for index 0 (O(n)) |
| `ucl_array_size(top)` | `0` if `top == NULL` or not an array; else **`kvec.n`**, *not* `top->len` |
| `ucl_array_find_index(top, i)` | `NULL` if not an array or `i >= n`; else the element (which may itself be `NULL`) |
| `ucl_array_index_of(top, elt)` | `(unsigned)-1` (i.e. `0xFFFFFFFF`) if not an array, kvec `NULL`, or not found; else the index. Pointer identity |
| `ucl_array_replace_index(top, elt, i)` | `NULL` if not an array or `i >= n`; else stores `elt` at `i` and returns the previous occupant, **not unref'd** — caller owns it. `top->len` unchanged |
| `ucl_object_array_sort(ar, cmp)` | `qsort` over the kvec with the given `int (*)(const ucl_object_t**, const ucl_object_t**)`. Guards are checked *after* `UCL_ARRAY_GET`, so `ar == NULL` crashes. **`qsort` is unstable** — see §6.2 |
| `ucl_object_reserve(obj, n)` | For arrays: allocates the kvec if absent, grows capacity to `n` if `m < n`. For objects: `ucl_hash_reserve`. Returns `true` (or `false` on ENOMEM). No-op for other types (still returns `true`). Dereferences `obj` without a NULL check |
| `ucl_elt_append(head, elt)` | Pure `next`/`prev` chain append (no container involved). If `head == NULL`, initialises `elt` as its own head and returns `elt`; otherwise appends `elt` at the tail and returns `head` |

**`ucl_array_merge` length bug — reproduce it deliberately.** The C is:

```c
kv_concat_safe(ucl_object_t *, *v1, *v2, e0);      /* v1->n becomes n1 + n2 */
for (i = v2->n; i < v1->n; i++) {                  /* starts at n2, not n1!  */
    obj = &kv_A(*v1, i);
    if (*obj == NULL) continue;
    top->len++;
}
```

The loop should have started at the *old* `v1->n`. As written it iterates
`n1` times over the window `[n2, n1+n2)` and increments `top->len` once per
non-NULL slot in that window. So after a merge, `top->len` and
`ucl_array_size(top)` disagree unless `n1 == n2`. Since `ucl_object_compare`
uses `len` for arrays (§6) this **is** observable. Port it verbatim and cover it
with a test.

### 4.8 Iteration

Two APIs. Both iterate an object's entries in **hash-list (insertion) order**.

**Inline iterator** — `ucl_object_iterate(obj, &iter, expand_values)`, a macro
for `ucl_object_iterate_with_error(obj, &iter, expand_values, NULL)`:

```c
if (obj == NULL || iter == NULL) return NULL;
if (expand_values) {
    case UCL_OBJECT: return ucl_hash_iterate2(obj->value.ov, iter, ep);
    case UCL_ARRAY:  idx = (uintptr_t)*iter;
                     skip NULL slots; return element at idx; *iter = idx+1;
                     (returns NULL once idx reaches n; the kvec may be NULL)
    default: fall through to the linear walk
}
/* linear walk of the implicit-array chain */
elt = *iter;
if (elt == NULL) elt = obj;          /* first call: yield obj itself */
else if (elt == obj) return NULL;    /* we wrapped: done */
*iter = elt->next ? elt->next : obj; /* park on obj to signal completion */
return elt;
```

Consequences: with `expand_values == false` you iterate the object *and its
duplicates* (`obj, obj->next, ...`), starting with `obj` itself. With
`expand_values == true` you iterate the container's children (chain **heads**
only for objects — implicit-array siblings are *not* visited). The iterator
value for arrays is an integer smuggled through a `void*`; for objects it is a
heap cursor.

`ucl_object_iterate_end(obj, &iter)` frees the heap cursor if `obj` is an
`UCL_OBJECT` and sets `*iter = NULL`. Must be called when abandoning an
object iteration early (Go: unnecessary, but keep the function as a no-op so
ported tests compile).

`ep` (`ucl_object_iterate_with_error`) receives `EINVAL` when the object's hash
is `NULL`, `ENOMEM` on cursor allocation failure, `0` otherwise; it is left
untouched by the array and linear paths.

**Safe iterator** — heap object with a magic `"uite"` header:

```c
struct ucl_object_safe_iter { char magic[4]; uint32_t flags;
                              const ucl_object_t *impl_it;   /* implicit chain cursor */
                              ucl_object_iter_t   expl_it; };/* inner iterator     */
```

* `ucl_object_iterate_new(obj)` — `impl_it = obj`, `expl_it = NULL`,
  `flags = UCL_ITERATE_FLAG_UNDEFINED (0)`.
* `ucl_object_iterate_reset(it, obj)` — frees the inner cursor if the previous
  state was "inside object", then re-points at `obj`.
* `ucl_object_iterate_safe(it, expand)` = `iterate_full(it, expand ?
  UCL_ITERATE_BOTH : UCL_ITERATE_IMPLICIT)`.
* `ucl_object_iterate_full(it, type)`:
  * `impl_it == NULL` → `NULL` (exhausted).
  * `impl_it` is an object → `flags = INSIDE_OBJECT`; pull from the hash
    iterator. On exhaustion, if `type & UCL_ITERATE_IMPLICIT`, advance
    `impl_it = impl_it->next`, reset `expl_it`, and recurse. On an internal
    error (`ern != 0`) set `flags = EXCEPTION` and return `NULL`.
  * `impl_it` is an array → `flags = INSIDE_ARRAY`; same pattern with the array
    iterator.
  * otherwise (scalar) → `flags = IMPLICIT`; return `impl_it` and advance to
    `impl_it->next`. If `type & UCL_ITERATE_EXPLICIT` and the value just
    produced is itself an object or array, **discard it and recurse** (i.e.
    containers are flattened away rather than returned).
* `ucl_object_iter_chk_excpn(it)` → `flags == UCL_ITERATE_FLAG_EXCEPTION`.
  (Note the C passes `ucl_object_iter_t*` but casts it straight to the struct
  pointer — the double indirection in the prototype is a bug; callers pass the
  iterator itself.)
* `ucl_object_iterate_free(it)` frees the inner cursor (only when
  `flags == INSIDE_OBJECT`) and the iterator.

`enum ucl_safe_iter_flags` values: `UNDEFINED=0, INSIDE_ARRAY=1,
INSIDE_OBJECT=2, IMPLICIT=3, EXCEPTION=4`.

### 4.9 Refcounting and copying

* `ucl_object_ref(obj)` — `NULL` in → `NULL` out. If `UCL_OBJECT_EPHEMERAL`,
  returns a **deep copy** (`ucl_object_copy`) and does **not** touch the
  refcount; otherwise increments `ref` and returns the same pointer.
* `ucl_object_unref(obj)` — `NULL`-safe; decrement, and at zero
  `ucl_object_free_internal(obj, allow_rec=true, dtor=ucl_object_dtor_unref)`,
  which frees the whole `next` chain, all array elements (each with its own
  chain), and destroys the hash (unref'ing every stored chain).
* `ucl_object_free(obj)` — deprecated; frees unconditionally, ignoring
  refcounts.
* `ucl_object_copy(other)` = `ucl_object_copy_internal(other, allow_array=true)`.

`ucl_object_copy_internal(other, allow_array)`:

1. `memcpy` the struct (so `type`, `flags`, `len`, `keylen`, `key` pointer, and
   the scalar value all carry over).
2. Clear `UCL_OBJECT_EPHEMERAL` on the copy; set `ref = 1`; unlink
   (`next = NULL`, `prev = self`).
3. Key: **only if `other` owns a key trash *and* `other->key` points at that
   trash** is a fresh NUL-terminated copy made and `new->key` repointed. If the
   key was not owned, the copy **shares the original key pointer** (in Go: copy
   the string, which is strictly safer and behaviourally identical).
4. Value trash: duplicated with `strdup` — **so a `UCL_OBJECT_BINARY` string is
   truncated at the first NUL byte by a copy.** If the new type is `UCL_STRING`,
   `value.sv` is repointed at the duplicate. Note `len` is *not* recomputed, so
   a copy of a binary string keeps the original `len` while owning fewer bytes.
5. For `UCL_ARRAY`/`UCL_OBJECT`: zero the value union and set `len = 0`, then
   re-add every child (via `ucl_object_iterate(other, &it, true)`), deep-copying
   each. Array children are copied with `allow_array = false` (chains dropped —
   array elements never have chains); object children with `allow_array = true`
   (so implicit arrays are preserved) and re-inserted with
   `ucl_object_insert_key(new, cp, cp->key, cp->keylen, false)`.
   **Because `insert_key` only bumps `len` for new keys, `new->len` can be less
   than `other->len`** for parsed objects containing implicit arrays (§2.4).
6. Otherwise, if `allow_array` and `other->next != NULL`, copy the rest of the
   chain (each with `allow_array = false`) and `DL_APPEND` them.

`UCL_USERDATA` copies the `dtor`/`emitter`/pointer by value — the pointer is
**shared**, and both copies will run the dtor. (A pre-existing C hazard; in Go
just copy the interface value.)

### 4.10 Comments table (object-API adjacent)

`ucl_comments_*` store comments in an ordinary `UCL_OBJECT` whose **keys are the
raw bytes of the commented object's pointer**:

```c
ucl_comments_add (comments, obj, str) -> insert_key(comments, fromstring(str),
                                                    (const char*)&obj, sizeof(void*), true)
ucl_comments_find(comments, srch)     -> lookup_len(comments, (const char*)&srch, sizeof(void*))
ucl_comments_move(comments, from, to) -> ref+delete+insert under the new pointer key; false if absent
```

In Go this must become `map[*Object]*Object` (identity keyed) rather than a
string-keyed UCL object, or an object keyed by a stable synthetic id. The
observable behaviour is only "comment attached to object X"; nothing depends on
the key bytes themselves except that `ucl_comments_move` preserves nothing about
ordering (delete + insert → moves to the tail).

---

## 5. String → value parsing (shared by the object API and the parser)

### 5.1 `ucl_object_fromstring_common(str, len, flags)`

```
if (str == NULL) return NULL;
obj = ucl_object_new();                    /* type UCL_NULL initially */
if (len == 0) len = strlen(str);
if (flags & UCL_STRING_TRIM) trim leading and trailing UCL_CHARACTER_WHITESPACE_UNSAFE bytes
else                         [start, end) = whole string
obj->type = UCL_STRING;
if (flags & UCL_STRING_ESCAPE) dst = JSON-escaped copy (see below), obj->len = escaped length
else                           dst = plain copy,            obj->len = end - start
obj->value.sv = dst; obj->trash_stack[UCL_TRASH_VALUE] = dst;
if ((flags & UCL_STRING_PARSE) && dst != NULL) {         /* UCL_STRING_PARSE == 60 */
    if (flags & UCL_STRING_PARSE_BOOLEAN) {
        if (!ucl_maybe_parse_boolean(obj, dst, obj->len) && (flags & UCL_STRING_PARSE_NUMBER))
            ucl_maybe_parse_number(obj, dst, dst + obj->len, &pos,
                                   flags & UCL_STRING_PARSE_DOUBLE,
                                   flags & UCL_STRING_PARSE_BYTES,
                                   flags & UCL_STRING_PARSE_TIME);
    } else {
        ucl_maybe_parse_number(obj, dst, dst + obj->len, &pos, ...same...);
    }
}
return obj;
```

Critical consequences:

* **The object keeps its string trash even after being reclassified as a
  number.** `obj->len` still holds the *string* length and
  `ucl_object_tostring_forced` returns the *original text*, not a rendering of
  the number. Reproduce this: give the Go object a "cached string rendering"
  field populated at construction time in this path.
* `*pos` is discarded, so any prefix that `ucl_maybe_parse_number` accepts wins
  even if input remains. Since the buffer is NUL-terminated and
  `ucl_lex_is_atom_end` includes `#`, `,`, `;`, `]`, `}`, `\n`, `\r` and NUL,
  the string `"10;xyz"` becomes `UCL_INT 10` while `"10abc"` stays a string.
* Trim uses `UCL_CHARACTER_WHITESPACE_UNSAFE` = `{0x09 \t, 0x0a \n, 0x0b \v,
  0x0c \f, 0x0d \r, 0x20 space}`. The trailing scan stops at `start`, so an
  all-whitespace input trims to the empty string.
* `UCL_STRING_PARSE_BYTES` alone (64) does **not** satisfy
  `flags & UCL_STRING_PARSE`, so no parsing happens.

**JSON escaping (`UCL_STRING_ESCAPE`).** A byte is escaped if it is in
`UCL_CHARACTER_JSON_UNSAFE | UCL_CHARACTER_WHITESPACE_UNSAFE`, i.e. one of
`{0x00, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x20, '"', '\\'}`. Output:

| byte | output | added length |
|---|---|---|
| `\n` | `\n` | +1 |
| `\r` | `\r` | +1 |
| `\b` (0x08) | `\b` | +1 |
| `\t` | `\t` | +1 |
| `\f` (0x0c) | `\f` | +1 |
| `\\` | `\\` | +1 |
| `"` | `\"` | +1 |
| `\0` | `\u0000` | +5 |
| `\v` (0x0b) | `\u000B` (capital B) | +5 |
| space | unchanged | +0 |

Everything else is copied verbatim (no `\uXXXX` for other control characters —
libucl's escaper is deliberately narrow).

### 5.2 `ucl_maybe_parse_boolean(obj, start, len)` — the exact word list

Matching is **length-dispatched then `strncasecmp`**, so it is ASCII
case-insensitive and requires an exact whole-token match:

| len | accepted (any case) | value |
|---|---|---|
| 5 | `false` | false |
| 4 | `true` | true |
| 3 | `yes` | true |
| 3 | `off` | false |
| 2 | `no` | false |
| 2 | `on` | true |

Nothing else is a boolean — not `y`, `n`, `1`, `0`, `enabled`, `t`, `f`.
On a match, `obj->type = UCL_BOOLEAN` and `obj->value.iv = val`. Returns whether
it matched; `obj` may be `NULL` (test-only mode).

### 5.3 `ucl_maybe_parse_number(obj, start, end, *pos, allow_double, number_bytes, allow_time)`

Returns `0` on success (and sets `obj`'s type/value plus `*pos` = first
unconsumed byte), `EINVAL` for "not a number", `ERANGE` on overflow. On failure
`*pos` is set to `start` (or, in the final fallthrough, to `c`).

**Phase 1 — scan.** `p = c = start`; `is_neg=0`; flags `got_dot`, `got_exp`,
`need_double`, `is_time`, `valid_start`, `is_hex` all false.

```
if (*p == '-') { is_neg = 1; c++; p++; }       /* reads *p with no p<end check */
while (p < end) {
    if (is_hex && isxdigit(*p))            p++;                       /* NB: does not set valid_start */
    else if (isdigit(*p))                  { valid_start = true; p++; }
    else if (!is_hex && (*p=='x'||*p=='X')){ is_hex = true; allow_double = false; c = p+1; p++; }
    else if (allow_double) {
        if (p == c)                        return EINVAL;             /* empty digit run */
        else if (*p == '.')  { if (got_dot) return EINVAL; got_dot = need_double = true; p++; }
        else if (*p=='e'||*p=='E') {
            if (got_exp) return EINVAL;
            got_exp = need_double = true; p++;
            if (p >= end) return EINVAL;
            if (!isdigit(*p) && *p!='+' && *p!='-') return EINVAL;
            p++;
        }
        else break;                         /* end of the numeric part */
    }
    else if (!allow_double && *p=='.')     return EINVAL;
    else break;
}
if (!valid_start || p == c) return EINVAL;
```

Note: `valid_start` is only set by a *decimal* digit, so hex needs the leading
`0` of `0x...`; a bare `xFF` is rejected. Detecting `x` clears `allow_double`
and restarts the digit run after the `x`. There is no `0b`/`0o`/leading-zero-octal
handling — **libucl does not parse octal**; `0755` is decimal 755.
(The `strtoimax(numbuf, &endptr, 10)` call forces base 10, so a leading `0` is
never treated as octal.)

**Phase 2 — convert.** A 128-byte stack buffer is used; if `p - c + 1 >= 128`
the result is `EINVAL` (so numeric literals are limited to 127 significant
characters). The buffer receives an optional `-` followed by the bytes `[c, p)`.

```
errno = 0;
if (need_double)  dv = strtod (numbuf, &endptr);
else              lv = strtoimax(numbuf, &endptr, is_hex ? 16 : 10);
if (errno == ERANGE) return ERANGE;
endptr = (char*)c + (endptr - numbuf - is_neg);   /* map back into the source */
if (endptr >= end)                                        { p = end;    goto set_obj; }
if (endptr == NULL || ucl_lex_is_atom_end(*endptr) || *endptr == '\0') { p = endptr; goto set_obj; }
```

**Phase 3 — suffixes** (only reached when `endptr < end && endptr != start`).
`p = endptr`; switch on `*p`:

*Case `m M g G k K`:*

* If at least 2 bytes remain (`end - p >= 2`):
  * `p[1]` is `s`/`S` → **time**: promote to double (`dv = lv` if not already),
    `is_time = true`; if `p[0]` is `m`/`M` then `dv /= 1000.0` (**milliseconds**),
    else `dv *= ucl_lex_num_multiplier(p[0], /*is_bytes=*/false)` (so `ks` = ×1000
    seconds, `gs` = ×1 000 000 000 seconds). `p += 2`; if anything remains and it
    is not an atom end → `EINVAL`. Done.
  * else if `number_bytes` **or** `p[1]` is `b`/`B` → **bytes**: demote to
    integer (`lv = dv` if `need_double`), `lv *= ucl_lex_num_multiplier(p[0],
    /*is_bytes=*/true)`; `p += 2`; trailing check as above. Done.
    *(Quirk: when `number_bytes` is set, the second byte is consumed even if it
    is not `b`/`B` — e.g. `"10k;"` consumes `k;` and yields 10240.)*
  * else if `ucl_lex_is_atom_end(p[1])` → **plain multiplier**: `dv *=
    num_multiplier(*p, false)` if `need_double`, else `lv *= num_multiplier(*p,
    number_bytes)`; `p += 1`. Done.
  * else if `allow_time && end - p >= 3` and `tolower(p[0..2]) == "min"` →
    **minutes**: promote to double, `is_time = true`, `dv *= 60.0`, `p += 3`,
    trailing check. Done.
  * else → fall out of the switch → `EINVAL`.
* If exactly 1 byte remains: plain multiplier as above (`dv`/`lv` scaled,
  `p += 1`), trailing check, done.

*Case `s S`:* if `allow_time` and the suffix ends the atom
(`p == end-1 || ucl_lex_is_atom_end(p[1])`): promote to double, `is_time = true`,
no scaling (seconds are the base unit), `p += 1`. Done. Otherwise `EINVAL`.

*Case `h H d D w W y Y`:* if `allow_time` and the suffix ends the atom: promote
to double, `is_time = true`, `dv *= ucl_lex_time_multiplier(*p)`, `p += 1`. Done.

*Case `\t` or space:* skip `UCL_CHARACTER_WHITESPACE` bytes (`\t` and space
only); if the scan reaches `end` or an atom-end byte → done, otherwise `EINVAL`.

*Any other byte:* `EINVAL` (`*pos = c`).

**Phase 4 — `set_obj`.**

```c
if (allow_double && (need_double || is_time)) {
    obj->type = is_time ? UCL_TIME : UCL_FLOAT;
    obj->value.dv = dv;
} else {
    obj->type = UCL_INT;
    obj->value.iv = lv;
}
*pos = p; return 0;
```

So with `allow_double == false` (e.g. `UCL_STRING_PARSE_INT|PARSE_TIME` without
`PARSE_DOUBLE`), a time value collapses to the **unscaled integer**: `"10s"`
→ `UCL_INT 10`. And `is_hex` clears `allow_double`, so `0x10` is always
`UCL_INT`.

### 5.4 The multiplier tables (exact)

`ucl_lex_num_multiplier(c, is_bytes)` — `tolower(c)` matched against:

| char | normal (`is_bytes=false`) | bytes (`is_bytes=true`) |
|---|---|---|
| `k` | 1 000 | 1 024 |
| `m` | 1 000 000 | 1 048 576 |
| `g` | 1 000 000 000 | 1 073 741 824 |
| anything else | 1 | 1 |

`ucl_lex_time_multiplier(c)` — `tolower(c)` matched against:

| char | seconds |
|---|---|
| `m` | 60 (**unreachable**: `m` is always taken by the k/m/g case above; minutes only come from the literal `min` spelling, which hard-codes `*= 60.`) |
| `h` | 3 600 |
| `d` | 86 400 |
| `w` | 604 800 |
| `y` | 31 536 000 (365 days — **not** 365.25) |
| anything else | 1 |

Milliseconds are not in a table: `ms`/`MS` divides by `1000.0` inline.

Worked examples (all case-insensitive on the suffix letter):

| literal | `allow_double` | `number_bytes` | `allow_time` | result |
|---|---|---|---|---|
| `10` | any | any | any | `UCL_INT 10` |
| `-10` | any | any | any | `UCL_INT -10` |
| `0x1f` | any | any | any | `UCL_INT 31` |
| `10.5` | true | — | — | `UCL_FLOAT 10.5` |
| `1e3` | true | — | — | `UCL_FLOAT 1000` |
| `10k` | — | false | — | `UCL_INT 10000` |
| `10k` | — | true | — | `UCL_INT 10240` |
| `10kb` | — | false | — | `UCL_INT 10240` |
| `10mb` | — | false | — | `UCL_INT 10485760` |
| `10m` | — | false | — | `UCL_INT 10000000` (**not** 10 minutes) |
| `10min` | true | — | true | `UCL_TIME 600` |
| `10s` | true | — | true | `UCL_TIME 10` |
| `10ms` | true | — | true | `UCL_TIME 0.01` |
| `10h` | true | — | true | `UCL_TIME 36000` |
| `10d` | true | — | true | `UCL_TIME 864000` |
| `10w` | true | — | true | `UCL_TIME 6048000` |
| `10y` | true | — | true | `UCL_TIME 315360000` |
| `10s` | false | — | true | `UCL_INT 10` |
| `10abc` | any | any | any | `EINVAL` (stays a string) |

### 5.5 Character classes referenced above (decoded from `ucl_chartable.h`)

| class | members |
|---|---|
| `UCL_CHARACTER_VALUE_END` (atom end) | `0x00`, `\n` (0x0a), `\r` (0x0d), `#`, `,`, `;`, `]`, `}` |
| `UCL_CHARACTER_WHITESPACE` | `\t` (0x09), space (0x20) |
| `UCL_CHARACTER_WHITESPACE_UNSAFE` | `\t`, `\n`, `\v` (0x0b), `\f` (0x0c), `\r`, space |
| `UCL_CHARACTER_JSON_UNSAFE` | `0x00`, `\b` (0x08), `\t`, `\n`, `\f`, `\r`, `"`, `\` |
| `UCL_CHARACTER_UCL_UNSAFE` | `0x00`, `\b`, `\t`, `\n`, `\f`, `\r`, space, `"`, `+`, `:`, `=`, `[`, `\`, `{` |

(`UCL_CHARACTER_UCL_UNSAFE` is what decides `UCL_OBJECT_NEED_KEY_ESCAPE`;
note it includes `+`, `:`, `=`, `[`, `{` and space but **not** `}`, `]`, `,`,
`;`, `#`, or `'`.)

---

## 6. Conversion, comparison, sorting

### 6.1 Type conversions — exact allowed/denied matrix

All `_safe` variants return `false` and **leave `*target` untouched** when the
conversion is not allowed or either pointer is `NULL`. All unsafe variants
initialise a local to a zero value, call the safe variant, and return the local —
so a failed conversion yields the zero value, indistinguishable from a real zero.

| function | accepts | produces | rejects (returns false / zero) |
|---|---|---|---|
| `ucl_object_todouble_safe` | `UCL_INT` (→ `(double)iv`, may lose precision above 2^53), `UCL_FLOAT`, `UCL_TIME` | `double` | everything else, incl. `UCL_BOOLEAN` and `UCL_STRING` |
| `ucl_object_todouble` | — | `0.0` on failure | — |
| `ucl_object_toint_safe` | `UCL_INT`, `UCL_FLOAT` (→ C `(int64_t)dv`, **truncation toward zero**, UB if out of range), `UCL_TIME` (same) | `int64_t` | everything else, incl. `UCL_BOOLEAN` |
| `ucl_object_toint` | — | `0` on failure | — |
| `ucl_object_toboolean_safe` | `UCL_BOOLEAN` **only** | `value.iv == 1` | everything else, incl. `UCL_INT 0`/`1` and the strings `"true"`/`"false"` |
| `ucl_object_toboolean` | — | `false` on failure | — |
| `ucl_object_tostring_safe` | `UCL_STRING` **only**, and only when `UCL_OBJECT_BINARY` is **clear** | NUL-terminated `const char*` via `ucl_copy_value_trash` | non-strings → `false`. **Binary strings: returns `true` but never writes `*target`** — the caller's variable keeps whatever it held (`NULL` in the unsafe wrapper). A genuine C wart; reproduce as "returns ok with an empty result" |
| `ucl_object_tostring` | — | `NULL` on failure | — |
| `ucl_object_tostring_forced` | any type | `ucl_copy_value_trash(obj)` — for non-strings, the JSON rendering from §2.8; `NULL` if `UCL_OBJECT_BINARY`. **Dereferences `obj` without a NULL check** | — |
| `ucl_object_tolstring_safe` | `UCL_STRING` (binary included) | `*target = value.sv` (raw, possibly not NUL-terminated), `*tlen = obj->len` if `tlen != NULL`. No trash side effect | non-strings → `false` |
| `ucl_object_tolstring` | — | `NULL` on failure; `*tlen` untouched | — |

There is **no** implicit string→number or number→string conversion in the
`to*` family; the only string parsing entry point is
`ucl_object_fromstring_common` (§5.1).

### 6.2 `ucl_object_compare(o1, o2)`

Returns `>0`, `0`, `<0`. **Dereferences both arguments without NULL checks.**

```
if (o1->type != o2->type) return o1->type - o2->type;   /* enum order, §1.1 */
switch (o1->type) {
  UCL_STRING:  if (o1->len == o2->len && o1->len > 0) ret = strcmp(tostring(o1), tostring(o2));
               else                                   ret = o1->len - o2->len;
  UCL_FLOAT, UCL_INT, UCL_TIME:
               ret = (int)(ucl_object_todouble(o1) - ucl_object_todouble(o2));
  UCL_BOOLEAN: ret = (int)toboolean(o1) - (int)toboolean(o2);
  UCL_ARRAY:   if (o1->len == o2->len && o1->len > 0) {
                   for (i = 0; i < vec1->n; i++) {           /* NB: vec1->n, not len */
                       it1 = vec1[i]; it2 = vec2[i];         /* NB: unchecked index into vec2 */
                       if (it1 == NULL && it2 != NULL) return -1;
                       if (it2 == NULL && it1 != NULL) return  1;
                       if (both non-NULL) { ret = compare(it1, it2); if (ret) break; }
                   }
               } else ret = o1->len - o2->len;
  UCL_OBJECT:  if (o1->len == o2->len && o1->len > 0) {
                   for each entry it1 of o1 (insertion order, chain heads only) {
                       it2 = ucl_object_lookup(o2, ucl_object_key(it1));
                       if (it2 == NULL) { ret = 1; break; }
                       ret = compare(it1, it2); if (ret) break;
                   }
               } else ret = o1->len - o2->len;
  default:     ret = 0;      /* UCL_NULL and UCL_USERDATA always compare equal */
}
```

Points a Go port must get exactly right:

* Numeric comparison goes through `double` and is **truncated to `int`**, so two
  distinct `int64` values less than 1 apart in `double` space compare equal, and
  a difference of e.g. `0.5` truncates to `0` — `1.0` and `1.4` compare **equal**.
  Also, a difference larger than `INT_MAX` overflows. Replicate as
  `int(float64(a) - float64(b))` with C truncation-toward-zero semantics.
* Strings of different lengths compare by `len` difference, never by content —
  so `"b"` < `"aa"`. Equal-length strings compare with `strcmp`, which **stops
  at an embedded NUL** and goes through `ucl_object_tostring` (so binary strings
  compare as `NULL` pointers — a crash in C; guard in Go).
* Zero-length strings, empty arrays and empty objects hit the `len` branch and
  compare equal to each other (`0 - 0`).
* Object comparison is **asymmetric**: it only checks that every key of `o1`
  exists in `o2` with an equal value; extra keys in `o2` are invisible except
  through the initial `len` check. It also never compares implicit-array
  siblings.
* Array comparison iterates `vec1->n` but indexes `vec2` with the same `i` — it
  is only safe because `o1->len == o2->len` was checked, and `len` may diverge
  from `n` after `ucl_array_merge` (§4.7). In Go, bound-check and treat an
  out-of-range `it2` as `NULL`.

`ucl_object_compare_qsort(o1, o2)` is just `ucl_object_compare(*o1, *o2)` — the
default comparator for `ucl_object_array_sort`.

`ucl_object_array_sort(ar, cmp)` uses `qsort`, which is **not stable**; equal
elements may be reordered arbitrarily. If exact C output order matters for a
ported test, either the test avoids ties or the Go port must mimic glibc's
introsort — in practice use `sort.SliceStable` and note the divergence.

`ucl_object_sort_keys(obj, how)` — §3.4.

---

## 7. Recommended Go design

### 7.1 One struct, tagged union

Keep the C shape; do not use an `interface{}` value or a per-type Go interface.
The C API constantly mutates an object's type in place
(`ucl_object_fromstring_common` turns a string into an int; `insert_key` turns
`UCL_NULL` into `UCL_OBJECT`), which a sum-of-interfaces model makes painful.

```go
type Type uint16

const (
    Object Type = iota; Array; Int; Float; String; Boolean; Time; Userdata; Null
)

type Object struct {
    // value union — exactly one is meaningful per Type
    iv  int64          // Int, Boolean (0/1)
    dv  float64        // Float, Time
    sv  []byte         // String (raw bytes; len(sv) is NOT authoritative, see Len)
    av  []*Object      // Array (elements may be nil)
    ov  *Hash          // Object (may be nil until first insert)
    ud  any            // Userdata payload
    udDtor    func(any)
    udEmitter func(any) string

    Key    []byte      // may be nil; compared by exact bytes
    Next   *Object     // implicit-array chain
    Prev   *Object     // head.Prev == tail; fresh object's Prev == itself
    Len    uint32      // per-type semantics, §2.4 — keep it explicit, do not derive
    Flags  uint16      // low 12 bits flags, high 4 bits priority
    Type   Type

    valueTrash []byte  // cached string rendering; see §2.8
    hasValueTrash bool
}
```

* Keep `Len` as a real field. Deriving it from `len(av)`/hash size breaks
  `ucl_object_compare` and the `ucl_array_merge` quirk. Add
  `ArraySize()` returning `uint32(len(av))` for `ucl_array_size`.
* Keep `sv` as `[]byte`, not `string`: strings can carry embedded NULs
  (`UCL_OBJECT_BINARY`) and `Len` may legitimately exceed the usable content
  after a copy. Provide `String() string` doing the `tostring` rules.
* Model `trash_stack[UCL_TRASH_KEY]` as nothing at all — Go strings/slices are
  already owned copies. Model `trash_stack[UCL_TRASH_VALUE]` as the
  `valueTrash`/`hasValueTrash` pair, because its *caching* is observable
  (§2.8) — `ToStringForced` must return the cached rendering when present and
  must overwrite `Len` when it first renders a non-string.
* Flags: keep the raw `uint16` and provide `Priority() uint`,
  `SetPriority(uint)` implementing §2.7 bit-for-bit. Do **not** split priority
  into its own field: `ucl_object_set_priority` clobbers nothing else, but
  emitters and tests read `flags` directly in the C test suite.

### 7.2 The chain

Model `Next`/`Prev` literally as pointers with the DL invariants
(`head.Prev == tail`, `tail.Next == nil`, singleton's `Prev == self`). A Go
slice would be cleaner but the C API exposes `ucl_elt_append`,
`ucl_object_iterate(..., false)`, and the parser's `DL_CONCAT` splice, all of
which are pointer-level operations that ported tests will exercise directly.
Provide helpers `dlAppend(head, elt)`, `dlConcat(head1, head2)`,
`dlReplaceElem(&head, el, add)`, `dlDelete(&head, del)` mirroring `utlist.h`.

### 7.3 The hash

```go
type Hash struct {
    m        map[string]*hashElt   // key: the raw key bytes, folded if caseless
    order    []*hashElt            // insertion order; the emission contract
    caseless bool
}
type hashElt struct{ obj *Object; idx int }  // idx keeps delete/replace O(1)
```

* Map key: `string(keyBytes)` for the case-sensitive hash, `string(foldASCII(keyBytes))`
  for the caseless one, where `foldASCII` maps only `'A'..'Z'` → `'a'..'z'`.
  Never use `strings.ToLower` (it is Unicode-aware and would diverge).
* `Insert` appends to `order` **only when the key is new**, and is a silent
  no-op otherwise (§3.2).
* `Replace(old, new)` must write the new elt into the **same `order` slot**.
* `Delete(obj)` looks up by key bytes, removes from the map, and removes from
  `order` (`append(order[:i], order[i+1:]...)` plus an `idx` fix-up, or a
  tombstone + compaction). Order of survivors must be preserved.
* `Iterate` walks `order`. Return chain heads only; callers walk `Next`.
* `Sort(flags)` — stable sort of `order` by (len(key), key bytes) with the exact
  comparator from §3.4, including the little-endian 4-byte-block quirk in the
  caseless variant, then recurse into `Object`-typed children when
  `UCL_SORT_KEYS_RECURSIVE`.

### 7.4 Refcounting in Go

`Ref`/`Unref`/`Free` become no-ops and the GC handles lifetime — **except for
three places where refcount semantics leak into behaviour**:

1. **`UCL_OBJECT_EPHEMERAL` + `ucl_object_ref` ⇒ deep copy.** This is a real
   behavioural branch: `Ref(obj)` must return `Copy(obj)` when the flag is set.
   Anything that stores the result then holds an independent object, so mutating
   it does not affect the source. Keep `Ref` as a real function with this branch;
   do not inline it away.
2. **Aliasing vs. copying in `merge`/`array_merge`/`insert`.** The `copy bool`
   parameters of `ucl_object_merge` and `ucl_array_merge` decide whether the
   destination *shares* the source's child objects or owns deep copies. With
   `copy == false` the two trees genuinely alias, and a later mutation through
   either is visible through both. Go pointers reproduce this for free — but the
   port must not "helpfully" deep-copy when `copy == false`.
3. **Ownership transfer.** `ucl_object_insert_key`, `ucl_array_append`,
   `ucl_array_prepend` consume the caller's reference; `ucl_object_pop_key*`,
   `ucl_array_delete`, `ucl_array_pop_*`, `ucl_array_replace_index` hand one
   back. In Go these are pure pointer moves, but the *documented* contract
   should stay identical so ported C tests read the same. Note in particular
   that `ucl_array_replace_index` and `pop_*` return an object that is still
   linked into nothing — its `Next`/`Prev` are untouched, so a popped
   implicit-array head still carries its siblings.

Also note `ucl_object_free` (deprecated) frees regardless of refcount; in Go it
is a no-op, and any test that checks "use after free" behaviour must simply be
dropped.

### 7.5 Suggested package surface

Mirror the C names one-to-one at the internal layer (per the project's
"faithful C-API mirror first" direction):

```
NewObject() / TypedNew(t) / NewFull(t, prio) / NewUserdata(dtor, emitter, ptr)
FromString(s) / FromLString(b) / FromStringCommon(b, flags) / FromInt / FromDouble / FromBool
(o *Object) Type() / TypeString() / StringToType(s)
(o *Object) Key() / KeyL()
(o *Object) InsertKey(elt, key, copyKey) / InsertKeyMerged / ReplaceKey
(o *Object) DeleteKey(key) / PopKey(key)
(o *Object) Lookup(key) / LookupLen(key) / LookupAny(keys...) / LookupPath(p) / LookupPathChar(p, sep)
(o *Object) Merge(elt, copy)
ArrayAppend / ArrayPrepend / ArrayMerge / ArrayDelete / ArrayHead / ArrayTail /
ArrayPopLast / ArrayPopFirst / ArraySize / ArrayFindIndex / ArrayIndexOf / ArrayReplaceIndex
EltAppend(head, elt)
ToDouble / ToDoubleSafe / ToInt / ToIntSafe / ToBoolean / ToBooleanSafe /
ToString / ToStringSafe / ToStringForced / ToLString / ToLStringSafe
Iterate(o, *iter, expandValues) + IterateWithError + IterateEnd
IterateNew / IterateReset / IterateSafe / IterateFull / IterateFree / IterChkExcpn
Ref / Unref / Free / Copy
Compare / CompareQsort / ArraySort / SortKeys
GetPriority / SetPriority / Reserve
```

Idiomatic sugar (`Get(key) (*Object, bool)`, `Range(func(k string, v *Object) bool)`,
`json.Marshaler`) belongs in the later layer, not here.

### 7.6 Where a naïve Go port will silently diverge — checklist

1. `Len` derived from container size instead of tracked (breaks `Compare`).
2. Emission order taken from map iteration instead of the ordered list.
3. `Replace` implemented as delete+insert (moves the key to the tail).
4. `insert_key_merged`'s "scalar found, object elt" case implemented as replace
   instead of delete+insert (that one **does** move to the tail).
5. `strings.ToLower` instead of ASCII-only folding for the caseless hash.
6. Lexicographic key sorting instead of length-major (§3.4).
7. Real lexicographic comparison instead of the little-endian uint32 quirk in
   the caseless sort comparator.
8. Comparing numbers exactly instead of `int(float64 difference)` truncation.
9. `strconv.ParseInt` accepting `0x`/`0b`/`_` where libucl does not, or treating
   a leading `0` as octal (libucl never does).
10. Treating `"y"`/`"1"` as booleans (only the six words in §5.2 count).
11. `10m` interpreted as minutes (it is 10 000 000; only `10min` is minutes).
12. Fixing the `ucl_array_merge` `len` bug or the `keylen == 0` lookup asymmetry
    instead of reproducing them.
13. Recomputing the value trash for a `fromstring_common`-parsed number instead
    of returning the cached original text.
14. Losing the `UCL_OBJECT_EPHEMERAL` ⇒ deep-copy branch in `Ref`.
