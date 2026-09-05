# Test harness inventory (`libucl/tests/`)

Scope note: `test_msgpack.c`, `test_csexp.c`, `test_schema.c` are covered in
depth by other agents' specs; they're included here only at inventory level
(what they do / flags / assertions) for completeness of the harness picture.

## 0. How the harness is wired

- `tests/CMakeLists.txt` registers 10 test binaries via a macro
  `add_ucl_test(testname sourcefile wrapper)`: builds `sourcefile` linked
  against the `ucl` library, and (non-Windows only) registers a CTest entry
  that runs `tests/<wrapper>` (a shell script) with three env vars set:
  `TEST_DIR=<src>/tests`, `TEST_OUT_DIR=<build>/tests`,
  `TEST_BINARY_DIR=<build>/tests`. `test_strnstr` is the one exception:
  it's built and registered directly as a CTest command (no `.test`
  wrapper, no env vars needed) since it takes no file arguments at all.
  `test_basic`, `test_speed`, `test_schema` are POSIX-only (guarded by
  `IF(NOT CMAKE_SYSTEM_NAME STREQUAL "Windows")`) because they use
  fd/mmap/STDIN_FILENO APIs.
- `tests/run_tests.sh` is a **legacy/alternate** driver (pre-dates the
  per-binary `.test` + CTest wiring): given a test binary path as `$1`, it
  loops `${TEST_DIR}/*.in` (not `basic/*.in` — note the path differs from
  `basic.test`, so as shipped this old script would actually look for `.in`
  files directly under `tests/`, which don't exist there anymore now that
  they live in `tests/basic/`) and diffs against `.res`; `$2`/`$3`/`$4`
  optionally run the speed/generate/schema binaries too. **This script is
  not referenced by CMakeLists.txt or any `.test` file and appears dead** —
  the actual harness is the individual `.test` wrappers below. Do not port
  it; port the `.test` wrappers instead.
- Every `.test` file is a `#!/bin/sh` script that `cd`s nowhere itself
  (relies on the env vars for all paths) and is executed by CTest with
  cwd = build dir.

## 1. The 10 test binaries

### test_basic (`test_basic.c`, 303 lines) — wrapper `basic.test`

**What it does:** a CLI harness around one parse + one emit. `main` parses
flags with `getopt(argc, argv, "fjcyCMm")`, then reads exactly one input
(`argv[0]` after getopt, or stdin) and writes exactly one output
(`argv[1]`, or stdout).

**Flags:**
- `-j` emit JSON (`UCL_EMIT_JSON`); with `-c` too -> JSON-compact
  (`UCL_EMIT_JSON_COMPACT`)
- `-y` emit YAML (`UCL_EMIT_YAML`)
- (none of the above) emit config format (`UCL_EMIT_CONFIG`) — the default,
  and the only one ever diffed against a golden
- `-C` set `UCL_PARSER_SAVE_COMMENTS` (comments preserved and emitted)
- `-M` set `UCL_PARSER_DISABLE_MACRO` (macros treated as comments/inert)
- `-m` treat input as msgpack (`UCL_PARSE_MSGPACK` instead of `UCL_PARSE_UCL`)
  — not actually exercised by `basic.test` (no `-m` invocation there)
- `-f` use fd-based I/O (`open`/`ucl_parser_add_fd`/raw `write`) instead of
  `fopen`/`ucl_parser_add_chunk_full`/`fprintf` — exercises the alternate
  ingestion path, output is discarded either way

**Parser setup (always):** `flags = UCL_PARSER_KEY_LOWERCASE` plus the `-C`/
`-M` bits above; registers variable `ABI=unknown`
(`ucl_parser_register_variable`); if an input filename is given, calls
`ucl_parser_set_filevars(parser, fname_in, true)` (this is what makes
`${CURDIR}` expand inside `.include`/`.load` directives in the `basic/*.in`
fixtures — critical for a Go port to replicate, since roughly half the
fixtures rely on it).

**Two-phase parse/emit/reparse:** parses once from the input file/fd,
emits (to an in-memory buffer via `ucl_object_emit_memory_funcs`, or
`UCL_EMIT_JSON`/`UCL_EMIT_JSON_COMPACT`/`UCL_EMIT_YAML` directly), frees the
first parser/object, **re-parses the emitted text with a second parser**
(`ucl_parser_add_string`), and it's the *second* parse's object that is
actually emitted to the real output file/fd. So `test_basic` is itself a
mini round-trip test: parse -> emit -> reparse -> emit-to-file. If either
phase produces a parser error, it writes `"Error occurred (phase N): %s\n"`
to the output and returns exit code 1 (this is why some `.in` files have no
`.res` — `10.in` is expected to fail entirely, e.g., and the wrapper's
"if test_basic returns nonzero, print output and `exit 1`" behavior makes
that a hard requirement: **`10.in` must actually fail to parse** for
`basic.test` to pass overall — this is a negative case, not merely
"skip diff check". Any Go port of `10.in` must assert a parse error.)

**What it asserts:** nothing internally beyond "no parser error" (exit code
0 unless a phase errored) — output correctness is entirely external, via
`basic.test`'s `diff` against `<n>.res` when present.

### `basic.test` wrapper

