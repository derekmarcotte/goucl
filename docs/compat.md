# Compatibility Target: `github.com/mitchellh/go-libucl` @ f9d0e55

This is the exact drop-in surface the pure-Go port must reproduce. Source:
`/home/claude/libucl-go/go-libucl/{libucl.go,parser.go,object.go,decoder.go,go-libucl.h}`.
C reference: `/home/claude/libucl-go/libucl/include/ucl.h` (v0.9.4).

`package libucl`. Nothing else is exported anywhere in the package.

---

## 1. Complete exported API surface

### Package-level functions

```go
func ParseString(data string) (*Object, error)
func NewParser(flags ParserFlag) *Parser
```

### Types

```go
type ParserFlag int
type MacroFunc func(string)
type Parser struct{ /* unexported */ }
type Object struct{ /* unexported */ }
type ObjectIter struct{ /* unexported */ }
type ObjectType int
type Emitter int
```

### `*Parser` methods

```go
func (p *Parser) AddString(data string) error
func (p *Parser) AddFile(path string) error
func (p *Parser) Close()
func (p *Parser) Object() *Object
func (p *Parser) RegisterMacro(name string, f MacroFunc)
```

### `*Object` methods

```go
func (o *Object) Close() error
func (o *Object) Emit(t Emitter) (string, error)
func (o *Object) Delete(key string)
func (o *Object) Get(key string) *Object
func (o *Object) Iterate(expand bool) *ObjectIter
func (o *Object) Key() string
func (o *Object) Len() uint
func (o *Object) Ref() error
func (o *Object) Type() ObjectType
func (o *Object) ToBool() bool
func (o *Object) ToInt() int64
func (o *Object) ToFloat() float64
func (o *Object) ToString() string
func (o *Object) Decode(v interface{}) error   // decoder.go
```

### `*ObjectIter` methods

```go
func (o *ObjectIter) Close()
func (o *ObjectIter) Next() *Object
```

Note the asymmetry: `ObjectIter.Close()` has **no return value** (unlike
`Object.Close() error`). This must be preserved exactly — it's part of the
call-site contract.

---

## 2. Per-item C calls and semantics

### `ParseString(data string) (*Object, error)`
```go
p := NewParser(0)
defer p.Close()
if err := p.AddString(data); err != nil { return nil, err }
return p.Object(), nil
```
Convenience wrapper. Flags are always `0` (`UCL_PARSER_DEFAULT`) — there is no
way to pass flags through `ParseString`. Parser is closed before returning, but
`p.Object()` already took its own ref via `ucl_parser_get_object` (see below),
so the returned `*Object` stays valid after the parser is freed. **The pure-Go
core must reproduce this independence**: the returned root object must not
become invalid when the originating parser is discarded.

### `NewParser(flags ParserFlag) *Parser`
Calls `C.ucl_parser_new(C.int(flags))`. Stores the raw `*C.struct_ucl_parser`
and an empty `macros []int` slice. Never returns nil in practice (`ucl_parser_new`
only fails on OOM, unhandled).

