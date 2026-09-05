# Parser port — implementation notes

Files owned by this component: `parser.go` (chunks, lexer, state machine,
duplicate handling), `parser_macro.go` (macros, includes, variable expansion),
`parser_test.go` (the `testdata/basic` golden harness) and
`parser_unit_test.go` (everything the goldens cannot reach).

Spec: `docs/reference/parser.md`. Upstream quirks: `docs/findings.md`.

## Status

All 156 fixtures in `testdata/basic` pass: 147 output goldens and 9
expected-error goldens, each `emit(parse(emit(parse(input))))` with
`ParserKeyLowercase`, `ABI` = `unknown`, file variables from the input path and
one unconditional trailing newline.

Beyond the goldens the implementation was diffed against the compiled C library
(`../libucl/build`) on:

* 100 hand-written fixtures covering heredocs, nested comments, number suffix
  edge cases, every include form (prefix, target=array, glob, path, url, sign,
  self-include, nesting limit), `.load`, `.inherit`, `.priority`, CRLF, embedded
  NULs, truncated input and malformed braces — 6 emitter modes each;
* 3000 randomly generated token soups (5 seeds × 600), 6 modes each;
* all 10 parser flag values × 4 duplicate strategies on inputs exercising
  implicit arrays, priorities and time/byte suffixes;
* user macros (plain, context, failing, shadowing a built-in), macro arguments
  and the variable handler.

Every comparison matches byte for byte except the two divergences below.

## Deliberate divergences

### 1. Array element after an indented comment (`docs/findings.md` #3)

libucl skips whitespace and then comments exactly once when starting a value,
so a comment on the first line inside an array leaves the cursor on the next
line's indentation and the element is lexed as a string (`1` becomes `"  1"`),
or, for a block comment, fails with `string value must not be empty`. This port
loops instead, so a leading comment is transparent.

Consequences:

* `TestParserArrayLeadingComment` records the C's output next to ours for each
  shape, including the three the C already gets right, so the divergence stays
  visible.
* **`testdata/basic/9.comments.err` was replaced by
  `testdata/basic/9.comments.golden`.** That `.err` recorded a phase-2 reparse
  failure caused solely by this bug (repeated `.include`s make `prefix1` an
  implicit array, saved comments are emitted inside it, and libucl cannot read
  its own output back). Under the fix the fixture round-trips, so it is now an
  expected-success case. The golden was generated from this implementation, not
  captured from C — it is the only golden in the directory that was not. Its
  body is byte-identical to the emitted text the old `.err` file contained; only
  the reparse outcome changed.

### 2. Reads past the buffer are bounded, not reproduced

`docs/reference/parser.md` §12.2 listed three places where the C reads outside its
buffer, and this port bounds all three. A later pass against the reference
build under AddressSanitizer found that **only one of them is real**; see
`docs/findings.md` #10 for the evidence. The bounding stays regardless, because
it costs nothing and the same arithmetic that yields a stray byte in C is a
panic in Go.

