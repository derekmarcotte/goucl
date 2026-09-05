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

**Therefore the shim is pure type aliases**, and `goucl.Object` must itself
expose the mitchellh method set:

```go
package libucl

import "example.com/goucl"

type Object = goucl.Object          // alias, not a definition
type ObjectIter = goucl.ObjectIter
type Parser = goucl.Parser
type ObjectType = goucl.Type
type Emitter = goucl.Emitter
type ParserFlag = goucl.ParserFlag
type MacroFunc = goucl.MacroFunc
```

With aliases, `goucl.Object.Get` returning `*goucl.Object` *is* returning
`*libucl.Object`. The chain type-checks and `decoder.go` needs no edits.

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
4. Add `compat.go` with the aliases above.
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
