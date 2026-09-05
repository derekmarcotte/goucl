# Porting specs

Implementation specs extracted from the libucl C sources (v0.9.4, `../libucl`)
by a reading pass over each component. They exist so the Go implementation can
be written — and reviewed — without re-reading the C each time, and so that
behaviour we must match is written down explicitly rather than rediscovered.

| Spec | Covers | C sources |
|---|---|---|
| `object-model.md` | object type, flags, priorities, ordered hash, conversions | `ucl_util.c` (object half), `ucl_hash.c` |
| `parser.md` | state machine, lexing, macros, includes, variables | `ucl_parser.c`, `ucl_internal.h`, `ucl_chartable.h` |
| `emitter.md` | config/JSON/compact/YAML output, number & string formatting | `ucl_emitter*.c` |
| `schema.md` | JSON Schema draft-4 validation | `ucl_schema.c` |
| `msgpack.md` | MessagePack encode/decode | `ucl_msgpack.c` |
| `sexp.md` | canonical S-expressions | `ucl_sexp.c` |
| `tests.md` | the 10 upstream test drivers and their fixtures | `tests/` |
| `compat.md` | the mitchellh/go-libucl API we must reproduce | `../go-libucl` |

These are descriptions of observed C behaviour, not authority: where a spec and
the C oracle disagree, the oracle wins and the spec gets corrected.
