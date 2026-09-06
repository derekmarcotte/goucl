# Drop-in plan for github.com/mitchellh/go-libucl

How the compatibility branch is put together, and what it requires of this
package. Derived from `compat.md` (the full API inventory).

## The constraint that decides the design

`decoder.go` in go-libucl must be carried over **byte-for-byte** — it is
already pure Go, and leaving it untouched is what makes its tests meaningful.
It calls `*Object` methods and chains on their results:

```go
obj.Get(key).Type()
iter := obj.Iterate(true); for v := iter.Next(); ...
```

So `Get` must return something with the full `*Object` method set, and
`Iterate(bool).Next()` must too.

That rules out a wrapper:

```go
type Object struct{ *goucl.Object }   // NO
```

`goucl.Object.Get` would return `*goucl.Object`, not `*libucl.Object`, and
`decoder.go` would stop compiling. Wrapping every return value means rewriting
`decoder.go`, which defeats the purpose.

### Correction: pure aliases do not compile either

This document originally concluded "therefore the shim is pure type aliases".
That was wrong, and the compile error is unavoidable rather than a detail of
how it is written. `decoder.go` line 13 declares

```go
func (o *Object) Decode(v interface{}) error
```

and Go forbids defining a method on a non-local type. An alias *is* the
non-local type, so the shim fails with:

```
cannot define new methods on non-local type Object
```

Wrapping fails for the reason given above; aliasing fails for this one. The
answer is the third option: **defined types over the goucl types**, which are
local (so `decoder.go` may hang `Decode` on them) while sharing an identical
underlying struct.

```go
type Object goucl.CompatObject          // defined type, not an alias
type ObjectIter goucl.CompatObjectIter
type Parser goucl.CompatParser

type ObjectType = goucl.Type            // these stay plain aliases
type Emitter = goucl.Emitter
type ParserFlag = goucl.ParserFlag
type MacroFunc = goucl.MacroFunc
```

Methods are one-line forwarders, and the conversions between `*Object` and
`*goucl.CompatObject` are free pointer conversions because the underlying
structs are identical. That matters for more than speed: `*libucl.Object`
remains *the same node*, so no lookup allocates a wrapper and calling `Get`
twice returns the identical pointer — which
`TestObjectDecode_mapStructObject` actually observes.

The chaining constraint that ruled out wrappers is still satisfied, because
the forwarders return the local `*Object` type.

### Why the goucl side needs `Compat`-prefixed types

Four names on `goucl.Object`/`goucl.Parser` were already taken by the C-mirror
API with different signatures — `Object.Key() []byte`,
`Parser.AddString/AddFile(...) bool`, and
`Parser.RegisterMacro(string, MacroHandler) bool`. Rather than change files it
did not own, the compat layer defines `CompatObject`, `CompatObjectIter` and
`CompatParser` over the real types and hangs the mitchellh method set on those.

`Len()` reads the unexported length field directly, which is legal because
`compat.go` lives in package `goucl`. No frozen file needed changing.

To retire the `Compat` prefix later, those four names are what must move.

## What goucl must therefore export

Method names on `*Object`, exactly:

    Close() error        Emit(Emitter) (string, error)   Delete(string)
    Get(string) *Object  Iterate(bool) *ObjectIter       Key() string
    Len() uint           Ref() error                     Type() ObjectType
    ToBool() bool        ToInt() int64                   ToFloat() float64
    ToString() string

`*ObjectIter`: `Close()` — **no return value**, unlike `Object.Close() error`.
That asymmetry is part of the call-site contract; preserve it.

`*Parser`: `AddString(string) error`, `AddFile(string) error`, `Close()`,
`Object() *Object`, `RegisterMacro(string, MacroFunc)`.
Package level: `ParseString(string) (*Object, error)`, `NewParser(ParserFlag) *Parser`.

Constant *values* must match the C enums, which `types.go` already pins:
`ObjectType` follows `ucl_type_t`, `Emitter` follows `ucl_emitter_t`.

## Notes for whoever implements this