For each `tests/basic/*.in` file (call the basename `$_t`):
1. `test_basic $_t.in $_out` (writes to a scratch file, NOT `$_t.out`) —
   if the process exit code is nonzero, print the output and abort the
   whole test run (`exit 1`). **This means every `.in` file must at least
   not crash the harness at exit-code level except where failure IS the
   expected outcome** — but note: exit code check happens unconditionally,
   including for `10.in`, whose whole point is a parse error. So `10.in`
   in fact must produce exit code 0 from `test_basic`'s own perspective —
   re-check: `test_basic` returns `ret` (0 or 1) only from its own `goto
   end` paths tied to *its own* parser errors; but `.in` files that are
   individually malformed UCL still result in `ucl_parser_get_error()!=NULL`
   → phase-1 error → `ret=1`. Since `10.in` has no `.res` and is documented
   (HANDOFF.md) as parsing successfully in some looser sense — **verify
   empirically against the real C binary before assuming pass/fail**; do
   not hardcode an assumption in the Go port without running the reference
   binary once toolchain is available.
2. If `$_t.res` exists, diff scratch output against it (`diff -s -u`); any
   difference -> abort with `exit 1`. This is the only byte-for-byt
   n
   e-checked assertion in the whole basic suite, and it's always the
   **config emitter** output post-round-trip (see two-phase description
   above — the golden reflects parse→emit→reparse→emit, not a single pass).
3. Five more invocations, all with output redirected to `/dev/null` — smoke
   tests only, asserting nothing but that the process doesn't itself cause
   the wrapper to `exit 1` (note: the wrapper does NOT check exit codes for
   these five — only the first invocation's exit code is checked; these are
   purely "doesn't crash the shell pipeline" style, not even asserted):
   - `-f` (fd path)
   - `-j` (JSON)
   - `-c -j` (JSON compact)
   - `-y` (YAML)
   - `-C` (save comments)
   - `-M` (skip macro)
   - `-M -C` (skip macro + save comments)

### test_generate (`test_generate.c`, 317 lines) — wrapper `generate.test`

**What it does:** the broadest single-binary exercise of the object-model
API — no input file at all; it *programmatically builds* a `ucl_object_t`
tree via ~40 API calls, covering:
- `ucl_object_fromstring_common` with `UCL_STRING_TRIM`/`UCL_STRING_ESCAPE`
  combinations
- key insert/replace (`ucl_object_insert_key`, `ucl_object_replace_key`)
- array ops: `ucl_array_append`/`prepend`, `ucl_array_index_of`,
  `ucl_object_copy`, `ucl_array_delete`, `ucl_array_pop_last`,
  `ucl_array_pop_first`, `ucl_object_array_sort` with
  `ucl_object_compare_qsort` (including UTF-8 strings and an emoji, sorted)
- refcounting: `ucl_object_ref`/`ucl_object_unref` on a shared boolean node,
  verified still valid/mutable after the parent tree is freed
  (`ref->value.iv = 100500` at the very end, post-unref-of-tree)
- string-to-typed-value parsing via `UCL_STRING_PARSE*` flags (bytes
  suffix "1mb" -> int 1048576, "3.14" -> float, "true"/"off" -> bool,
  emails/hashes that fail int-parse and stay strings)
- userdata objects (`ucl_object_new_userdata` with dtor/emit callbacks) —
  emits literal string `"test userdata emit"`
- key lookup: `ucl_object_lookup_any`, `ucl_object_lookup_len`,
  `ucl_object_pop_key` (pop-and-remove semantics, second pop returns NULL),
  `ucl_object_lookup_path` (dotted path lookup, including "ignore leading/
  repeated dots" edge cases: `.key4.1`, `.key4........1...` both find the
  same element as `key4.1`; `.key4.3` and `key9..key1` correctly miss)
- merge semantics: `ucl_object_merge` (object merge with overwrite flag),
  `ucl_array_merge` (array merge with append flag)
- delete: `ucl_object_delete_key`, `ucl_object_delete_keyl` (length-based,
  with embedded NULs in the key: `"key18\0\0"` length 7)
- comments API: `ucl_comments_add`, `ucl_comments_find`, `ucl_comments_move`
  (re-attaching a comment from one node to another)
- `ucl_elt_append` (implicit-array chaining via `next`) combined with
  `ucl_array_replace_index`
- iteration: `ucl_object_iterate_new`/`_safe`/`_reset`/`_free`,
  `ucl_object_iter_chk_excpn`, walking into a nested array's elements and
  back out
- **emits the whole tree** with `UCL_EMIT_CONFIG` (with comments attached)
  to `emitted`, writes it to the output file/stdout
- **then re-parses** `emitted` with a *second* parser
  (`UCL_PARSER_NO_IMPLICIT_ARRAYS` first, expecting a duplicate-key parse
  **error** with `UCL_DUPLICATE_ERROR` strategy since `key16` was inserted
  twice — asserts this fails; falls back to a fresh parser with
  `UCL_DUPLICATE_MERGE` strategy which must succeed), and asserts
  `ucl_parser_get_column(parser) == 0`,
  `ucl_parser_get_linenum(parser) != 0`,
  `ucl_parser_get_error_code(parser) == 0` after `ucl_parser_clear_error`.