### `(*Parser) AddString(data string) error`
Calls `C.ucl_parser_add_string(p.parser, cs, C.size_t(len(data)))`. `len(data)`
is passed explicitly (not 0), so this is never subject to the C API's
"len==0 means treat data as NUL-terminated" special case — a zero-length Go
string parses as a zero-length chunk, not as "read until NUL".
On failure (`result == false`): fetches `C.ucl_parser_get_error(p.parser)` and
returns `errors.New(<that C string>)`. **The exact error string text produced
by the parser (syntax errors etc.) is part of the contract** — Go's parser must
produce byte-identical (or at least behaviorally equivalent, tests permitting)
error text to what `ucl_parser_get_error` returns in the C parser for the same
malformed input, to the extent any test asserts on error text (none of the
three _test.go files currently assert exact error text — only `err != nil`).
Calling `AddString` multiple times on one parser accumulates chunks (per
`ucl_parser_add_chunk` semantics: later chunks merge with/override earlier
top-level keys per libucl's duplicate-key priority rules) — no test here
exercises multiple `AddString` calls, but the pure-Go parser must support it
since it's part of the public contract.

### `(*Parser) AddFile(path string) error`
Calls `C.ucl_parser_add_file(p.parser, cs)`. Same error path as `AddString`.
Reads the named file, default priority. No file-not-found test exists, but the
underlying C call returns `false` + sets `ucl_parser_get_error` to an IO error
description on missing/unreadable files — the Go port needs an equivalent path
(`os.Open` failure) surfaced through the same `error` return.

### `(*Parser) Close()`
```go
C.ucl_parser_free(p.parser)
if len(p.macros) > 0 { delete each registered macro id from the global map }
```
`ucl_parser_free` releases the C parser (and drops its ref on any object it
was still holding internally — irrelevant once ported to Go). The macro-map
cleanup is pure Go-side bookkeeping for the cgo indirection (int id → Go
closure) and has **no C equivalent to preserve** — in the pure-Go port this
becomes an ordinary struct field cleanup, or is dropped entirely since Go
closures need no manual deregistration. Calling `Close()` twice on a real cgo
parser would double-free the C parser (undefined behavior in C); the pure-Go
version should make this safe (no-op on a nil/already-closed parser) since
nothing in the tests exercises double-Close on `Parser`, but robustness here
costs nothing and avoids a footgun the C version had.

### `(*Parser) Object() *Object`
Calls `C.ucl_parser_get_object(p.parser)`. Per the C doc: **"refcount is
increased"** — the parser and the returned object are independently owned
after this call. If the C call returns `NULL` (e.g. nothing was ever
successfully parsed), returns Go `nil`. `TestParser`/`TestParserAddFile` both
assert `obj != nil` after `AddString`/`AddFile` + `Object()`.

### `(*Parser) RegisterMacro(name string, f MacroFunc)`
Registers `f` in a package-global `map[int]MacroFunc` (guarded by
`macrosLock sync.Mutex`), allocating the next free integer id (skips ids
already in use — see the `for macros[macrosIdx] != nil { macrosIdx++ }` reuse
loop), records that id in `p.macros` for cleanup on `Close`, then calls
`C.ucl_parser_register_macro(p.parser, cname, C._go_macro_handler_func(), C._go_macro_index(idx))`.
The C side stores `idx` as an opaque `void*` user-data pointer (cast, not a
real pointer — a callback-indirection hack required only because cgo can't
pass Go closures across the C boundary). **This entire id-map/mutex apparatus
is a cgo-only workaround.** In the pure-Go port, `MacroFunc` closures can be
stored directly (e.g. `map[string]MacroFunc` on the `Parser` struct, or a
closure captured in the parser's macro-invocation table) — no int-id
indirection, no global mutex, no `go_macro_call` export needed.
Semantics to preserve: the macro handler in C is `_go_macro_handler`, which
calls back into `go_macro_call(idx, data, n)` → looks up `f` in the map → if
found, calls `f(C.GoStringN(data, n))` and returns `true` (macro handled
successfully); if not found (defensive dead code — shouldn't happen in
practice), returns `false`. Per `ucl_parser_register_macro`'s doc, **the
handler is called synchronously immediately when the macro is parsed**, macro
name is registered *without* its leading `.`, and the handler receives the
raw macro argument bytes as they appear in the source (here decoded as a Go
string via `GoStringN` with explicit length, not NUL-terminated `GoString`).
`TestParserRegisterMacro` proves: `.foo "bar";` invokes the registered `"foo"`
handler with `data == "bar"` exactly once, synchronously during `AddString`.

### `(*Object) Close() error`
```go
C.ucl_object_unref(o.object)
return nil
```
Always returns `nil` — this is not a fallible operation in the Go wrapper,
regardless of what the C refcounting does internally. `ucl_object_unref`
decrements refcount and frees when it hits zero; calling it on an object whose
refcount is already exhausted, or double-`Close()`-ing the same `*Object`
handle, is a use-after-free in C (UB — the tests never do this deliberately,
but many code paths in `decoder.go` and the tests rely on `Close()` being
called exactly once per obtained reference). **In the pure-Go port this becomes
a no-op returning `nil`** (per HANDOFF.md's decision) — no refcounting, no
freeing, so double-`Close()` must be harmless and cannot be observed as an
error by any test.

### `(*Object) Emit(t Emitter) (string, error)`
```go
result := C.ucl_object_emit(o.object, uint32(t))
if result == nil { return "", nil }
return C.GoString(C._go_uchar_to_char(result)), nil
```
**Quirk: this method can never return a non-nil error.** Even if
`ucl_object_emit` fails (returns `NULL` — e.g. unsupported emitter enum value,
or internal allocation failure), the Go wrapper reports success with an empty
string, not an error. The pure-Go `Emit` must reproduce this — callers that
check `err != nil` will never see it trip, by design/accident of this binding.
Also note: `ucl_object_emit`'s C doc says the returned buffer "must be freed
after using" — the cgo binding **never frees it** (a real memory leak in the
C binding on every successful `Emit` call). This is irrelevant to a pure-Go
implementation (no malloc'd buffer exists) but is worth knowing so it isn't
mistakenly "fixed" in a way that changes observable behavior (there is none to
change — GC just handles it in Go).
`TestObjectEmit`/`TestObjectEmit_EmitConfig` pin the **exact byte-for-byte**
output of the JSON and Config emitters for `foo = bar; bar = baz;`:
- `EmitJSON` → `"{\n    \"foo\": \"bar\",\n    \"bar\": \"baz\"\n}"` (4-space
  indent, no trailing newline)
- `EmitConfig` → `"foo = \"bar\";\nbar = \"baz\";\n"` (trailing newline, each
  key on its own line, values double-quoted)
These exact formatting rules (indentation, quoting, newline placement) come
from libucl's `ucl_emitter.c`/`ucl_emitter_utils.c` and must be matched
precisely by the Go emitter, not merely produce "equivalent" JSON/config.

### `(*Object) Delete(key string)`
```go
C.ucl_object_delete_key(o.object, ckey)
```
Return value of `ucl_object_delete_key` (`bool`, whether key existed) is
**discarded** — `Delete` has no way to report "key not found," it's always a
no-op-if-absent, no-error void call. Per C doc: "old object will be
unrefered" (freed if refcount hits 0). `TestObjectDelete` proves delete then
`Get` returns nil afterward. `TestObjectDelete_unknown` proves deleting an
absent key (`"foo"` when only `"bar"` exists) does not panic/error.

### `(*Object) Get(key string) *Object`
```go
obj := C.ucl_object_find_keyl(o.object, ckey, C.size_t(len(key)))
if obj == nil { return nil }
result := &Object{object: obj}
result.Ref()
return result
```
`ucl_object_find_keyl` is `#define`d to `ucl_object_lookup_len` — exact-length
key lookup within an `UCL_OBJECT`-typed `o` (the *first* matching key if
present; on a fresh lookup this returns the *head* of any implicit-array chain
for duplicate keys, see §4). Returns `NULL`/Go `nil` if not found or if `o` is
not an object type. The wrapper then explicitly `Ref()`s the found object
before returning it — i.e. **every `Get()` call bumps the refcount of the
returned node**, matching the general pattern that every `*Object` handed to
the caller owns one ref that must be `Close()`d. **Important non-obvious
behavior a pure-Go implementation must reproduce: repeated `Get(key)` calls
for the same key return handles that wrap the *same underlying node identity*
(same C pointer)** — see §6/§7, `TestObjectDecode_mapStructObject` depends on
this via `reflect.DeepEqual` over `*Object` pointer fields.

### `(*Object) Iterate(expand bool) *ObjectIter`
```go
C.ucl_object_ref(o.object)
return &ObjectIter{expand: expand, object: o.object, iter: nil}
```
Bumps refcount on `o`'s underlying object (the iterator holds its own ref,
independent from `o`), and seeds a fresh opaque C iterator state
(`ucl_object_iter_t iter = NULL`, per the required init pattern in the C doc).
`expand` is threaded straight through to every `Next()` call as the
`expand_values` bool passed to `ucl_object_iterate`/`ucl_object_iterate_with_error`.
Per C semantics: `expand=true` walks *values* — for a container, yields each
child; for an implicit-array-chained scalar/object node, also expands the
`next` chain transparently as if it were a real array. `expand=false` walks
*keys* without expanding same-key duplicate chains (so duplicate keys are only
visible as their single head node — the test `TestObjectDecode_mapMulti`/`decodeIntoMap`
relies on iterating with `expand=false` at the outer level specifically to get
one entry per *distinct key*, then a nested `expand=true` iterate over each to
walk the duplicate-key chain).
"The iterator does not need to be fully consumed" (per the Go doc comment) —
partial iteration + `Close()` must be safe; the C doc's `ucl_object_iterate_end`
exists for exactly this (freeing internal heap state for `UCL_OBJECT`
iteration abandoned early) but this Go binding does **not** call
`ucl_object_iterate_end` — it only calls `ucl_object_unref` on the top object
in `ObjectIter.Close()`. This suggests the underlying C library's simple
iterator (this codebase predates the "safe iterator" API) doesn't leak on
early abandonment for the plain `ucl_iterate_object` — a pure-Go port has no
such internal-heap-state concern at all since Go iteration state is just Go
values.

### `(*Object) Key() string`
```go
return C.GoString(C.ucl_object_key(o.object))
```
Per C doc: "key or NULL if there is no key." `C.GoString(NULL)` in cgo returns
`""`, so root/keyless objects (e.g. an array element, or the top-level parsed
object) yield `Key() == ""`. No test exercises the empty-key path directly but
`TestObjectIterate_array` implicitly relies on array elements still working
via `ToString()` without ever calling `.Key()` on them.

### `(*Object) Len() uint`
```go
if o.Type() == ObjectTypeObject && o.object.next != nil {
    iter := o.Iterate(false)
    defer iter.Close()
    var count uint = 0
    for obj := iter.Next(); obj != nil; obj = iter.Next() {
        obj.Close()
        count += 1
    }
    return count
}
return uint(o.object.len)
```
**THE quirk called out in HANDOFF.md** — see full writeup in §4. Otherwise,
returns the raw `len` field of the underlying `ucl_object_t` struct (a
`uint32_t`, widened to Go `uint`): "size of an object" per the C struct
comment — for `UCL_OBJECT` it's the key count, for `UCL_ARRAY` the element
count, for scalar types it is implementation-defined/unused-by-this-binding
(e.g. may hold string byte length for `UCL_STRING`, is unspecified for
`UCL_INT`/`UCL_FLOAT`/`UCL_BOOLEAN`/`UCL_NULL` — nothing in this codebase
relies on `Len()` for scalar types except as a slice-capacity hint in
`decoder.go`, where a wrong/nonsensical value only costs a `reflect.MakeSlice`
mis-sized cap, not correctness, since actual appends are driven by iteration).

### `(*Object) Ref() error`
```go
C.ucl_object_ref(o.object)
return nil
```
Always returns nil (same non-fallible pattern as `Close`). Per C doc,
increments refcount and **returns the same object pointer** (C API detail —
the Go wrapper discards that return value and just re-uses its existing `o`).
Becomes a no-op returning `nil` in pure Go (§7).

### `(*Object) Type() ObjectType`
```go
return ObjectType(C.ucl_object_type(o.object))
```
Direct enum passthrough — values must match `ucl_type_t` exactly (§3).

### `(*Object) ToBool/ToInt/ToFloat/ToString`
```go
C.ucl_object_toboolean(o.object)  -> bool
C.ucl_object_toint(o.object)      -> int64
C.ucl_object_todouble(o.object)   -> float64
C.ucl_object_tostring(o.object)   -> C.GoString(...) string
```
All four call the **"unsafe" variants** (`ucl_object_toboolean` not
`_safe`, etc.) — per C doc these perform best-effort type coercion (the
"conversion compatibility" table near `ucl_type_t`: e.g. `UCL_TIME`→`UCL_FLOAT`,
`UCL_FLOAT`→`UCL_INTEGER` are convertible) and **do not report failure** — if
the object's actual type is incompatible with the requested conversion, the
`_safe` sibling would return `false`/leave `target` untouched, but these
unsafe forms just return some (possibly zero/garbage-per-C, but well-defined
per libucl's actual conversion switch) value with no error signal at all. The
pure-Go port must replicate libucl's actual coercion table for each of these
four conversions (bool/int/float/string), not just "reinterpret the matching
type," since `decoder.go` calls `ToBool()`/`ToInt()`/`ToString()` on objects
whose static type doesn't always match (it dispatches on `o.Type()` itself
first in most cases, but the interface-decode path for `ObjectTypeInt`/
`ObjectTypeBoolean` types constructs a zero value and redecodes into it,
ultimately routing back through `decodeIntoInt`/`decodeIntoBool`, which in the
non-string-type branch always call the unsafe `ToInt()`/`ToBool()` — relying on
libucl's real coercion, e.g. float→int truncation, bool-from-nonzero-number,
etc.). Precise float round-tripping is pinned by
`TestObjectToFloat_oneThird`/`_negativeOneThird` (see §6) — this requires
`ToFloat`'s underlying number parsing to match C `strtod` bit-for-bit for
those literals.

### `(*Object) Decode(v interface{}) error`
Entry point into `decoder.go`; see §5.

### `(*ObjectIter) Close()`
```go
C.ucl_object_unref(o.object)
```
Releases the iterator's own ref on the top object taken in `Iterate()`. No
return value (contrast with `Object.Close() error`). Becomes a no-op in pure
Go.

### `(*ObjectIter) Next() *Object`
```go
obj := C.ucl_iterate_object(o.object, &o.iter, C._Bool(o.expand))
if obj == nil { return nil }
C.ucl_object_ref(obj)
return &Object{object: obj}
```
`ucl_iterate_object` is `#define`d to `ucl_object_iterate`, itself `#define`d
to `ucl_object_iterate_with_error(ob, it, ev, NULL)` (no error pointer used
here — errors like `ENOMEM` during iteration are silently swallowed by this
binding). Each yielded object gets its own `Ref()` bump before being wrapped —
**every value produced by iteration is an owned reference the caller must
`Close()`**, exactly like `Get()`. Returns Go `nil` once iteration is
exhausted, at which point `o.iter`'s internal state is fully consumed (no
explicit "reset" — `ObjectIter` values are single-pass).

---

## 3. Exact numeric constant values

All three enums are cgo-derived from C enums in `include/ucl.h`. Values below
are read directly from the header (§ line numbers as found) — **the pure-Go
`iota` blocks must reproduce these exact integers**, since callers of the
binding compare/serialize/switch on them.

### `ObjectType` ↔ `ucl_type_t` (ucl.h:134-144)

| Go constant | value | C constant |
|---|---|---|
| `ObjectTypeObject`   | 0 | `UCL_OBJECT` |
| `ObjectTypeArray`    | 1 | `UCL_ARRAY` |
| `ObjectTypeInt`      | 2 | `UCL_INT` |
| `ObjectTypeFloat`    | 3 | `UCL_FLOAT` |
| `ObjectTypeString`   | 4 | `UCL_STRING` |
| `ObjectTypeBoolean`  | 5 | `UCL_BOOLEAN` |
| `ObjectTypeTime`     | 6 | `UCL_TIME` |
| `ObjectTypeUserData` | 7 | `UCL_USERDATA` |
| `ObjectTypeNull`     | 8 | `UCL_NULL` |

(Go source is a plain `iota` block in this exact order — matches the C order
1:1, confirmed against ucl.h.)

### `Emitter` ↔ `ucl_emitter_t` (ucl.h:149-156)

| Go constant | value | C constant |
|---|---|---|
| `EmitJSON`        | 0 | `UCL_EMIT_JSON` |
| `EmitJSONCompact` | 1 | `UCL_EMIT_JSON_COMPACT` |
| `EmitConfig`      | 2 | `UCL_EMIT_CONFIG` |
| `EmitYAML`        | 3 | `UCL_EMIT_YAML` |

C has two more values the Go binding does **not** expose:
`UCL_EMIT_MSGPACK = 4`, `UCL_EMIT_MAX = 5` (sentinel/unsupported). If the
pure-Go port ever adds MessagePack emit, it must be given value `4` to stay
consistent with the C enum, even though nothing in current go-libucl uses it.

### `ParserFlag` ↔ `ucl_parser_flags_t` (ucl.h:163-173) — **bitmask, not iota**

These are **not** sequential `iota` values — they're independent bit flags,
each assigned directly from the matching C `#define`-style enum value:

| Go constant | value | C constant |
|---|---|---|
| `ParserKeyLowercase` | 1 (`1<<0`) | `UCL_PARSER_KEY_LOWERCASE` |
| `ParserZeroCopy`     | 2 (`1<<1`) | `UCL_PARSER_ZEROCOPY` |
| `ParserNoTime`       | 4 (`1<<2`) | `UCL_PARSER_NO_TIME` |

C also defines, unexposed by this binding: `UCL_PARSER_DEFAULT = 0`,
`UCL_PARSER_NO_IMPLICIT_ARRAYS = 1<<3` (8), `UCL_PARSER_SAVE_COMMENTS = 1<<4`
(16), `UCL_PARSER_DISABLE_MACRO = 1<<5` (32), `UCL_PARSER_NO_FILEVARS = 1<<6`
(64). `NewParser(0)` is the implicit "default flags" call used everywhere in
the tests and in `ParseString`.
Typing note: in the original source, only `ParserKeyLowercase` carries an
explicit `ParserFlag` type annotation on its const line; `ParserZeroCopy`/
`ParserNoTime` are written as bare `= C.UCL_PARSER_ZEROCOPY` continuations,
so strictly their declared type follows Go's per-line inference from the cgo
constant expression rather than being forced to `ParserFlag`. This has no
behavioral consequence at any call site in the tests (both are passed straight
into `int`-accepting or `ParserFlag`-typed positions and work either way); a
clean pure-Go rewrite may simply give all three an explicit `ParserFlag` type
without changing observable behavior.

---

## 4. Quirks to preserve deliberately

1. **`Len()` iteration-vs-field quirk** (HANDOFF.md's flagged one, `object.go`
   `Len()`, full code in §2). Triggers only when **both** conditions hold:
   `Type() == ObjectTypeObject` **and** the underlying node's C `next` pointer
   is non-nil. This happens specifically when a key is defined more than once
   as a nested block (e.g. `bundle "foo" {}; bundle "bar" {};` — both values
   under key `bundle` are `UCL_OBJECT`-typed, so libucl chains them via
   `next`/`prev` into an implicit array whose *head* node is still typed
   `UCL_OBJECT`). In that case the head's own `len` field describes only *that
   one object's* key count, not the chain length, so `Len()` must count by
   walking the `next` chain via a fresh non-expanding iterator instead. Proven
   by `TestObjectLen_object` (`Len() == 2` for two `bundle` blocks). For
   non-object types (e.g. duplicate scalar keys — `foo = foo; foo = bar;`),
   `Len()` does **not** special-case the chain and just returns the raw `len`
   field of the head node, which is unrelated to the chain length (see
   `TestObjectDecode_sliceRepeatedKey`, which never calls `.Len()` directly on
   that chain, only iterates it — so this half of the quirk is *latent*: the
   pure-Go port must reproduce the raw-field read for non-object head types
   even though no test currently pins its actual returned number).

2. **Pointer/identity stability of `Get()` across repeated calls.** Two
   separate `Get("foo")` calls (or a `Get` vs. a value reached via iteration)
   for the *same* underlying key must yield `*Object` values that are deeply
   equal via `reflect.DeepEqual` — in the cgo binding this falls out for free
   because both wrap the identical `*C.ucl_object_t` pointer, and
   `reflect.DeepEqual` on structs holding equal pointers short-circuits to
   true without inspecting the pointee. `TestObjectDecode_mapStructObject`
   depends on exactly this: it independently does
   `obj.Get("value").Get("foo")` to build `fooObj`, then separately calls
   `obj.Decode(&result)` (which internally does its own `Get`/iterate walk to
   populate `result.Value["foo"].Object` via the `libucl:",object"` tag), and
   asserts `reflect.DeepEqual(result.Value, expected)` where
   `expected["foo"].Object == fooObj`. **A pure-Go core must make repeated
   lookups of the same logical node return handles referencing the identical
   underlying node value/pointer** (not freshly-allocated copies with equal
   contents) or this test will fail even though the data is "equal." The
   natural way to get this for free: keep the internal object graph as
   pointer-linked nodes (`*node`) and have `*Object` simply wrap/embed that
   same `*node` pointer, exactly mirroring the C ownership model, per
   HANDOFF's "mirror the C API" design directive.

3. **`Emit()` never returns an error**, even on underlying emit failure —
   returns `("", nil)` when the emit call yields nothing. See §2. Any
   reimplementation must not "improve" this by surfacing a real error; tests
   only exercise the success path but the *no-error-path* API shape (return
   type `(string, error)` where error is always nil in practice) must not be
   changed since it's part of the function signature contract even if unused.

4. **`Close()`/`Ref()` asymmetry across types**: `Object.Close() error` vs.
   `ObjectIter.Close()` (no return). Both must keep their exact signatures.

5. **`Delete()` and `Ref()`/`Close()`'s C bool/void return values are
   discarded** — `Delete` cannot report "key not found," `Ref`/`Close` cannot
   report ucl-level ref-count faults. Preserve the discarding (i.e. don't add
   new error returns not in the original signatures).

6. **`AddString` always passes the true byte length**, never `0`/NUL-sentinel
   — an empty-string `AddString("")` must be treated as "zero-length chunk,"
   not "read until NUL," matching `len(data)` being passed explicitly rather
   than `0`.

7. **`Iterate(expand)` semantics differ meaningfully by `expand`** and
   `decoder.go` depends on switching between them at different tree depths
   (see §5) — outer key-iteration commonly uses `expand=false` to get one
   iteration step per distinct key (yielding just the head of any
   duplicate-key chain), while inner iteration commonly uses `expand=true` to
   walk every value under that key (including the implicit-array chain and/or
   real arrays) as a flat sequence. Collapsing this distinction would break
   every multi-value-key test (`TestObjectDecode_mapMulti`,
   `TestObjectDecode_mapObjectMultiple`, `TestObjectDecode_nestedStructRepeated`,
   `TestObjectDecode_structArray`, `TestObjectDecode_sliceRepeatedKey`, etc).

8. **Key iteration/emission order must match declaration order** (see §6) —
   this is really a parser/object-model property, not an API-shape quirk, but
   it's load-bearing for nearly every test that checks emitted or iterated
   output (`TestObjectIterate`, `TestObjectEmit*`). The underlying C hash
   table (`uthash`-based) preserves insertion order for iteration; a Go
   reimplementation using a plain `map[string]*node` would not, and must
   instead use an order-preserving structure (e.g. keep the linked-list/`next`
   chain as authoritative iteration order, with a separate index map only for
   O(1) `Get`/`Delete` lookup).

---

## 5. `decoder.go` summary and its exact `*Object` API dependency

`decoder.go` (330 lines) is a mapstructure-style reflective decoder,
`(*Object) Decode(v interface{}) error` entry point, struct tag name
`"libucl"`. It dispatches purely on `reflect.Kind` of the destination:
`Bool`, `Interface`, `Int`, `Map`, `Ptr`, `Slice`, `String`, `Struct` (anything
else → `"unsupported type"` error). Notable behaviors:

- **`decodeIntoInt`**: only handles `reflect.Int` (not `Int8/16/32/64` or
  unsigned variants) — `strconv.ParseInt(s, 0, result.Type().Bits())` for
  string-typed source objects (auto base detection, so `"0x2A"` parses as
  hex), else `o.ToInt()` for everything else.
- **`decodeIntoInterface`**: builds native Go values with no external type
  hint — arrays become `[]interface{}` (via `expand=true` iteration, guessing
  each element's Go type recursively), objects become `[]map[string]interface{}`
  (**always a slice**, even for a single non-duplicated key block — this is
  because it iterates the *outer* object with `expand=false`, so one map per
  distinct-key occurrence in the implicit-array-chain sense; see
  `TestObjectDecode_mapObjectMultiple`/`TestObjectDecode_mapObject`, both
  assert a `[]map[string]interface{}` result even with a single block).
  Booleans/ints/strings redecode through their concrete-type decode functions
  (`redecode = true` path).
- **`decodeIntoMap`**: requires `o.Type() == ObjectTypeObject`, else errors
  (`TestObjectDecode_mapNonObject`). Map key type must be `string`-kind, else
  errors. Preserves pre-existing map entries not touched by decode
  (`TestObjectDecode_mapNonNil`). Iterates outer with `expand=false`, inner
  per-key with `expand=true`, reusing any pre-existing value at that map key
  as the decode target seed (`TestObjectDecode_mapReuseVal` — lets values
  spread across duplicate blocks merge field-by-field into one map entry).
- **`decodeIntoPtr`**: allocates a new pointee, decodes into it, sets the
  pointer.
- **`decodeIntoSlice`**: `expand := o.Type() != ObjectTypeObject` — i.e. slices
  expand everything except when the *source* itself is a plain object type
  (then it walks it un-expanded, though this path is mostly reached through
  the struct-field dispatch below, which special-cases slices explicitly).
- **`decodeIntoString`**: only `Boolean`/`String`/`Int` source types convert
  to string (`Float` is **not supported** — not tested, but note the gap: a
  Float object decoded into a `string` field would hit the `default:` error
  branch, `"unsupported type to string"`).
- **`decodeIntoStruct`**: the most complex path.
  - Handles **anonymous/embedded fields** with `libucl:",squash"` tag —
    squashed fields' sub-fields are decoded as if flattened into the parent
    (`TestObjectDecode_structSquash`); embedded fields must be struct-kind or
    it errors.
  - Special tag values (as the **second** comma-separated tag component,
    field name empty or given): `",decodedFields"` (collect names of fields
    that were actually populated, as `[]string`, in field-processing order —
    map iteration order note below), `",key"` (set field to `o.Key()` instead
    of looking up a child), `",object"` (set field to the current child
    `*Object` itself, after `elem.Ref()` — the field must be typed `*Object`),
    `",unusedKeys"` (collect keys present in the source object but never
    consumed by any struct field, as `[]string`, nil if none unused).
  - Ordinary fields: tag's first component overrides the lookup key name
    (else uses the Go field name as-is, case-sensitive first). Lookup order:
    exact `o.Get(fieldName)`; if not found, a **case-insensitive linear scan**
    via `o.Iterate(true)` comparing `strings.EqualFold` against each key,
    taking the first case-insensitive match (`Close()`ing every rejected
    candidate along the way to avoid ref leaks in the cgo world). No match at
    all → field is left as its Go zero value, field simply skipped (not an
    error).
  - Used keys are tracked (`usedKeys[elem.Key()]`) purely from the exact-match
    key (**not** the fallback case-insensitive key it may have matched under)
    — feeds `,unusedKeys` accounting.
  - **Critical dispatch quirk**: if the destination field's `Kind()` is
    `reflect.Slice`, decode the found element directly
    (`decode(fieldName, elem, field)` — lets `decodeIntoSlice`'s own
    expand-iteration handle duplicates/arrays). Otherwise (struct, string,
    int, bool, map, ptr, ...), it **iterates the found element with
    `expand=false`** and decodes into the *same* destination field once per
    iteration step, letting each duplicate-key occurrence progressively
    overwrite/merge into the one field (this is how
    `TestObjectDecode_nestedStructRepeated` gets `Foo` from the first `value{}`
    block and `Bar` from the second to both land in one `Nested` struct — each
    iteration step recurses into `decodeIntoStruct` again on the *same*
    `result.Value`, additively filling in whichever fields that particular
    block's keys satisfy).
  - Field processing order comes from **iterating a `map[*reflect.StructField]reflect.Value`**
    — Go map iteration order is randomized. **This means the *order* fields
    are decoded in (and thus the order entries land in `,decodedFields`) is
    not deterministic** in the original code, purely coincidental that
    `TestObjectDecode_structKeys` (which asserts `Keys: []string{"Foo", "Bar"}`
    with only two fields) always happens to observe struct-declaration order
    in practice for small field counts — a pure-Go reimplementation of the
    *decoder* should carry this exact map-based iteration forward unchanged
    (per HANDOFF's instruction, `decoder.go` is not to be redesigned), so this
    latent nondeterminism is inherited as-is, not something to "fix."

### Exact `*Object`/`*ObjectIter` methods `decoder.go` calls (the contract the new core must satisfy)

- `(*Object).Type() ObjectType` — pervasive dispatch.
- `(*Object).ToBool() bool`
- `(*Object).ToInt() int64`
- `(*Object).ToString() string`
- `(*Object).Len() uint` — only ever used as a slice/map-capacity **hint**
  (`make([]T, 0, int(o.Len()))`), never for loop bounds or correctness.
- `(*Object).Iterate(expand bool) *ObjectIter` — called with both `true` and
  `false` at different points, see above.
- `(*Object).Key() string`
- `(*Object).Get(key string) *Object`
- `(*Object).Ref() error` — exactly one call site, the `",object"` tag path.
- `(*Object).Close() error` — called on every object obtained via `Get`/
  iteration once done with it (pervasive; in pure Go this is a no-op but the
  call sites remain and must compile/behave harmlessly).
- `(*ObjectIter).Next() *Object`
- `(*ObjectIter).Close()`

**Not called anywhere in `decoder.go`**: `ToFloat()`, `Delete()`, `Emit()`.
These four methods (well, three — `ToFloat` plus `Delete`/`Emit`) are
irrelevant to the decoder's correctness but still must exist on `*Object` for
the package's public surface (used directly by `object_test.go`/user code,
not by the decoder).

---

## 6. Test inventory — every case and what it pins

### `parser_test.go`

| Test | Pins |
|---|---|
| `TestParser` | `NewParser(0)` + `AddString` + `Object()` round-trip: root type is `ObjectTypeObject`, `Get("foo")` finds a `ObjectTypeString` value `"bar"` with `Key()=="foo"`. |
| `TestParserAddFile` | Same as above but via a temp file and `AddFile`. Depends on real filesystem I/O — no C-specific behavior beyond "open and read a file," portable as-is. |
| `TestParserRegisterMacro` | `.foo "bar";` invokes the registered macro synchronously during `AddString`, with the exact unquoted argument `"bar"`. Not C-specific in observable behavior (id-map plumbing is a pure cgo-bridging detail, invisible to the test) — but the *timing* (macro fires during parse, before `AddString` returns) and *exact string decoding* of the macro argument must be preserved. |
| `TestParseString` | Package-level `ParseString` convenience: `obj.Len() == 2` for `"foo = bar; baz = boo;"` — this exercises the **non-quirky** branch of `Len()` (root object, no `next` chain) so it's a plain `len` field check; still requires the parser to correctly count 2 distinct top-level keys. |

### `object_test.go`

| Test | Pins |
|---|---|
| `TestObjectEmit` | Exact JSON emit string, see §2/§4.3 — **byte-exact formatting** (4-space indent, specific quoting/newlines), not just semantic JSON equivalence. |
| `TestObjectEmit_EmitConfig` | Exact Config emit string — same exactness requirement, different format rules. |
| `TestObjectDelete` | `Get` finds `"bar"`, `Delete("bar")`, then `Get` returns `nil`. |
| `TestObjectDelete_unknown` | Deleting an absent key is a silent no-op (no panic/error). |
| `TestObjectGet` | `Get` returns correct `Key()`/`ToString()`. |
| `TestObjectLen_array` | `Len() == 3` for a 3-element array — plain `len` field path. |
| `TestObjectLen_object` | `Len() == 2` for two same-key nested-object blocks — **the quirk path**, see §4.1. This is the load-bearing test for the quirk; it must keep passing exactly as the C binding behaves, not as a "corrected" count. |
| `TestObjectIterate` | Order-sensitive: expects `["foo","bar","bar","baz"]` i.e. iterates `foo` then `bar` **in declaration order**, with `expand=true`. Depends on the object model preserving insertion/declaration order for iteration — a hash-map-based reimplementation without explicit order tracking would likely produce a different (and non-deterministic across runs) order and fail this test. Not "C-specific" per se, but is a property of libucl's storage (ordered hash) that Go's native `map` does **not** give you for free — flag as a must-preserve ordering guarantee. |
| `TestObjectIterate_array` | Same order-sensitivity for a real array: `["foo","bar","baz"]` — arrays are naturally ordered so this is lower-risk, but still pins order. |
| `TestObjectToBool` | Basic bool decode of a `true`/`false` UCL value. |
| `TestObjectToFloat_oneThousand` | `1000` parses to float `1000.0` exactly. |
| `TestObjectToFloat_oneThousandth` | `0.001` parses to float `0.001` exactly — ordinary float parsing, low risk. |
| `TestObjectToFloat_oneThird` | Parses a **53-significant-digit decimal literal** representing 1/3 and expects it to equal Go's own `1.0/3.0` (i.e. the nearest representable `float64`). **This directly pins the parser's number-parsing algorithm to produce the IEEE-754-correctly-rounded result**, matching what C's `strtod` (used internally by libucl) produces for the same literal. A naive/non-correctly-rounded decimal-to-float64 conversion in the Go port could produce an off-by-one-ULP value and silently fail this test. Use Go's standard `strconv.ParseFloat` (which is correctly-rounded) for the actual conversion, not a hand-rolled parser, to guarantee this matches. |
| `TestObjectToFloat_negativeOneThird` | Same as above, negative sign — also exercises negative-number parsing through the same exact literal. |

### `decoder_test.go`

All are pure-Go reflection/decode-shape tests once `*Object`'s `Get`/`Iterate`/
`Type`/`To*`/`Key`/`Len`/`Ref`/`Close` behave per §2/§5 — none of them are
directly C-specific *except* transitively through:
(a) the `Len()`/iteration-order/pointer-identity quirks already flagged, and
(b) `TestObjectDecode_mapStructObject`'s pointer-identity dependency (§4.2,
repeated below since it's the single most C-behavior-coupled test in the
whole suite).

| Test | Pins |
|---|---|
| `TestObjectDecode_basic` | Struct decode of bool/string/int fields, including string-typed source `"42"` decoding into an `int` field, and `bool`-typed field getting `"true"` string decoded into a **string** field (`BoolStr`) — mixed-type coercion. |
| `TestObjectDecode_interface` | `map[string]interface{}` with array/scalar/bool leaves; arrays with mixed string/int elements decode as `[]interface{}` with per-element type guessing. |
| `TestObjectDecode_map` | `map[string]string`, int value `12` coerces to string `"12"`. |
| `TestObjectDecode_mapMulti` | Duplicate-key nested-object blocks merge into one `map[string]string` (`foo{foo=bar}; foo{bar=baz};` on the *inner* object → one flat map) — exercises expand=true inner iteration over the duplicate chain. |
| `TestObjectDecode_mapNonNil` | Pre-populated map entries survive decode untouched alongside newly decoded ones. |
| `TestObjectDecode_mapNonObject` | Decoding an array into a map errors. |
| `TestObjectDecode_mapObject` | Nested object under a scalar-sibling key decodes to `[]map[string]interface{}` (always a slice, even for one occurrence). |
| `TestObjectDecode_mapObjectMultiple` | Same but two `bar{}` blocks → two-element `[]map[string]interface{}`, each block's own key set undisturbed (not merged across blocks, unlike the plain-map case) — because `decodeIntoInterface`'s object branch treats each occurrence as a separate map, whereas `decodeIntoMap`'s struct/map branch merges same-key duplicates field-by-field. This distinction (merge vs. one-map-per-occurrence) is dispatch-path-dependent, not type-dependent — important to replicate precisely. |
| `TestObjectDecode_mapReuseVal` | Cross-block field merge into one map value via a `Struct`-typed map element (`Struct{Foo,Bar}` gets `Foo` from block 1, `Bar` from block 2, same map key `"foo"`). |
| `TestObjectDecode_nestedStruct` | Simple nested struct decode. |
| `TestObjectDecode_nestedStructRepeated` | Nested struct field populated additively across two same-key blocks (the "iterate expand=false, redecode into same field repeatedly" quirk, §5). |
| `TestObjectDecode_slice` | `[]string` from a real array, including int-to-string coercion (`12`→`"12"`). |
| `TestObjectDecode_sliceRepeatedKey` | `[]string{"foo","bar"}` from **duplicate scalar keys** (`foo=foo; foo=bar;`), i.e. the implicit-array chain on a non-object head type, expanded via `Iterate(true)`. |
| `TestObjectDecode_sliceSingle` | A single (non-duplicated, non-array) scalar key still decodes into a one-element slice — `Iterate(true)` on a lone scalar yields exactly that one value. |
| `TestObjectDecode_struct` | Mixed slice + string struct fields. |
| `TestObjectDecode_structKeys` | `,decodedFields` tag — **order-sensitive** (`[]string{"Foo","Bar"}`), riding on the struct's field-processing map-iteration nondeterminism noted in §5 — technically flaky in principle, must be carried over as-is per HANDOFF (don't "fix" `decoder.go`). |
| `TestObjectDecode_mapStructNamed` | `,key` tag sets a field to the object's own key string. |
| `TestObjectDecode_mapStructObject` | **The pointer-identity test** — `,object` tag stores the live `*Object` itself on a struct field, and the test asserts `reflect.DeepEqual` against an independently-`Get()`'d `*Object` for the same logical node. Requires the identity-preservation quirk in §4.2. Also calls `result.Value["foo"].Object.Close()` manually afterward — this must be safe as a no-op double-`Close` scenario in the pure-Go core (the `fooObj` obtained separately in the test is *also* deferred-`Close()`'d — if both wrap the same identity and `Close` had real refcounting semantics, this could double-free; since the port makes `Close` a no-op, it's automatically safe). |
| `TestObjectDecode_structArray` | `[]*Nested` from two same-key object blocks, pointer-slice element allocation. |
| `TestObjectDecode_structSquash` | `,squash` embedded-struct flattening. |
| `TestObjectDecode_structUnusedKeys` | `,unusedKeys` tag collects keys never consumed by any struct field (order: declaration order of the source, `["foo","baz"]`, since `"bar"` was consumed by the `Bar` field). Implicitly also order-sensitive on iteration order (§4.8). |

### Summary of tests coupled to non-obvious C-derived behavior
- `TestObjectLen_object` — the `Len()` quirk (§4.1).
- `TestObjectIterate`, `TestObjectDecode_structUnusedKeys` (and, transitively,
  anything asserting a slice/ordered-map shape built from iteration) —
  declaration-order iteration guarantee (§4.8).
- `TestObjectDecode_mapStructObject` — pointer/reference identity across
  independent lookups of the same node (§4.2). This is the one test that will
  silently pass with "equal-looking but distinct" objects in a naively-copying
  Go implementation and only fail if identity truly diverges structurally in
  a way `reflect.DeepEqual` can detect — worth a dedicated unit test in the
  new core keyed directly to this behavior, not just relying on this ported
  test.
- `TestObjectToFloat_oneThird`/`_negativeOneThird` — correctly-rounded
  decimal-literal-to-`float64` parsing matching C `strtod` (§6 table).
- `TestObjectDecode_structKeys` — inherited nondeterminism from Go map
  iteration in `decoder.go`'s field-collection step; not really "C behavior"
  but flagged since it's a subtle latent risk carried over unchanged.

---

## 7. Refcounting: what's a no-op, and what tests observe it

Methods whose entire purpose in the cgo binding is C memory management:

- `(*Object) Ref() error` — always `C.ucl_object_ref` + return `nil`.
- `(*Object) Close() error` — always `C.ucl_object_unref` + return `nil`.
- `(*ObjectIter) Close()` — always `C.ucl_object_unref` on the iterator's
  captured top object.
- `(*Parser) Close()` — `C.ucl_parser_free` plus purely-Go macro-map cleanup
  (the latter isn't C refcounting but is also cgo-bridging bookkeeping with no
  Go-native equivalent needed).

**Per HANDOFF.md's explicit decision, all four become no-ops in the pure-Go
port** — `Ref`/`Close` (both `Object` and `ObjectIter` variants) keep their
exact signatures (kept "so existing call sites still compile") but do nothing
and cannot fail.

**No test in any of the three `_test.go` files asserts on the *effect* of
refcounting itself** — i.e. nothing checks that memory was actually freed,
that a refcount reached a particular number, or that using an object after
enough `Close()` calls panics/becomes invalid. Every test's `Close()`/`Ref()`
calls are pure hygiene (matching what a correct cgo caller must do to avoid
leaks/UAF in C) and are safe to become inert:
- Tests routinely `defer obj.Close()` immediately after obtaining an object
  and continue using it for the rest of the test body — this only works
  because in the real cgo binding the `defer` runs *after* the test function
  returns, so the object stays valid for the whole test regardless; it isn't
  exercising post-Close behavior.
- The one test that comes closest to "observing" a refcount interaction is
  `TestObjectDecode_mapStructObject` (discussed in §6) — but what it actually
  observes is *pointer/value identity* via `reflect.DeepEqual`, not a
  numeric refcount or free/no-free distinction. It calls `.Ref()` implicitly
  (via the `,object` decoder tag) and `.Close()` twice on effectively "the
  same" object (once via the test's own `fooObj`, once via
  `result.Value["foo"].Object`) — with no-op semantics this is trivially safe;
  with real refcounting it relies on the extra `Ref()` the tag path takes to
  keep the object alive after the decode's own iteration-owned reference is
  `Close()`'d.