* **Real.** A truncated `\u` escape read a fourth hex digit from past the end
  of the value. In Go that is an index out of range, so it was a crash on any
  input carrying one (`docs/findings.md` #9). Fixed here and upstream.
* **Not a defect.** `p[-1] != '\\'` in `ucl_skip_comments` and
  `ucl_parse_macro_arguments` cannot read before the buffer: both sites are
  reached only after the bytes they look back at have been consumed. The Go
  code is defensive, not compensating.
* **Not demonstrated.** The backwards space strip in `ucl_parse_value` has no
  explicit lower bound, and `*out_len = dstlen` in `ucl_check_variable_safe`
  assigns where its sibling accumulates. Neither could be triggered, including
  3,000,000 mutated fixtures under ASAN with each chunk exactly sized.
One further C artefact is not reproducible in Go and is called out here rather
than worked around: under `UCL_PARSER_ZEROCOPY`, `UCL_DUPLICATE_ERROR`'s
message prints `nobj->key` with `%s` even though a zero-copy key is not
NUL-terminated, so the C emits the whole rest of the input inside the message.
This port prints exactly the key. Go slices carry their length; there is
nothing to reproduce.

## Reproduced upstream bugs (not divergences)

Kept because output depends on them; each is commented at its site.

* `ucl_check_variable_safe` assigns rather than accumulates `*out_len` when a
  variable handler resolves a name, under-counting the buffer so pass two
  truncates. `TestParserVariableHandler` pins the observable half.
* A saved block comment records `p - beg + 1` bytes — one past the closing `/`.
  When the comment ends the chunk that byte is outside the buffer; both mmap'd
  files and the malloc'd read buffers libucl is fed in practice hold a zero
  there and the C's own output records the resulting NUL, so `skipComments`
  appends one explicitly instead of leaving it to chance.
* `next_key` and `end_of_object` are declared once in `ucl_state_machine` and
  `ucl_parse_key` leaves them alone on several paths, so they carry over between
  keys: after `a b { }`, a later `c = 1` whose key ended in `=` still sees
  `next_key == true` and gets nested. Found by differential fuzzing; hoisting
  them out of the loop is what makes the port agree.
* uthash returns the *most recently* added entry for a duplicate key, so
  re-registering a built-in macro name shadows it. `docs/reference/parser.md` §7.1 says
  the opposite; the C was checked and the doc is wrong.
* `ucl_parser_append_elt` does not increment the container length on the
  `NO_IMPLICIT_ARRAYS` path, and the wrapper array aliases the original key.
* `.try_include` of a missing file returns false from the macro handler without
  recording an error, silently truncating the rest of the chunk. `.include`
  with `try=true` is different: it clears `must_exist` but not `soft_fail`, so
  it is a genuine no-op. Both are pinned in `TestParserIncludes`.
* `UCL_PARSER_DISABLE_MACRO` skips macros, which is what it is documented to
  do. Released libucl rejects them instead, because it never enters the state
  holding its own skip path; this port takes the fix and so does upstream
  branch `fix-disable-macro` (`docs/findings.md` #2).

## Not implemented

* **URL includes.** `.include(url=true)` returns `URL support is disabled`,
  matching a libucl built without libfetch/libcurl. `docs/reference/parser.md` §12.8
  suggests an opt-in `Parser.HTTPClient`; that is additive when wanted.
* **Signature checking.** `PubkeyAdd` fails with `cannot check signatures
  without openssl` and `sign=true` is a no-op, matching a build without
  OpenSSL, which is the build the goldens came from.
* **Special handlers.** `ucl_parser_add_special_handler` (the magic-prefix
  preprocessor hook) has no Go equivalent yet. Nothing in the test corpus uses
  it, and `AddChunkFull` has the one place it would slot in.

## API notes

* `ucl_parser_register_variable(parser, var, NULL)` deletes a variable. Go has
  no nil string, so deletion is `Parser.UnregisterVariable`.
* `ucl_parser_set_filevars(parser, NULL, ...)` means "use the working
  directory"; the Go spelling is an empty filename.
* `ucl_parser_get_error` returns a C string. `Parser.GetError` returns an
  `error` whose text is byte-identical; `Parser.ErrorString` gives the raw text.
* Macro handlers drop the C's `void *ud`: a Go closure captures what it needs.
* `ParseMsgPack` delegates to `DecodeMsgPackFull`. The C's msgpack reader drives
  the same container stack as the UCL state machine; the Go decoder is
  standalone and returns a finished tree, which is merged into the top object
  when an earlier chunk already produced one.

## Nothing needed changing outside this component

No bugs were found in the object API, the emitters or the core containers while
building on them, and no file outside `parser*.go` was edited. Two places would
have been convenient to reach into but did not need modifying:

* `hash.Insert`/`hash.Replace` are used directly by the duplicate-key path
  rather than through `Object.InsertKey`, because the parser needs
  `ucl_hash_replace`'s "keep the iteration position" semantics and the
  case-insensitive hash variant. Both are already exposed within the package.
* `Object.length` is written directly where the C writes `stack->obj->len++`,
  including on the paths where the C's accounting is inconsistent.
