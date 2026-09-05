# goucl — pure-Go UCL (Universal Configuration Language)

Package `goucl` is a pure-Go implementation of [libucl](https://github.com/vstakhov/libucl) v0.9.4,
intended as a drop-in replacement for the cgo binding
[`github.com/mitchellh/go-libucl`](https://github.com/mitchellh/go-libucl) — so
dependents can drop the C dependency with an import-path swap.

Status: **in progress.** See `docs/` for the ported-behaviour specs.

## Scope

Full port: parser (UCL + JSON), object model, the config/JSON/JSON-compact/YAML
emitters, includes, macros and variable expansion, plus JSON Schema draft-4
validation, MessagePack read/write, and canonical S-expressions.

## Correctness approach

Correctness is proven against the C implementation, not asserted:

* `testdata/basic/` is a verbatim copy of libucl's own fixtures.
* `tools/gen-goldens.sh` regenerates emitter goldens from the compiled C
  library. Upstream only diffs the *config* emitter and smoke-tests the rest;
  we pin all four emitters against C-produced output.
* The upstream C test drivers are re-expressed as Go tests over the same data.
* mitchellh/go-libucl's own test suite is run unmodified against this package
  via a compatibility shim, as a drop-in proof.

Regenerating goldens needs the reference checkout built:

```sh
cmake -B ../libucl/build ../libucl && cmake --build ../libucl/build
./tools/gen-goldens.sh
```

## Licence

This project is a port of libucl and is distributed under the same BSD-2-clause
terms; see `NOTICE-libucl`. Test fixtures under `testdata/` are copied verbatim
from the libucl distribution.
