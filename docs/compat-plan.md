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