* `(*Object).Emit` is deliberately unclaimed. The emitter package exposes
  `EmitText*`; the compat `Emit` is the dispatcher that routes the four text
  formats there and `EmitMsgPack` to the msgpack encoder. Per `compat.md` §2 it
  must **never surface an error**, so swallow `ErrEmitFormat` and return
  `("", nil)`.
* `Ref()`/`Close()` exist only to manage C memory and become no-ops returning
  `nil` — but `Ref` on an object flagged `FlagEphemeral` still deep-copies
  (see `object-model.md` §7.4). Check whether any go-libucl test observes this.
* `Len()` has a quirk to preserve: for an object with a non-nil `Next`
  (implicit array) it counts by iteration rather than reading the length field.
* Aliases mean `goucl` owns these names. Keep the C-mirror API (`ToStringSafe`,
  `LookupPath`, ...) alongside them; the two sets coexist.

## Branch mechanics

In `../go-libucl` (a git repo at `f9d0e55`):

1. `git switch -c pure-go`
2. Delete the cgo files: `parser.go`, `object.go`, `libucl.go`, `go-libucl.h`.
3. Keep `decoder.go`, `decoder_test.go`, `object_test.go`, `parser_test.go`
   **unmodified**.
4. Add `compat.go` with the defined types and forwarders above.
5. Add `go.mod`:

   ```
   module github.com/mitchellh/go-libucl
   go 1.21
   require example.com/goucl v0.0.0
   replace example.com/goucl => ../goucl
   ```

6. `go test ./...` — mitchellh's own suite, unmodified, against pure Go.

Flag any test that pins C-specific behaviour (pointer identity, use-after-free,
refcounting) rather than semantics; those are the legitimate exceptions.

## Outcome

Built on branch `pure-go` of `../go-libucl` (commit `6ebd62b`).

**39 of 39 of mitchellh's tests pass, with their four files byte-identical to
`f9d0e55`** — verified with `git diff f9d0e55 -- decoder.go decoder_test.go
object_test.go parser_test.go`, which is empty. The package also builds under
`CGO_ENABLED=0`, which is the point of the exercise.

Two things left deliberately untouched, both pre-existing at `f9d0e55`:

* `TestObjectDecode_structKeys` flakes roughly one run in eight.
  `decoder.go:287` builds a `map[*reflect.StructField]reflect.Value` and
  appends `decodedFields` while ranging it, so the order is randomised while
  the test asserts `["Foo","Bar"]`. It is mitchellh's bug and would flake
  identically against the cgo binding. Reproduce with
  `go test -run TestObjectDecode_structKeys -count=30`.
* `go vet` reports `decoder.go:41:2: unreachable code`. Not in the subset
  `go test` runs, so the suite is unaffected. Nothing is reported against
  `compat.go`.

`Makefile`, `.travis.yml` and `README.md` still describe the cgo build.

## Addendum: the shim moved into this module

The plan above was executed in `../go-libucl` on branch `pure-go`, and the
`go.mod` quoted in step 5 is the one written *there*. It is left as written
because it records what was actually done.

The shim now lives here, at `compat/libucl`, as part of the restructure into
three packages. What changed and what did not:

* `compat.go` is ours, so its import moved from `example.com/goucl` to
  `github.com/derekmarcotte/goucl/internal/core`, and the `goucl.` qualifiers
  with it. Nothing else in it changed.
* **mitchellh's four files are still byte-identical to `f9d0e55`.** That is
  the whole proof, so it is re-verified rather than assumed:

  ```sh
  cd ../go-libucl
  for f in decoder.go decoder_test.go object_test.go parser_test.go; do
      git show f9d0e55:$f | cmp - ../goucl/compat/libucl/$f
  done
  ```

* Both pre-existing quirks carried across unchanged, as they must: `go vet`
  still reports `decoder.go:41:2: unreachable code`, and
  `TestObjectDecode_structKeys` still flakes about one run in eight. Neither
  is fixable without editing a file whose value is that it is unedited.

`../go-libucl` is untouched and stays as the provenance record. Its `pure-go`
branch no longer builds, because its `replace` points at a module path that no
longer exists — it is superseded by `compat/libucl`, and re-pointing its
`go.mod` would be the only change needed if it were ever wanted again.