**Conclusion: `Ref`/`Close` (all forms) can be implemented as true no-ops
returning `nil`/nothing, with no risk of breaking any ported test**, as
HANDOFF.md already concluded. The only thing that *must* be preserved
alongside making them no-ops is the object-identity behavior of §4.2/§6,
which is unrelated to refcounting itself — it's about the underlying object
graph's node identity being stable across repeated lookups, not about counts.

---

## Appendix: file inventory read for this spec

- `/home/claude/libucl-go/go-libucl/libucl.go`
- `/home/claude/libucl-go/go-libucl/parser.go`
- `/home/claude/libucl-go/go-libucl/object.go`
- `/home/claude/libucl-go/go-libucl/decoder.go`
- `/home/claude/libucl-go/go-libucl/go-libucl.h`
- `/home/claude/libucl-go/go-libucl/Makefile`
- `/home/claude/libucl-go/go-libucl/README.md`
- `/home/claude/libucl-go/go-libucl/decoder_test.go`
- `/home/claude/libucl-go/go-libucl/object_test.go`
- `/home/claude/libucl-go/go-libucl/parser_test.go`
- `/home/claude/libucl-go/libucl/include/ucl.h` (cross-reference for C
  semantics/enum values; specifically `ucl_type_t`, `ucl_emitter_t`,
  `ucl_parser_flags_t`, `ucl_object_s` struct layout, and every `ucl_object_*`/
  `ucl_parser_*` function referenced from the Go binding above)
- `/home/claude/libucl-go/HANDOFF.md` (project goal/context, read first per
  memory instructions)
