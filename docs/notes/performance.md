# Performance against the C

Measured, not estimated. `tools/run-bench.sh` reproduces everything below.

## How it is measured

Upstream ships `tests/test_speed.c`, but it times a single parse and a single
emit of one large file with no iteration and no statistics, so it cannot
produce a stable per-operation figure. The harness here is separate:

* `tools/bench-c.c` loops N times per operation and reports ns/op.
* `internal/core/bench_test.go` measures the same operations on the same
  fixtures through the standard Go benchmark harness.
* `tools/run-bench.sh` builds and runs both and prints the comparison.

The two sides are kept symmetrical deliberately, because the easy mistakes all
flatter one of them:

* Parsing builds a fresh parser and a fresh tree every iteration on **both**
  sides. Reusing either would amortise setup away on whichever side allows it.
* Emitting parses once and re-emits the same tree on both sides, and both
  materialise a buffer, because the C entry point always returns one.
* The C parser is created **without** zero-copy, matching `AddChunk`, which
  does not borrow the caller's bytes either. Zero-copy would measure a
  different operation.

Fixtures are the smallest, a middling one and the largest self-contained
document in the corpus. All three are free of includes, so nothing depends on
the working directory or touches the filesystem mid-parse.

## Results

Intel Xeon E5-2650 v2, 200000 iterations per C operation, median of 5 Go
samples. The machine is shared, so treat differences under about 15% as noise.

| Operation | Fixture | C ns/op | Go ns/op | Go/C | Go B/op | allocs |
|---|---|---:|---:|---:|---:|---:|
| parse | 1.in (187 B) | 10549 | 36890 | 3.50x | 4496 | 65 |
| parse | 2.in (490 B) | 22670 | 68561 | 3.02x | 11048 | 181 |
| parse | 4.in (1615 B) | 35752 | 89112 | 2.49x | 14104 | 214 |
| emit JSON | 1.in | 2269 | 6835 | 3.01x | 4488 | 8 |
| emit JSON | 2.in | 7137 | 16093 | 2.25x | 5144 | 9 |
| emit JSON | 4.in | 12356 | 23186 | 1.88x | 6264 | 5 |
| emit JSON compact | 1.in | 1858 | 5672 | 3.05x | 4360 | 8 |
| emit JSON compact | 2.in | 6175 | 12422 | 2.01x | 4696 | 9 |
| emit JSON compact | 4.in | 11758 | 20529 | 1.75x | 6008 | 5 |
| emit config | 1.in | 2244 | 6727 | 3.00x | 4440 | 8 |
| emit config | 2.in | 5966 | 12415 | 2.08x | 4888 | 9 |
| emit config | 4.in | 6519 | 13928 | 2.14x | 6008 | 5 |

## Reading it

Between 1.75x and 3.5x of the C, tightening as documents grow, because
per-call fixed cost stops dominating. For a configuration library this is a
comfortable place to be: config is read at startup, not in a hot loop, and a
1600-byte document parses in under 90 microseconds.

Parsing is the weaker half, and allocation is the visible reason: one
allocation per roughly eight input bytes, with no pooling. Emitting is closer
to parity and its allocation count is flat at 5 to 9 regardless of input size,
because it is dominated by growing one output buffer rather than by per-node
work.

**No optimisation has been attempted.** The allocation profile is an
observation from `-benchmem`, not a conclusion from a CPU or heap profile, and
the specific call sites have not been confirmed. If parsing ever needs to be
faster, that is where to look first, and profiling should come before any
change.

## One caveat on the comparison

The reference build carries the three fixes this port made to its libucl
checkout, so
these numbers compare against a *patched* C rather than against released
0.9.4. That is the right baseline for correctness, since it is the build the
goldens come from, and the fixes are small and not in any hot path -- but the
numbers should not be quoted as "against upstream 0.9.4" without saying so.

## A data-oriented pass was tried and rejected

Worth reading before proposing one, because the obvious version of this idea
looks much better than it measures.

`sizeof(Object)` is **184 bytes. The C's `ucl_object_t` is 64** -- exactly one
cache line. The C's `value` is an 8-byte union; Go has no unions, so our eight
value fields sit side by side and a parsed integer carries 184 bytes to hold 8.
That is a 2.9x blowup per node, it lines up suspiciously well with the 2.5-3.5x
parse ratio, and it invites the conclusion that layout is the problem.

It is not. Three changes were prototyped and measured:

1. Box the userdata trio and the render cache behind pointers, neither of which
   a parsed document ever populates: **184 -> 136 bytes**, a 26% smaller node.
2. Registration of the six built-in macros: six pointer allocations per parser
   became one slice.
3. The hash lookup written inline so the compiler elides the string conversion
   it cannot elide through a helper.

All three together, with the whole suite passing including the 168 goldens:

| | Memory | Time |
|---|---|---|
| Small fixtures (187 B - 1.6 KB) | -13 to -16% | ~4-6%, inside the noise |
| Upstream's 19 MB speed-test document | -14% (186 MB -> 161 MB) | ~5% |

**Roughly 5%, at a 19 MB working set where cache locality should dominate.**

The reason is in the allocation count, which barely moved: 2.7 million
allocations for that one document, averaging 69 bytes each. Node size scales
what each allocation costs; it removes none of them. The real lever would be
slab-allocating nodes into `[]Object` chunks -- they are never individually
freed, so it fits -- which would remove roughly a fifth of the allocations and
deliver actual contiguity. That is a rewrite of how the engine allocates, not a
tidying pass.

Rejected on the trade: 5% does not pay for the fidelity. Boxing fields means
`types.go` no longer reads field-for-field against `ucl_object_t`, and that
correspondence is what makes a future upstream change portable and reviewable.
The engine is measured at 1.75-3.5x of a mature C library, configuration is
read at startup, and no workload has complained.

Reconsider only if one does, and start by profiling allocation count rather
than by shrinking the struct.
