# NOTES: the go-libucl compatibility layer

What was built, what had to diverge from `docs/notes/compat-plan.md`, and what is
left failing on purpose.

Files: `compat.go` and `compat_test.go` here; `compat.go` and `go.mod` on the
`pure-go` branch of `../go-libucl`.

## Result

`github.com/mitchellh/go-libucl`'s own suite — `decoder_test.go`,
`object_test.go`, `parser_test.go`, all byte-for-byte unmodified, decoding
through their unmodified `decoder.go` — builds and passes against pure Go:
**39 of 39 tests pass**, with one inherited flake described at the bottom.

## Divergence 1: the plan's pure-alias shim cannot compile

`docs/notes/compat-plan.md` calls for `type Object = goucl.Object` in the shim.
That is impossible, independently of anything this package exports:
`decoder.go` declares

```go
func (o *Object) Decode(v interface{}) error
```

and Go forbids declaring a method on a non-local type, which an alias to
another package's type is:

    vet: ./decoder.go:13:10: cannot define new methods on non-local type Object

So `Object`, `ObjectIter` and `Parser` in the shim are **defined types** over
their goucl counterparts (`type Object goucl.CompatObject`), with one-line
forwarders. This keeps everything the plan wanted from aliases:

* `Get` returns `*libucl.Object`, `Iterate(...).Next()` returns
  `*libucl.Object`, so `decoder.go`'s chained calls type-check unchanged;
* the conversions are free pointer conversions between identical underlying
  structs, so a `*libucl.Object` **is** the goucl node — no allocation, and
  node identity survives, which is what `TestObjectDecode_mapStructObject`
  observes through `reflect.DeepEqual` (section 4.2 of `compat.md`).

A wrapper struct (`type Object struct{ *goucl.Object }`) would also have
compiled, but it allocates on every lookup and only satisfies that identity
test by accident of `DeepEqual` short-circuiting on the embedded pointer.

`ObjectType`, `Emitter`, `ParserFlag` and `MacroFunc` need no methods and stay
plain aliases.

## Divergence 2: four names in this package were already taken

The plan asked for the mitchellh method set on `goucl.Object` and
`goucl.Parser` themselves. Four of those names already exist in the C-mirror
API with incompatible signatures, in files this task does not own
(`object.go`, `parser.go`):

| C-mirror API | mitchellh needs |
|---|---|
| `(*Object).Key() []byte` | `Key() string` |
| `(*Parser).AddString(string) bool` | `AddString(string) error` |
| `(*Parser).AddFile(string) bool` | `AddFile(string) error` |
| `(*Parser).RegisterMacro(string, MacroHandler) bool` | `RegisterMacro(string, MacroFunc)` |

Rather than edit files owned by other agents, `compat.go` introduces
`CompatObject`, `CompatObjectIter` and `CompatParser` — again defined types
over `Object`, `ObjectIter`-equivalent state and `Parser`, so conversion is
free and identity is preserved — plus `MacroFunc`, `NewCompatParser` and
`CompatParseString`. The two API sets then coexist, exactly as the plan wanted
`ToStringSafe`/`LookupPath` to coexist with the compatibility names.

**No change is requested in any frozen file.** `Len()` needs the tracked
`length` field, and `compat.go` is in package `goucl`, so it reads it directly;
no accessor had to be added. If a future owner of `object.go`/`parser.go` ever
wants the compatibility methods to live directly on `Object` and `Parser`, the
four names above are what would have to be renamed first (`KeyBytes`,
`AddStringOK`, ...), and `CompatObject`/`CompatParser` could then be retired.

## Behaviour notes

* **`Emit` never returns an error.** The four text formats go to
  `EmitTextFull`, `EmitMsgPack` goes to `EncodeMsgPackTo`, and any failure —
  `ErrEmitFormat` for an out-of-range emitter included — is swallowed and
  reported as `("", nil)`, reproducing the cgo binding, which turned a NULL
  from `ucl_object_emit` into an empty string and a nil error.
* **`Ref`/`Close`/`Parser.Close`/`ObjectIter.Close` are inert**, so double
  Close and use-after-Close are safe. `CompatObject.Ref` still calls
  `goucl.Ref`, so an ephemeral object is deep copied (`object-model.md` 7.4);
  the copy is discarded, precisely as the cgo binding discarded the pointer
  `ucl_object_ref` returned. No go-libucl test observes this either way.
* **`Get` does not call `Ref`** although the cgo binding did. With no
  refcounts the call is a no-op, and on an ephemeral object the C's deep copy
  was discarded there too, so the two are observably identical.
* **`Len`'s quirk is preserved.** An object with a non-nil `Next` counts by
  iterating its implicit-array chain; everything else reports the raw tracked
  length. The latent other half — a *scalar* chain head is not special-cased,
  so `foo = foo; foo = bar;` reports 3, the head string's byte length, not 2 —
  is pinned by `TestCompatLen/repeated_scalar`, since no mitchellh test does.
* **A failed `AddString`/`AddFile` always returns a non-nil error**, even if
  the parser recorded no message: `errors.New(C.GoString(NULL))` was
  `errors.New("")`, which is non-nil, and callers only test `err != nil`.

## Left failing on purpose

### `TestObjectDecode_structKeys` is a 1-in-8 flake, inherited

It passes on a normal run and fails roughly 12.5% of the time:

```
--- FAIL: TestObjectDecode_structKeys
    decoder_test.go:386: bad: ... Keys:[]string{"Bar", "Foo"}
```

`decodeIntoStruct` collects fields into a `map[*reflect.StructField]reflect.Value`
and decodes in Go map iteration order, so the `,decodedFields` list it
produces is randomly ordered; the test asserts `[]string{"Foo", "Bar"}`.
Nothing about the object implementation is involved — a standalone program
that builds the same map from the same struct and does nothing else reverses
the order in 12,545 of 100,000 runs (12.5%, i.e. one slot in eight, matching
4 failures in 30 `-count` runs of the real suite). `compat.md` section 5 flagged
this in advance and `HANDOFF.md` forbids redesigning `decoder.go`, so both the
decoder and the test are left exactly as they are. The cgo binding had the
same flake for the same reason.

### `go vet ./...` cannot be clean in `../go-libucl`

```
decoder.go:41:2: unreachable code
```

That is mitchellh's `return nil` after an exhaustive `switch` in `decode`,
pre-existing at f9d0e55 and in a file that must stay unmodified. `go test`
runs a narrower vet subset that does not include `unreachable`, so the suite is
unaffected; `go vet` on `compat.go` alone is clean, as is `go vet ./...` here.

### Stale build files on the branch

`Makefile`, `.travis.yml` and `README.md` still describe the cgo build
(vendoring and compiling C libucl before `go test`). Nothing in the Go build
consults them, and rewriting them was outside this task, so they were left
untouched along with everything else that is not one of the four deleted cgo
files.