**What it asserts:** ~15 `assert()` calls scattered through (index-of
results, popped values, lookup-path hits/misses, iterator type sequence,
parser reparse behavior) — a C-style "abort on failure" test, not a
tabulated one. The one externally-checked thing is the emitted text,
diffed by the wrapper against `tests/generate.res` (629 bytes, checked in
full above — F.e. `key4` array is sorted ascending `[9.999, 10, 10.1]`,
`key14`'s UTF-8/emoji array is sorted per `ucl_object_compare_qsort`
producing order `[10, 9.999, 10.1, "abc","cde","😎","Ебв","абв"]`, note the
Cyrillic/emoji byte-order-dependent sort — a Go port must replicate the
exact same comparator, likely a byte-wise string compare + type-ordering
rule, not locale-aware collation).

**Env vars:** none required beyond the CMake-provided three (it takes only
an output-path positional arg, no `TEST_DIR` lookups itself — no input
file is read).

### `generate.test` wrapper
Runs `test_generate <outdir>/generate.out`, diffs against
`tests/generate.res`, `exit 1` on mismatch. Single golden-file comparison,
no smoke-only variants.

### test_roundtrip (`test_roundtrip.c`, 280 lines) — wrapper `roundtrip.test`

**What it does:** newest test (2026 copyright), added for PR #373 emitter-
escaping bugs. Self-validating (no `.res` file): 8 named cases, each with a
`build()` function constructing a `ucl_object_t` tree in memory (via
`ucl_object_fromlstring`/`ucl_object_insert_key`, with the `UCL_OBJECT_SQUOTED`
flag manually set on some string objects). For each case:
1. emit with `UCL_EMIT_CONFIG`
2. re-parse the emitted text with a fresh parser (flags=0)
3. `ucl_object_compare(orig, reparsed)` must be 0 (structural equality)
4. **idempotency check**: re-emit the reparsed object and `strcmp` against
   the first emit — must be byte-identical (catches emitters whose output
   depends on parser-stripped flags)

**Cases (all named, i.e. a Go port should keep these exact case names as
subtests):**
1. `simple` — baseline scalar object (string/int/bool)
2. `heredoc_normal` — long multi-line string with no `EOD` substring,
   normal heredoc emission
3. `heredoc_eod_middle` — long string containing literal `\nEOD\n` in the
   middle (must not prematurely close the heredoc on reparse)
4. `heredoc_eod_tail` — long string ending in `\nEOD` with no trailing
   newline (boundary condition on the emitter's scan)
5. `heredoc_eod_substring` — long string containing `EOD` not preceded by a
   newline (false-positive guard — must still emit/reparse as heredoc)
6. `squote_simple` — single-quoted string containing a single quote,
   `UCL_OBJECT_SQUOTED` flag set manually
7. `squote_backslash_quote` — single-quoted string containing literal
   `\'` (backslash-quote) — must not be misinterpreted as an escape on
   reparse
8. `combined` — one object exercising a long-heredoc-with-double-EOD value,
   a squoted `\'`-containing value, and a plain nested object together

**What it asserts:** all of the above per-case; a failure counter
accumulates, final exit code is nonzero if any case failed
(`%d/%zu round-trip cases failed`).

**Env vars:** none — fully self-contained, no file I/O of its own beyond
stdout/stderr.

### `roundtrip.test` wrapper
Just execs `test_roundtrip` with no args; `exit $?` — pure pass/fail via
process exit code, no diffing.

### test_streamline (`test_streamline.c`, 131 lines) — wrapper
`streamline.test`

**What it does:** exercises the *streaming* emitter API
(`ucl_object_emit_streamline_new`/`_start_container`/`_add_object`/
`_end_container`/`_finish`) as opposed to the tree-walking
`ucl_object_emit`/`ucl_object_emit_full`. Builds a small object (`key1`,
`key2`, `key3` — trimmed/escaped string variants, same as the head of
`test_generate.c`'s object) up front and emits it normally as the streaming
context's initial state, then **manually streams** a 4th key (`key4`, an
array of 3 numbers) element-by-element through the streamline API rather
than building it in the tree first. Supports `-j`/`-c`/`-y` flags
(untested by the wrapper — always run with no flags, i.e. config emitter)
selecting `UCL_EMIT_YAML`/`UCL_EMIT_JSON[_COMPACT]`/`UCL_EMIT_CONFIG` for
`ucl_object_emit_streamline_new`.

**What it asserts:** just `assert(ctx != NULL)` after context creation —
everything else is externally verified by the wrapper's diff.

**Env vars:** none beyond the standard three; takes one positional output
path arg (no input file — nothing is parsed in this test).

### `streamline.test` wrapper
Runs `test_streamline <outdir>/streamline.out`, diffs against
`tests/streamline.res` (4 lines, shown above — config-emitter output with
the array on multiple indented lines, trailing comma per element). Single
golden comparison, `exit 1` on mismatch.

### test_msgpack (`test_msgpack.c`, 468 lines) — wrapper `msgpack.test`
(covered in depth elsewhere; inventory only)

**What it does:** randomized round-trip fuzzer-style test (not a true fuzzer
— seeded from `/dev/urandom` once per run, not corpus-driven). Builds
`ntests=10` random-typed elements per iteration (`niter=20` iterations) from
a fixed pool of generator functions (`ucl_test_integer/string/boolean/map/
array/large_map/large_array/large_string/null`), selects via a PCG32 PRNG
(`pcg32_random`), inserts under random keys, adds one large-array/large-map/
large-string element each, emits `UCL_EMIT_MSGPACK`, re-parses the msgpack
bytes, and (implied by the file structure — key/element comparison logic
follows in the un-shown tail) compares the round-tripped tree against the
original. Optional single positional output-file arg mirrors `test_basic`'s
`fname_out` pattern but for raw emitted-bytes dumping/debugging, not part of
the pass/fail contract.

**Flags/argv:** none via getopt — just `argv[1]` as an optional dump path.

**Env vars:** none.

### `msgpack.test` wrapper
Execs `test_msgpack` with no args — pure exit-code pass/fail (randomized
internally, no golden file, no corpus).

### test_msgpack_malformed (`test_msgpack_malformed.c`, 272 lines) —
wrapper `msgpack_malformed.test` (inventory only)

**What it does:** targeted regression tests for issue #363 (heap-buffer-
overread in `ucl_msgpack_parse_ignore` from a missing bounds check after
length reassignment for fixext/ext types). Named test functions, each
constructing a specific truncated/malformed msgpack byte sequence and
asserting `ucl_parser_add_chunk_full(..., UCL_PARSE_MSGPACK)` fails
cleanly (no crash/overread) — same `FAIL`/`CHECK` macro pattern as
`test_csexp.c`. Cases (from `main`, in order): `test_issue363_fixext2_
truncated`, `test_fixext1_truncated`, `test_fixext4_truncated`,
`test_fixext8_truncated`, `test_fixext16_truncated`, `test_ext8_truncated`,
`test_ext16_truncated`, `test_empty_input`, `test_truncated_string`,
`test_truncated_map_key`, `test_truncated_int`, `test_unknown_format`.

