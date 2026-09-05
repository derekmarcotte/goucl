# NOTES — csexp (sexp.go)

Status: complete. `sexp.go` implements the canonical S-expression decoder
(`docs/sexp.md`), `sexp_test.go` ports every case in `tests/test_csexp.c` plus
a few extras and a native fuzz target. I own and only touched these two
files; I did not edit `parser.go` or anything else.

## Entry point to wire up

`parser.go:629-632` currently has:

```go
case ParseCSexp:
    // ucl_parse_csexp has no Go port yet; see NOTES-parser.md.
    p.createErr("csexp support is disabled")
    p.errCode = ErrSyntax
    p.state = stateError
    return false
```

Replace that whole branch with a call into a new `addCSexpChunk` helper,
following the exact shape of the existing `addMsgPackChunk` (parser.go:659):

```go
case ParseCSexp:
    return p.addCSexpChunk(data, priority)
```

```go
func (p *Parser) addCSexpChunk(data []byte, priority uint) bool {
    obj, err := DecodeCSexpFull(data, priority)
    if err != nil {
        msg := err.Error()
        if _, rest, found := strings.Cut(msg, ErrCSexp.Error()+": "); found {
            msg = rest
        }
        p.createErr("%s", msg)
        p.errCode = ErrSyntax
        p.state = stateError
        return false
    }
    if p.topObj == nil {
        p.topObj = obj
        p.curObj = obj
        return true
    }
    return p.topObj.Merge(obj, false)
}
```

i.e. `addCSexpChunk` is `addMsgPackChunk` with `DecodeMsgPackFull`/`ErrMsgPack`
swapped for `DecodeCSexpFull`/`ErrCSexp`. `ErrCSexp` and `DecodeCSexpFull` are
both exported from `sexp.go` and ready to use.

Public API surface added: `DecodeCSexp(data []byte) (*Object, error)`,
`DecodeCSexpFull(data []byte, priority uint) (*Object, error)`, and
`ErrCSexp error` (wrapped by every error the decoder returns, exactly as
`ErrMsgPack` is for msgpack).

## Design notes

* The C's `ucl_parse_csexp` is an explicit state machine with a manual
  linked-list stack (`struct ucl_stack`), because it has to be resumable
  chunk-at-a-time. A Go decode of one complete `[]byte` doesn't need that, so
  this port is ordinary recursive descent (`sexpDecoder.parseList` /
  `parseAtom`) instead — much shorter and easier to verify against the spec,
  with the same observable behavior.
* Nesting depth is bounded by an explicit `depth` parameter checked against
  `sexpMaxNesting` (1024, same value as `msgpackMaxNesting`) *before*
  recursing, exactly where the C's `read_obrace` checks it. This is what
  keeps a pathological run of thousands of `(` bytes from recursing without
  bound — verified by `TestCSexpNestingTooDeep` (1024 levels accepts, 1025
  rejects) and by the fuzz target (~9.7M execs, no panics, no timeouts).
* Several of the C's textually-distinct error paths ("value outside of any
  list", "extra closing brace", the NEXT_STATE "extra data" case) all
  collapse onto one check here: after the top-level list closes, the cursor
  must be exactly at end-of-input, or `DecodeCSexpFull` returns an error.
  This was verified case-by-case against the C's state machine (traced by
  hand for every issue #365 regression input) to confirm it produces the
  same success/failure outcome, which is all `docs/sexp.md` requires for
  parity — message text is explicitly not required to match.
* Out-of-bounds-read divergence: N/A here — unlike some other components,
  every read in this decoder is already guarded by an explicit bounds check
  before the slice operation (`parseAtom`'s `uint64(len(d.buf)-d.p) < length`
  guard covers both the truncated-atom case and the int-conversion-safety
  argument in the code comment), so there was no C out-of-bounds-read
  behavior to consciously avoid reproducing. Noting this explicitly per the
  house rule, in case a future reviewer looks for it.
* No zerocopy variant is exposed (matching `DecodeMsgPack`'s precedent):
  atom bytes are always copied into a freshly owned slice via `FromLString`,
  never aliased to the input `[]byte`.

## Test results

`go test ./...`: all pass (194 top-level `--- PASS`, 0 `--- FAIL`), up from
the 180 baseline — this component added tests, broke nothing.
`gofmt -l .`: empty. `go vet ./...`: clean.
`go test -run '^$' -fuzz '^FuzzDecodeCSexp$' -fuzztime=60s .`: ~9.7M execs,
16 workers, zero panics, zero crashers written to testdata/fuzz.
