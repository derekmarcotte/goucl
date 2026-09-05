# Emitter notes

Notes from porting the four text emitters (`emit.go`, `emit_stream.go`).
Nothing here asks for a change to the frozen core.

## 1. Public naming: the C's `ucl_object_emit` name is already taken

`types.go` defines `EmitJSON`, `EmitJSONCompact`, `EmitConfig`, `EmitYAML` and
`EmitMsgPack` as `Emitter` **constants**, so those names cannot also be
functions. The entry points are therefore:

```go
func EmitText(obj *Object, format Emitter) ([]byte, error)        // ucl_object_emit
func EmitTextString(obj *Object, format Emitter) (string, error)
func EmitTextTo(w io.Writer, obj *Object, format Emitter) error
func EmitTextFull(w io.Writer, obj *Object, format Emitter, comments EmitComments) error
                                                                   // ucl_object_emit_full
```

They are deliberately scoped to *text* so that the name `EmitObject` (or a
`(*Object) Emit` method, which `docs/compat.md` requires for the
`go-libucl`-compatible layer) stays free for whoever assembles the full
dispatcher. That dispatcher should route the four text formats to
`EmitTextFull` and `EmitMsgPack` to the msgpack emitter; `EmitText*` returns
`ErrEmitFormat` for anything it does not handle.

Per `docs/compat.md` §2 the compatibility `Emit` must never surface an error,
so the wrapper should swallow `ErrEmitFormat` and return `("", nil)`.

## 2. `docs/emitter.md` §2.2 is incomplete about YAML separators

§2.2 says the leading separator for JSON/YAML is `,` / `,\n` / (YAML at
indent 0) a bare `\n`. That holds for `ucl_emitter_common_elt`,
`_start_object` and `_start_array`, but **not** for the duplicate-key branch
inside `ucl_emitter_common_start_object`, which writes its own separator and
has no YAML special case at all:

```c
if (cur->next != NULL) {
    if (!first_key) { append(compact ? "," : ",\n"); }
    ucl_add_tabs(...);
    ucl_emitter_common_start_array(ctx, cur, true, true, compact);
```

So a duplicate-key group that is not the first top-level key under YAML is
preceded by `,\n`, not by a bare newline — producing e.g.

```yaml
first: 0,
dup: [
    1,
    2
]
```

Confirmed against the reference build. `TestEmitYAMLDuplicateChainSeparator`
pins it.

## 3. String length comes from `len(sv)`, not `Object.length`

The C reads `obj->value.sv` with `obj->len`. This port uses `len(o.sv)`
everywhere instead (values, keys, comment text, `ucl_maybe_long_string`'s
80-byte test). `core_test.go` already builds string objects that set `sv`
without setting `length`, so deriving the length is both safer and the more
idiomatic choice. If the object API ever makes `length` and `len(sv)` diverge
for a string on purpose, this needs revisiting.

## 4. `UCL_USERDATA` with an emitter returning NULL

The C substitutes `"null"` when a userdata emitter callback returns `NULL`.
`Object.udEmitter` is `func(any) string`, which has no nil, so that case is
unrepresentable; a nil callback yields `""` (the C's initial `ud_out`), which
is the reachable behaviour. Confirmed against the reference build.

## 5. Out-of-int32 doubles

`emitDouble` pins the out-of-range case to the six-decimal branch rather than
emulating x86-64's `cvttsd2si` saturation, exactly as `docs/emitter.md` §3
recommends. The two are provably equivalent for every `float64`: the gap
between adjacent doubles near ±2^31 is ~4.8e-7, which is larger than the
1e-7 closeness delta, so no out-of-range value can reach branch B either way.