**Env vars:** none. **Flags:** none — `main(void)` style, no argv parsing.

### `msgpack_malformed.test` wrapper
Execs `test_msgpack_malformed` with no args — exit-code pass/fail.

### test_schema (`test_schema.c`, 169 lines) — wrapper `schema.test`
(covered in depth elsewhere; inventory only)

**What it does:** reads a JSON-Schema-draft-4-style test-suite file from
**stdin** (`read_stdin`, manual growable-buffer read loop off
`STDIN_FILENO`), parses it as UCL/JSON (`ucl_parser_add_string`, flags=0),
then iterates top-level suite entries, each with `schema`/`tests`
(themselves each `data`/`description`/`valid`) — runs
`ucl_object_validate(schema, data, err)` per test case and compares the
boolean result against the expected `valid` field; prints mismatches with
description/data/schema dumps to stdout. Exit code reflects overall
pass/fail for that one invocation (one schema-suite file per process
invocation).

**Flags:** none (no argv parsing) — input is **always stdin**, this is the
one binary in the suite that doesn't take a filename argument at all.

**Env vars:** none itself; driven entirely by the wrapper's redirection.

### `schema.test` wrapper
Loops `${TEST_DIR}/schema/*.json` (25 real suites — 27 files on disk, 2 are
`.json.disabled`: `definitions.json.disabled`, `refRemote.json.disabled`,
which the glob `*.json` does not match, so they're silently skipped, not
merely "disabled" by content — a Go port should likewise skip files with
that literal suffix, or just not `t.Skip` them since they're absent from
the glob entirely). For each: pipes the file to `test_schema` via stdin,
appends stdout to a shared `/tmp/_ucl_test_schema.out` (accumulator, not
diffed against anything — purely for human inspection on failure), counts
successes vs. total, and the whole wrapper fails (`exit 1`) if
`_tests != _succeed` (i.e. **all** suite files must fully pass internally —
`test_schema`'s own exit code per file is the real assertion, this loop
just tallies it).

### test_speed (`test_speed.c`, 160 lines) — wrapper `speed.test`

**What it does:** a **benchmark**, not a correctness test — parses a large
real-world UCL file (`tests/rcl_test.json.xz`, 1.85 MB compressed) multiple
times and reports timing. Its pass/fail signal is only "did it crash /
return nonzero", not any content assertion; not read in full since it's out
of scope for a pure correctness port, but its shape (single positional
input-file arg, `argv[1]`) is simple.

**Env vars:** relies on `xz` being present (`sh -c "xz -c < /dev/null >
/dev/null"` as a capability probe) — the wrapper unconditionally attempts
`xz -cd` decompression regardless of that probe's actual result (the probe
appears vestigial/unused in the `.test` variant, unlike `run_tests.sh`
which does gate on it). **Needs `tests/rcl_test.json.xz`** unpacked via
`xz`, written to `${TEST_OUT_DIR}/test_file`, pre-read once ("cheat
benchmark" — page cache warm-up), then passed to `test_speed`, then deleted.

### `speed.test` wrapper
As described — loop over `${TEST_DIR}/*.xz` (only one file matches:
`rcl_test.json.xz`), decompress, benchmark, delete scratch file. `exit 1`
if `test_speed` returns nonzero.

### test_strnstr (`test_strnstr.c`, 82 lines) — no `.test` wrapper (direct
CTest command)

