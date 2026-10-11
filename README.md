# goucl — pure-Go UCL (Universal Configuration Language)

`go.marcottesoftware.ca/goucl` is a pure-Go implementation of
[libucl](https://github.com/vstakhov/libucl) v0.9.4, so dependents of the cgo
binding [`github.com/mitchellh/go-libucl`](https://github.com/mitchellh/go-libucl)
can drop the C dependency with an import-path swap.

The source lives at
[github.com/derekmarcotte/goucl](https://github.com/derekmarcotte/goucl); the
import path is a domain this project controls so that the code can move hosts
later without every dependant having to change its imports. `go get` resolves
it the same way either path would.

Three packages:

| | |
|---|---|
| (root) | the Go-shaped API — read a document, walk it, emit it |
| `compat/libucl` | drop-in for `github.com/mitchellh/go-libucl`, package `libucl` |
| `internal/core` | the C-mirror engine the other two are built on |

`compat/libucl` is a **separate module**, so it is opt-in: `go get`ting goucl
does not bring it, and the main module carries none of the third-party code
described under [Licence and attribution](#licence-and-attribution). Its
import path is unchanged. Because it is a separate module, `go test ./...` at
the root does not reach it — use `tools/test-all.sh` to run everything.

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

## Licence and attribution

This project is a port of libucl and is distributed under libucl's own
BSD-2-clause terms, copyright (c) 2013-2015 Vsevolod Stakhov with contributions
by Allan Jude. No additional copyright is asserted over the translation. See
`LICENSE` for the terms and `NOTICE` for full provenance.

`internal/core` was produced by automated translation of libucl's C sources by
a large language model, function by function; its correctness rests on
differential testing against the compiled C library rather than on the
translation itself. `internal/core/list.go` additionally translates the
utlist.h list macros, copyright (c) 2007-2022 Troy D. Hanson. Test fixtures
under `internal/core/testdata/` are copied verbatim from the libucl
distribution.

`compat/libucl` reproduces the API of Mitchell Hashimoto's
github.com/mitchellh/go-libucl and carries four files from it. **That module is
not under the licence above.** The repository it draws from states no licence,
it is archived, and a request to its author for clarification went unanswered,
so this project cannot place those files under terms of its own; the
attribution in `NOTICE` is credit rather than a licence grant. Its terms are
whatever the original library's are — unstated — as `compat/libucl/LICENSE`
sets out. Read that before depending on or redistributing that package.
`USAGE.md` shows how to move off it onto the root module, which carries none of
those files.
