# goucl — pure-Go UCL (Universal Configuration Language)

`github.com/derekmarcotte/goucl` is a pure-Go implementation of
[libucl](https://github.com/vstakhov/libucl) v0.9.4, so dependents of the cgo
binding [`github.com/mitchellh/go-libucl`](https://github.com/mitchellh/go-libucl)
can drop the C dependency with an import-path swap.

Three packages:

| | |
|---|---|
| (root) | the idiomatic Go API — read a document, walk it, emit it |
| `compat/libucl` | drop-in for `github.com/mitchellh/go-libucl`, package `libucl` |
| `internal/core` | the C-mirror engine the other two are built on |

```go
doc, err := goucl.ParseFile("app.conf")
host, ok := doc.String("server.host")
port, ok := doc.Int("server.port")

for key, v := range doc.All() {
	// ...
}
```

The root package can parse, navigate and emit, with comments preserved through
a config round trip and structured errors carrying a position and a source
excerpt. It also decodes and encodes Go values directly — `Marshal`,
`Unmarshal`, `Encoder`, `Decoder`, and options such as `WithVariable`,
`WithMacro` and `DisallowUnknownFields` — so a struct can be the shape a
document is read into rather than a tree walked by hand. See
[`USAGE.md`](USAGE.md) for how, and [`DESIGN.md`](DESIGN.md) for what is
settled and what is still intent. Callers who need generic accessors, tree
construction, or the full engine behaviour today have `compat/libucl`.

Status: the port is **feature complete** and proven against the C. See
[`docs/`](docs/) for the decision record, implementation notes and extracted C
behaviour.

## Scope

Full port: parser (UCL + JSON), object model, the config/JSON/JSON-compact/YAML
emitters, includes, macros and variable expansion, plus JSON Schema draft-4
validation, MessagePack read/write, and canonical S-expressions.

## Correctness approach

Correctness is proven against the C implementation, not asserted:

* `internal/core/testdata/basic/` holds libucl's own fixtures, copied
  verbatim, plus two this project contributed back upstream.
* `tools/gen-goldens.sh` regenerates goldens from the compiled C library.
  Upstream diffs only the *config* emitter and sends the other five modes to
  /dev/null without checking their exit status; we pin all six against
  C-produced output — 168 goldens rather than 25.
* The upstream C test drivers are re-expressed as Go tests over the same data.
* mitchellh/go-libucl's own test suite is run against this package from its
  own files, three byte-identical to upstream and the fourth differing only by
  the markers disabling a test that is broken upstream too
  via a compatibility shim, as a drop-in proof.
* The parser, the MessagePack decoder and the S-expression decoder each have a
  fuzz target. The parser's found a real crash on its first run.

### Regenerating goldens

The reference build must carry the three fixes this project reported upstream,
or the goldens will not match: without them libucl mis-parses a comment at the
start of an array, rejects every document containing a macro under
`DISABLE_MACRO`, and reads past the value on a truncated `\u` escape. See
`docs/findings.md` items 2, 3 and 9. The branches live in the sibling checkout:

```sh
cd ../libucl
git switch -c all-fixes master
git merge fix-array-leading-comment fix-disable-macro fix-truncated-unicode-escape
cmake -B build . && cmake --build build
cd ../goucl && ./tools/gen-goldens.sh
```

Once those land upstream, plain `master` will do.

## Licence

This project is a port of libucl and is distributed under the same BSD-2-clause
terms; see `NOTICE-libucl`. Test fixtures under `internal/core/testdata/` are
copied verbatim from the libucl distribution.