**What it does:** unit tests for two internal C string helpers exposed via
`ucl_internal.h`: `ucl_strnstr(haystack, needle, haystack_len)` (bounded
`strstr`) and `ucl_strncasestr` (case-insensitive variant). Manually
heap-allocates 1-3 byte buffers (to make ASan/valgrind catch any 1-byte
overread) and asserts exact pointer-return semantics:
- partial match that doesn't fit within `haystack_len` -> `NULL`
  (`"x:/"` searching for `"://"` within length 3 -> NULL, since only "x:/"
  is present, not "://")
- exact match at start -> returns pointer to start of haystack
- single-char haystack matching single-char needle with `len=1` -> returns
  haystack pointer; same call with `len=0` -> `NULL` (zero length means "no
  bytes to search")
- empty needle (`""`, len 0) against a length-0 search window still
  returns the haystack pointer (matches at the very start, vacuously)
- `ucl_strncasestr` mirrors all of the above with a case-folded compare
  (`"xA/"` vs `"a//"` -> NULL; `"aBc"` vs `"AbC"` -> matches at start)

**Env vars:** none. **Flags:** none, `main(void)`.

**Not directly portable:** these test a **C-specific bounded string-search
helper** that exists only because C has no native bounded/length-aware
substring search (`strstr` requires NUL termination). Go's standard
library (`strings.Contains`, `strings.Index`, `bytes.Index`,
`strings.EqualFold` + manual scan for case-insensitive) already operates on
length-delimited `string`/`[]byte` with no analogous gap, so there is no
Go equivalent function to port test-for-test. If the Go port's internal
code ends up calling `strings.Index`/`bytes.Index` directly wherever C
called `ucl_strnstr`, this test has no reason to exist in Go at all — note
this explicitly rather than porting a vestigial helper.

## 2. `tests/basic/` fixture table

26 `.in` files, 25 have a matching `.res` (only `10.in` does not — and per
above, `10.in` is a case that appears designed to fail parsing, not merely
"unchecked output"; verify exit-code expectations against the real C
binary before locking in Go behavior). One subdirectory, `include_dir/`
(fixtures for glob/priority `.include` tests), plus flat `.inc` files used
by individual `.in` files via `${CURDIR}`-relative `.include`/`.load`.

| File | Golden | Feature covered |
|---|---|---|
| `1.in` | yes | number literal edge cases: hex (`-0xdeadbeef`), malformed hex-with-suffix falling back to string (`0xdeadbeef.1`, `0xreadbeef`), scientific notation, bare `yes`/`no` -> bool, duplicate key -> implicit array |
| `2.in` | yes | nested sections/objects, arrays with mixed types, suffixed numbers (`1s`,`1min`,`1kb`,`5M`,`10mS`,`10y` — time/byte multipliers), quoted vs. bare strings, path-like bare tokens, variable expansion (file continues past shown head) |
| `3.in` | yes | comments, section-with-colon key (`ALIAS :`), duplicate keys inside a nested object, array with bracket literal token (`some[]value`), path-list arrays |
| `4.in` | yes | full realistic package-manifest style document: nested objects/arrays, heredoc (`<<EOD` ... with embedded near-miss `EOD`/`EOF` lines testing heredoc terminator matching), escaped strings, boolean values, `origin`/`categories`/`files`/`directories`/`scripts` nested maps |
| `6.in` | yes | comments-only prefix, single trivial `key = value` |
| `8.in` | yes | nested `section ... { }` blocks with bareword tags (multi-word section headers: `subsubsection testsubsub1 testsubsub2 {...}`), duplicate section names merging, empty array (`array = []`), inline comments after `{` |
| `9.in` | yes | `.include` macro: correctness of relative-path (`$CURDIR`) resolution, repeated includes, commented-out includes, `.include(prefix=true; key=...; target=...; glob=true)` variants (prefixing included content under a key, globbing into an array target), `.try_include` on a nonexistent file (must not error) — uses `9.inc`, `9-comment.inc`, `9-empty.inc` |
| `10.in` | **no** | regression/negative case: duplicate `section foo bar`/`section foo baz` blocks plus a later conflicting `section foo { bar = lol }` — comment says removing the last line "makes parsing successful", implying this fixture is expected to **fail** to parse as shipped |
| `11.in` | yes | leading blank lines before a bare JSON-style object (`{"key":"value"}`) — parser must tolerate leading whitespace/newlines before content |
| `12.in` | yes | trailing comma after a scalar value (`key1: 12   ,`), and value continuing after another key on same conceptual line (`key2: 12 value`) — bareword multi-token value handling |
| `13.in` | yes | `.include(glob=true, something="test")` glob include over `include_dir/test*.conf`, `.include(priority=N)` for two files (`pri1.conf`/`pri2.conf`), a commented-out `.include(try=true)` of an intentionally-invalid file — uses `include_dir/{test1,test2,test3,pri1,pri2,invalid}.conf` |
| `14.in` | yes | comment-only section body, `.include(try=true) "./1.in"` (relative non-CURDIR include of another fixture file), trailing key after include |
| `15.in` | yes | `.include(priority=1)` interacting with a **duplicate key across include and main doc** — regression comment explicitly documents a formerly-buggy "override" behavior (`15.inc`, priority semantics vs. object-vs-scalar key clash) |
| `16.in` | yes | variant of 15: duplicate `overrided` object key appearing twice in the *same* file (not via include) after a priority include — implicit-array-of-objects semantics (`16.inc`) |
| `17.in` | yes | issue-74 regression: a bare string ending in a trailing backslash with no following char (line-continuation-like edge case at EOF) |
| `18.in` | yes | `.inherit` macro (object inheriting keys from a previously-defined object by name), multiple levels of inheritance chaining (`mything1` inherits `defaults`, `mything`, and itself), `.priority N` macro affecting override precedence, low-priority comment marker |
| `19.in` | yes | `.include(duplicate="append"/"merge"/"rewrite")` — the three duplicate-handling strategies for object/array/scalar keys, each exercised via a dedicated `.inc` file (`19-append.inc`, `19-merge.inc`, `19-rewrite.inc`) against pre-existing `okey_*`/`akey_*`/`skey_*` keys |
| `22.in` | yes | issue-113 regression: a non-ASCII/invalid UTF-8 byte as a bare key (`<0xEF 0xBF 0xBD-ish>=1` — exact byte in file) — parser must handle or reject invalid UTF-8 key bytes without crashing |
| `23.in` | yes | `.include()` with **no options** pulling in four style variants of included content (JSON-with-braces, JSON-without-braces, UCL-with-braces, UCL-without-braces) interleaved with top-level scalar keys before/after — tests that bare (non-object-wrapped) included content merges correctly at top level (`23-{json,ucl}-{with,without}-braces.inc`) |
| `comments.in` | yes | exhaustive comment-syntax coverage: empty `/**/`, multi-line `/* ... */`, comments containing quote-like text, nested `/* /* */ */` (C-style non-nesting — must terminate at first `*/`), `#`-line comments, a value that is itself a quoted string containing `/*` (must not be treated as a comment start inside quotes) |
| `escapes.in` | yes | string escape sequences: `\r\n\b\t\f\\\"`, unicode escapes `\u03B4` (BMP), `\u0B90`, `\u1F640` (astral-looking but actually a 5-hex-digit-truncated escape — libucl's `\u` is fixed 4-hex-digit, so `\u1F640` parses as `\u1F64` + literal `0`), `\uFFFF` |
| `heredoc_eod.in` | yes | heredoc-style long double-quoted string containing a literal `\nEOD\n` substring — must not be confused with actual `<<EOD` heredoc terminator syntax (this is a *quoted* string, not a heredoc block, so `EOD` inside it is just text) |
| `issue312.in` | yes | regression: two same-named sections (`section_a` twice, `section_b` twice) each with different single keys — tests object-merge-vs-implicit-array semantics for repeated section names at top level |
| `issue319.in` | yes | minimal heredoc block regression: `key = <<EOD\nvalue\nEOD` — simplest possible heredoc round-trip |
| `load.in` | yes | `.load(...)` macro (distinct from `.include`) with every combination of `try`/`multiline`/`trim`/`escape` boolean options and `target="string"` vs `target="int"`, plus varying `priority`, plus one `try=true` load of a genuinely-missing file (`load_bad.inc`, must not error) — uses `load.inc` |
| `squote.in` | yes | single-quoted string syntax: escape handling inside `'...'` (which escapes are honored vs. literal — e.g. `\n` inside squotes vs generic backslash passthrough), line-continuation backslash-newline inside a squoted string, empty squoted string `''`, trailing literal backslash-escaped quote |

`include_dir/` fixtures (used only as include targets, not parsed directly
as top-level test cases): `invalid.conf` (deliberately malformed —
`@@@@ BAD UCL ~~~~`), `pri1.conf`/`pri2.conf` (priority-tagged key/value
pairs), `test1.conf`/`test2.conf`/`test3.conf` (three near-identical
`key = valueN;` files for glob-include testing).

## 3. Go re-expression plan

Proposed package: a `tests` (or `ucltest`) directory alongside the ported
Go package, e.g. `go-libucl/ucl/` (library) + `go-libucl/ucl/testdata/`
(verbatim copies, or a symlink/vendor of `libucl/tests/basic`,
`libucl/tests/schema`, `libucl/tests/rcl_test.json.xz` — copy rather than
symlink for a self-contained module). Suggested file-by-file mapping:

- **`basic_test.go`** (replaces `test_basic.c` + `basic.test`):
  `TestBasic(t *testing.T)` that does `filepath.Glob("testdata/basic/*.in")`,
  and for each, a subtest `t.Run(base, func(t *testing.T) {...})`:
  1. parse with lowercase-keys flag + `ABI=unknown` variable + filevars set
     from the `.in` path (so `${CURDIR}` resolves) — mirrors phase 1
  2. emit `UCL_EMIT_CONFIG` to a buffer
  3. re-parse that buffer with a second parser (mirrors the two-phase
     round-trip in the C driver) — **note this double-parse is
     load-bearing for matching the existing `.res` goldens**, since they
     were generated from the *second* parse's emit, not the first
  4. emit again, compare byte-for-byte against `testdata/basic/<n>.res` via
     `os.ReadFile` + `bytes.Equal` (skip compare, only assert `err == nil`,
     when no `.res` exists — except `10.in`, which should assert `err !=
     nil` per the negative-case finding above, pending empirical
     confirmation against the C binary)
  5. sub-subtests (or just sequential calls within the same `t.Run`) for
     the smoke-only flag combinations: FD-based-equivalent (Go has no
     separate fd-vs-buffer distinction worth testing unless the Go API
     explicitly offers an `io.Reader`-based entry point mirroring
     `ucl_parser_add_fd` — if so, assert it produces the same object as the
     string path), JSON, JSON-compact, YAML, save-comments, skip-macro,
     skip-macro+save-comments — each just asserting `err == nil` (no
     output comparison), matching the wrapper's smoke-only treatment.
  Table-driven via directory globbing, not a hardcoded slice of names, so
  new `.in`/`.res` pairs dropped into `testdata/basic/` are picked up
  automatically — matches CLAUDE.md's stated test-data convention.

- **`generate_test.go`** (replaces `test_generate.c`): a single
  `TestGenerate(t *testing.T)` that ports the ~40 API calls verbatim (this
  is the one driver that's mostly a straight-line translation, not
  table-driven, since it's really an object-model API exercise wearing a
  test's clothing) using `t.Fatalf`/`require`-style checks in place of each
  `assert()`, ending with an emit compared against `testdata/generate.res`
  (copied verbatim). Keep the qsort-comparator behavior
  (`ucl_object_compare_qsort`) as a named exported comparator in the
  library so this test can call it exactly as the C one does.

- **`roundtrip_test.go`** (replaces `test_roundtrip.c`): translates
  directly — a `[]struct{name string; build func() *Object}` table (the Go
  mirror of `struct roundtrip_case`), one `t.Run` per case reusing the
  **exact same 8 case names** (`simple`, `heredoc_normal`,
  `heredoc_eod_middle`, `heredoc_eod_tail`, `heredoc_eod_substring`,
  `squote_simple`, `squote_backslash_quote`, `combined`), each doing
  build->emit->reparse->compare->re-emit->idempotency-compare exactly as
  the C `run_case` does. No `.res` files needed (self-validating, as in C).

- **`streamline_test.go`** (replaces `test_streamline.c`): a
  `TestStreamline(t *testing.T)` building the same small object + streamed
  `key4` array via the Go streaming-emitter API (once it exists), emit,
  compare against `testdata/streamline.res` (copied verbatim, 4 lines).

- **`strnstr` — no Go test needed** (or, if the Go port genuinely
  implements bounded-search helpers for internal reasons — e.g. porting
  `ucl_util.c` code that calls `ucl_strnstr` line-for-line before it gets
  refactored to idiomatic `strings.Index` — then a
  `strnstr_internal_test.go` with the same 5+5 cases, otherwise skip
  porting this driver at all and note its cases are subsumed by Go stdlib
  correctness).

- **msgpack / msgpack_malformed / csexp / schema**: out of this agent's
  scope per the task split, but for consistency they'd become
  `msgpack_test.go` (random-seeded round-trip, same PCG32-or-equivalent
  PRNG — or swap to Go's `math/rand` with a fixed seed for determinism,
  which would be an intentional, notable deviation from the C test's
  `/dev/urandom` seeding), `msgpack_malformed_test.go` (12 named cases,
  direct port), `csexp_test.go` (19 named cases per the sexp.md spec),
  `schema_test.go` (table-driven over `testdata/schema/*.json`, skipping
  the two `.disabled` files by not globbing them — mirrors the shell
  wrapper's glob behavior exactly).

- **speed**: `speed_test.go` guarded behind a build tag or `-short` skip
  (`testing.Short()`) since it's a benchmark, not correctness — better
  expressed as a Go benchmark (`func BenchmarkParse(b *testing.B)`) reading
  the decompressed `rcl_test.json.xz` once via `compress/xz`-equivalent
  (Go stdlib has no native xz reader — would need
  `github.com/ulikunitz/xz` or shell out to `xz`, or simply re-encode the
  fixture as `.gz`/`.zst`/plain and check that in instead, sidestepping the
  dependency question entirely — flag this as an open decision, not
  resolved here).

## 4. Fuzzers -> Go native fuzzing

`tests/fuzzers/ucl_add_string_fuzzer.c` and `ucl_msgpack_fuzzer.c` are
libFuzzer (`LLVMFuzzerTestOneInput`) harnesses — map directly onto Go 1.18+
native fuzzing (`go test -fuzz`):

- **`ucl_add_string_fuzzer.c` -> `FuzzParseString` (e.g. in
  `fuzz_test.go`)**: `func FuzzParseString(f *testing.F) { ... f.Fuzz(func(t
  *testing.T, data []byte) { if len(data) == 0 { return }; p :=
  NewParser(0); _ = p.AddString(string(data)) /* must not panic */ }) }`.
  The C version's `size==0` early-return maps to skipping empty input the
  same way; there's no meaningful "expected error" here — the only
  invariant under fuzz is **no panic/crash**, mirroring libFuzzer's
  crash-oracle-only contract (the C code doesn't check parse success
  either, just that `ucl_parser_add_string` doesn't crash and frees
  cleanly).
- **`ucl_msgpack_fuzzer.c` -> `FuzzParseMsgpack`**: same shape, guard
  `len(data) < 3` (mirroring the C `size < 3` early return), parse with
  `UCL_PARSE_MSGPACK` equivalent + key-lowercase flag, assert no panic; if
  parse succeeds, exercise whatever the Go object's teardown is (Go has no
  manual unref to test here, so this half of the C logic — freeing the
  object — has no Go analogue beyond just letting it be GC'd).
- **Seed corpus**: Go fuzzing reads seeds from
  `testdata/fuzz/FuzzParseString/*` (one file per seed, `go test -fuzz`
  auto-populates this directory on crash-minimization but seeds should also
  be pre-populated manually from **every existing `.in`/`.inc` fixture** in
  `testdata/basic/` and every csexp/msgpack test's byte literals — copy
  them in as the initial corpus so `go test -fuzz=FuzzParseString` starts
  from known-interesting inputs rather than blank/random. Use
  `f.Add([]byte(...))` calls seeded from a handful of representative fixed
  literals (e.g. `1.in`, `3.in`, `comments.in`, `squote.in` contents) plus
  optionally a corpus-loader `TestMain`/init that walks `testdata/basic`
  and calls `f.Add` per file for full coverage — the standard idiom is
  explicit `f.Add(...)` calls in the `Fuzz*` seed section, not dynamic
  directory walking (the `go test -fuzz` machinery only auto-discovers
  `testdata/fuzz/<FuzzName>/*` corpus files, not arbitrary directories), so
  the concrete plan is: write a small one-time script/init that copies
  `testdata/basic/*.in` into `testdata/fuzz/FuzzParseString/` in the
  corpus-file format (`go test` corpus files have a
  `go test fuzz v1\n[]byte("...")\n` header — use `f.Add` + run once
  with `-fuzz` briefly and let the framework write them out, or hand-format
  them) rather than hand-writing each one.
- No `libFuzzer`-specific dictionary/coverage-guidance concepts need
  porting — Go's fuzzer has its own coverage instrumentation built into
  the toolchain.

## 5. What cannot be ported directly (and why)

1. **`test_speed`** — needs `tests/rcl_test.json.xz` (1.85 MB) decompressed
   via `xz`; Go stdlib has no xz reader. Either shell out to `xz` (fragile,
   external dependency for a "pure Go" project — contradicts the project's
   own stated goal), pull in `github.com/ulikunitz/xz` (a new dependency
   the user may not want, given the whole point is dropping a C
   dependency — worth flagging even though xz itself isn't C-linkage, it's
   still an extra module), or re-encode the fixture in a Go-native-friendly
   format (`.gz` via stdlib `compress/gzip`, or just commit it uncompressed
   if size is acceptable) at port time. This is a **benchmark**, not a
   correctness test, so it's also reasonable to simply not port it at all
   and rely on `go test -bench` against ordinary `.in` fixtures for speed
   sanity, dropping the specific large corpus file requirement.
2. **`test_strnstr`** — tests a C-only gap-filler (bounded substring
   search over non-NUL-terminated buffers). Go's `string`/`[]byte` are
   already length-carrying, so `strings.Index`/`bytes.Index`/
   `strings.EqualFold` cover the same ground natively with no analogous
   missing-primitive gap. Recommend **not porting this driver test-for-
   test**; instead ensure wherever the C code used `ucl_strnstr` internally
   (e.g. scanning for heredoc terminators, URL scheme detection like the
   `"x:/"` vs `"://"` test data hints at) the Go port just uses the stdlib
   function directly, and cover that behavior via whatever higher-level
   test already exercises that code path (e.g. `heredoc_eod.in`,
   `issue319.in` already exercise heredoc-terminator scanning end-to-end).
3. **`run_tests.sh`** — appears to be dead/superseded tooling (its glob
   path `${TEST_DIR}/*.in` doesn't match current fixture layout under
   `tests/basic/`, and nothing in CMakeLists.txt or any `.test` wrapper
   invokes it). Do not port; port the `.test` wrappers' actual behavior
   instead (`basic.test`, `generate.test`, etc., as already reflected in
   the per-binary sections above).
4. **`test_schema` reading real stdin** — trivially portable (just read
   `[]byte` from the golden file directly in Go instead of piping through a
   subprocess's stdin), but note the *process-per-suite-file* granularity
   the shell wrapper uses maps naturally onto one Go subtest per JSON file
   rather than one process invocation per file — not a blocker, just a
   structural note already reflected in section 3 above.
5. **`test_msgpack`'s `/dev/urandom` + PCG32 seeding** — reproducible-CI
   concerns mean a Go port should almost certainly use a **fixed seed**
   (`math/rand` with a constant, or table-driven fixed cases replacing full
   randomization) rather than replicate nondeterministic seeding — flagged
   in section 3, called out again here since it's a deliberate, visible
   behavior change from the C test rather than a mechanical translation.
6. **10.in's expected outcome** — flagged twice above (sections 1 and 2):
   this fixture's pass/fail contract is ambiguous from static reading alone
   (the embedded comment implies parsing currently fails, and it has no
   `.res`, but `basic.test`'s wrapper unconditionally treats a nonzero
   `test_basic` exit code as a hard harness failure with no per-file
   allowance for "this one's supposed to fail"). This needs to be resolved
   empirically by building and running the real C `test_basic` binary
   against `10.in` once the toolchain is available (per HANDOFF.md's
   pick-up checklist) before writing the Go assertion for this one case —
   do not guess.
