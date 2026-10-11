# Review pass

A pass over the finished port looking for defect classes rather than reading
line by line. What was checked, and what it found.

## Findings, in the order they were found

| # | Finding | Status |
|---|---|---|
| 1 | `TestBasicGoldens` *skips* a fixture/mode pair with no expectation file, so deleting a golden quietly reduced coverage | Fixed: `TestGoldenCoverage` asserts all 156 expectations present |
| 2 | The parser, the component that reads untrusted input, was the only decoder with no fuzz target | Fixed: `FuzzParseString` |
| 3 | **Panic** on a truncated `\u` escape — index out of range, a denial of service on any malformed config | Fixed here (`findings.md` #9) |
| 4 | `AddFd` filled its buffer with a single `Read`, which may return short: the rest of the document was dropped silently | Fixed: `io.ReadFull`, matching the include path |
| 5 | The schema pattern cache was an unbounded package-level `sync.Map` keyed by pattern strings from the schema | Fixed: bounded at 1024 |
| 6 | Two of the three recorded "out-of-bounds reads" in the C are provably in bounds | Corrected (`findings.md` #10) |

Items 3, 4 and 5 are real defects in this port. Items 1 and 2 are gaps in the
evidence rather than in the code, which is worse in a project whose entire
correctness argument rests on the tests.

## Checked and clean

* **Race detector** clean across the suite.
* **Coverage** 90.6% of statements.
* **Nesting limits** present on every decoder that consumes untrusted input:
  msgpack 1024, csexp 1024, schema depth 1024, parser 65535 objects and 16
  nested includes.
* **Stack exhaustion** — 60,000 levels of nesting survive parse, emit, copy and
  compare. Go's growable stacks absorb it and the parser's own cap bounds it.
* **Allocation** — no `make` sized from anything but a real input length or a
  stat size.
* **Emitter errors** are latched on first failure and every write short
  circuits after it, so a failing `io.Writer` cannot produce partial output
  with a nil error.
* **Package-level mutable state** — only sentinel errors and lookup tables
  built in `init`, plus the now-bounded cache. Nothing lazily initialised, so
  no data race on first use.
* **Nil safety** — nothing exported panics on a nil `*Object`, nil arguments or
  empty input. Pinned by `nilsafety_test.go`, since a nil returned by a lookup
  that missed is an ordinary value here and gets passed straight on.
* **Integer widths** — `length` is `uint32` and truncates above 4 GiB, which is
  exactly what the C's `uint32_t len` does. Faithful, not a defect.
* **Documentation** — every exported symbol has a doc comment.
* **Dead code** — none; no unreferenced unexported function.
* **Unchecked type assertions** — one, on a `sync.Map` that only ever holds the
  asserted type. Safe.

## Deliberately not changed

`AddFd` on a pipe returns success having parsed nothing, because a pipe reports
a size of zero and `ucl_parser_add_fd_full` returns true on `st_size == 0`
before it reaches `mmap`. That is surprising, and it is what the C does.
`TestAddFdOnPipeMatchesC` documents it so it does not get "fixed" into a
divergence later.
