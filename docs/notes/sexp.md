# NOTES — csexp (sexp.go)

Status: complete. `sexp.go` implements the canonical S-expression decoder
(`docs/reference/sexp.md`), `sexp_test.go` ports every case in `tests/test_csexp.c` plus
a few extras and a native fuzz target. I own and only touched these two
files; I did not edit `parser.go` or anything else.

## Entry point

`DecodeCSexp(data []byte) (*Object, error)` and `DecodeCSexpFull(data, priority)`.

`AddChunkFull`'s `ParseCSexp` arm dispatches to `addCSexpChunk`, which has the
same shape as `addMsgPackChunk`: neither format is driven through the UCL state
machine, both are decoded whole and merged into the parser's top object. That
seam has its own tests in `parser_csexp_test.go`, because a correct decoder
sitting behind an unwired dispatch arm still leaves `ucl_parser_add_chunk_full`
broken for the format, and the decoder's own tests would not notice.

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
  same success/failure outcome, which is all `docs/reference/sexp.md` requires for
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
