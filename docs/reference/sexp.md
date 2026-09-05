# S-expression (CSEXP) parser spec

Source: `libucl/src/ucl_sexp.c` (269 lines, single exported function
`bool ucl_parse_csexp(struct ucl_parser *parser)`).
Test driver: `libucl/tests/test_csexp.c` (418 lines).

## 1. What "canonical S-expression" means here

This is **not** general Lisp S-expression syntax (no symbols, no bare atoms,
no dotted pairs, no quoting). It is Rivest's "canonical S-expression" (csexp)
format restricted to the subset libucl needs:

```
sexp   := "(" element* ")"
element:= sexp | atom
atom   := length ":" bytes         ; bytes is exactly `length` raw octets
length := [0-9]+                    ; decimal, no leading structure, >0
```

- The whole input must be exactly one top-level `sexp` (`(...)`)  — a bare
  atom or bare `)` at top level is rejected (`start_parse` requires `(`).
- Every `(` opens a **list**, represented in the object tree as `UCL_ARRAY`.
  There is no map/object form in csexp — only lists and byte-string atoms.
- Atoms are always length-prefixed byte strings (`N:` followed by exactly
  `N` raw bytes). There is no unprefixed/token atom form, no quoting, no
  hex (`#...#`) or base64 (`|...|`) transport forms from the full Rivest
  spec — libucl implements only the `N:bytes` "basic" transport.
- Whitespace between elements is **not** skipped/allowed except as literal
  bytes that would be consumed as part of a length-prefixed atom; the state
  machine has no whitespace-skipping state. A stray byte outside
  `(`, `)`, or digit context (e.g. right after `start_parse`, or as the
  first char of `read_length` when it's not `0-9`) is a "bad ... character"
  parse error. There is no support for whitespace between `(` and the next
  token, or between atoms, other than what's inherently consumed.
- Zero-length atoms (`0:`) are explicitly rejected (`"zero length element"`).
- Length is parsed as accumulating decimal digits into a `uint64_t`
  (`len = len*10 + digit`); if it exceeds `UINT32_MAX` parsing fails with
  "too big length of an element". This is a soft cap, not the true csexp
  spec (which allows arbitrary-precision lengths) — it exists to bound
  memory allocation.
- Nesting depth is capped at `UCL_MAX_NESTING` (1024, `ucl_internal.h:144`,
  shared with the msgpack parser). Exceeding it is a parse error ("csexp
  nesting too deep").

## 2. State machine

States (anonymous enum in `ucl_parse_csexp`):
`start_parse -> read_obrace -> {read_length <-> digits} -> read_value -> (NEXT_STATE dispatches back to read_obrace/read_ebrace/read_length) ... -> read_ebrace -> parse_err`

- **start_parse**: expects `(` as the very first byte of the chunk. Anything
  else -> "bad starting character for sexp block".
- **read_obrace**: on seeing `(`, checks depth against `UCL_MAX_NESTING`,
  allocates a new `struct ucl_stack` frame whose `obj` is a fresh
  `UCL_ARRAY`. First frame becomes both `parser->stack` and (if unset)
  `parser->top_obj`; subsequent frames are `LL_PREPEND`ed onto
  `parser->stack` (i.e. pushed — the stack is a plain singly-linked list
  used as a LIFO via prepend/pop-head). Increments `depth`, advances `p`,
  and dispatches via the `NEXT_STATE` macro to decide the next state from
  the following byte: `(` -> read_obrace (nested list), `)` -> read_ebrace
  (empty list), digit -> read_length (start of atom), anything else (or
  end of input while not already at a legal end) -> "extra data" error.
- **read_length**: accumulates decimal digits into `len`. On `:` with
  `len == 0` -> "zero length element" error; with `len > 0` -> transition to
  read_value. Non-digit, non-`:` byte -> "bad length character" error.
  Overflow past `UINT32_MAX` -> "too big length of an element" error.
- **read_value**: validates that `len` bytes remain in the buffer
  (`end - p >= len`, and `len` re-checked `!= 0` defensively); if not ->
  "invalid length: N, M remain" error (this is the guard for issue #361 —
  truncated/declared-too-long atoms). Otherwise allocates a `UCL_STRING`
  object, points `obj->value.sv` directly at the input bytes (`p`),
  sets `obj->len = len`, and **always** sets `UCL_OBJECT_BINARY` on the
  object's flags (csexp atoms are treated as raw/binary strings, not
  NUL-terminated C strings, since they may contain embedded NULs).
  If the parser was NOT created with `UCL_PARSER_ZEROCOPY`, the value is
  copied out (`ucl_copy_value_trash`) so it survives buffer reuse/free;
  with `UCL_PARSER_ZEROCOPY` the object aliases the original input buffer
  (caller must keep the buffer alive — same contract as UCL's normal
  zero-copy mode).
  If `parser->stack == NULL` at this point (no enclosing list — i.e. a
  value appearing after the outermost list has already closed) -> error
  "value outside of any list" (issue #365 regression: this used to be a
  NULL-pointer dereference). Otherwise the atom is appended to
  `parser->stack->obj` (the innermost open array) via `ucl_array_append`,
  `p` advances by `len` bytes, and `NEXT_STATE` re-dispatches on the next
  byte.
- **read_ebrace**: on `)`, pops the current stack frame. If
  `parser->stack == NULL` before popping (i.e. this `)` has no matching
  `(` — an extra closing brace) -> error ("invalid length: ..., ... remain"
  message text, reused from the same error path — somewhat misleading
  wording but it's the actual "extra end brace" case per the comment).
  Otherwise pop `st = parser->stack`, set `parser->stack = st->next`;
  if there is now a parent frame, it must be of type `UCL_ARRAY` (it always
  is, by construction) and the popped list is appended into it via
  `ucl_array_append`; if the popped frame *was* the outermost frame, there's
  no parent to append into (it remains reachable only via `parser->top_obj`,
  which was set when it was created). Frees the stack node, decrements
  `depth` (floor 0), advances `p`, and `NEXT_STATE` dispatches again — note
  this is also where `NEXT_STATE`'s special "at end of input" branch
  matters: reaching `end` right after a `)` is the *only* legal way to run
  out of input (the macro only skips the "extra data" error when
  `state == read_ebrace`).
- **parse_err**: cleanup path, also reached as the `default` case. Walks
  `parser->stack`, freeing every frame; any frame's `obj` that is not
  `parser->top_obj` is explicitly `ucl_object_unref`'d here (this is the
  issue #365 leak fix — orphaned inner arrays that were never appended to
  a parent must be freed explicitly, since only `top_obj` is owned/freed by
  `ucl_parser_free`). Returns `false`.

After the main `while (p < end)` loop exits normally, the function checks
`state != read_ebrace` — i.e. the input must end exactly on a closing brace
of the outermost list; anything else (mid-atom, mid-length, or an
unclosed list) is "invalid finishing state" and triggers the same
stack-draining cleanup, returning `false`. Only ending in `read_ebrace`
(and, by construction of the pop logic, `depth == 0`/`parser->stack == NULL`
at true top-level close) returns `true`.

Note: `NEXT_STATE` is also what enforces "no trailing garbage after the
final `)`": once the outer list is closed, `state == read_ebrace`, so
`NEXT_STATE`'s `p >= end` branch is silent (success) — but if there *is*
more input after the closing `)` (`p < end`), `NEXT_STATE`'s switch fires:
`)`  would be an extra-brace error on the next iteration, a digit starts
a new atom in `read_length`, which then fails in `read_value` because
`parser->stack` is now NULL ("value outside of any list") — this is
exactly the issue #365 bug-A regression case `()3:abc`.

## 3. Integration with the parser state machine (`UCL_PARSE_CSEXP`)

- `ucl_type_t`-like enum `UCL_PARSE_CSEXP` is one of the parse-type values in
  `include/ucl.h:225` (`enum { UCL_PARSE_UCL, UCL_PARSE_MSGPACK, ... ,
  UCL_PARSE_CSEXP, UCL_PARSE_AUTO }`, exact ordinal doesn't matter, only that
  it's a distinct case).
- Selected explicitly by passing `UCL_PARSE_CSEXP` as the `parse_type`
  argument to `ucl_parser_add_chunk_full(parser, data, len, priority,
  strategy, parse_type)`.
- Also reachable via **auto-detection**: when `parse_type == UCL_PARSE_AUTO`
  and there is at least one byte of input, `ucl_parser.c` (~line 3050-3062)
  inspects the first byte: high bit set (`(*data & 0x80) == 0x80`) ->
  `UCL_PARSE_MSGPACK`; first byte is exactly `(` -> `UCL_PARSE_CSEXP`;
  otherwise -> `UCL_PARSE_UCL`. So any UCL document that happens to start
  with a literal `(` byte would misdetect as csexp under `UCL_PARSE_AUTO` —
  csexp detection is a plain "starts with open-paren" heuristic, not
  content-sniffing.
- Dispatch: `ucl_parser.c` around line 3096: `case UCL_PARSE_CSEXP: return
  ucl_parse_csexp(parser);` — csexp parsing takes over the whole chunk; it
  does not interleave with the normal UCL state machine (`ucl_state_machine`)
  or with `ucl_parse_msgpack`. Each is a self-contained, chunk-at-a-time
  parser sharing only `struct ucl_parser` (in particular `parser->stack`,
  `parser->top_obj`, `parser->err`, `parser->chunks`) and the same
  `struct ucl_stack` type (reused across UCL/msgpack/csexp, though the
  `e` union member is unused by csexp).
- Chunk preconditions (asserted, not soft-checked): `parser->chunks != NULL`,
  `parser->chunks->begin != NULL`, `parser->chunks->remain != 0` — i.e.
  `ucl_parse_csexp` is only ever called with a non-empty current chunk (the
  zero-length-chunk case is special-cased earlier in `ucl_parser_add_chunk_full`
  and never reaches the format-specific parse call).
- `UCL_PARSER_ZEROCOPY` and `UCL_PARSER_DISABLE_MACRO` flags apply (macros
  are irrelevant to csexp anyway — there is no macro syntax in csexp, but the
  test harness passes `UCL_PARSER_DISABLE_MACRO` defensively). Other UCL
  parser flags (`UCL_PARSER_KEY_LOWERCASE`, `UCL_PARSER_SAVE_COMMENTS`,
  variable expansion, `.include` handling) are simply not consulted by this
  code path — csexp has no keys, no comments, no macros, no variables.
- Result retrieval is via the normal `ucl_parser_get_object(parser)`, which
  returns `parser->top_obj` — always a `UCL_ARRAY` (the outermost list),
  possibly containing nested `UCL_ARRAY`s and `UCL_STRING` (binary) leaves.
  There is no `UCL_OBJECT` (map) node anywhere in a csexp result.

## 4. Mapping sexp atoms to UCL object types

| csexp construct        | UCL object type | Notes |
|---|---|---|
| `(...)`                 | `UCL_ARRAY`      | one array node per paren pair, including the empty list `()` |
| `N:bytes` atom          | `UCL_STRING`     | always flagged `UCL_OBJECT_BINARY`; `len` = N; value points at (or copies) the raw N bytes verbatim — no charset assumption, no escaping, embedded NUL/control bytes preserved exactly |

There is no numeric/boolean/null inference — every atom is a `UCL_STRING`
with the binary flag, full stop. (This is unlike the main UCL parser, where
bare tokens get type-inferred as numbers/bools.) A Go port must **not** try
to interpret atom bytes as anything other than an opaque byte string when
building the object tree — that inference (if ever wanted) is entirely a
consumer-side concern.

Top-level result: `parser->top_obj` is the outermost `UCL_ARRAY` — matches
what `test_csexp.c` asserts (`top->type == UCL_ARRAY`) in every test case.

## 5. Error cases (exhaustive, from the state machine)

| Trigger | Error message (for parity, not necessarily verbatim in Go) | State |
|---|---|---|
| First byte isn't `(` | "bad starting character for sexp block: %x" | start_parse |
| Nesting depth would exceed 1024 | "csexp nesting too deep (over %d)" | read_obrace |
| calloc/ucl_object_typed_new OOM | "no memory" | read_obrace |
| Non-digit, non-`:` byte while reading a length | "bad length character: %x" | read_length |
| `:` reached with accumulated length 0 | "zero length element" | read_length |
| Accumulated length > UINT32_MAX | "too big length of an element" | read_length |
| Declared atom length exceeds remaining bytes (or is 0, defensively) | "invalid length: %llu, %ld remain" | read_value |
| Atom value appears with no enclosing open list (`parser->stack == NULL`) | "value outside of any list" | read_value |
| `)` with no matching open list (`parser->stack == NULL`) | "invalid length: %llu, %ld remain" (reused message) | read_ebrace |
| Any byte other than `(`, `)`, or a digit right after a value/close, when not legally at end-of-input | "extra data" | NEXT_STATE macro |
| Input ends anywhere other than exactly after the outermost `)` (mid-atom, mid-length, unclosed list) | "invalid finishing state: %d" | end-of-function check |
| Parent frame during `)`-pop is not `UCL_ARRAY` (structurally unreachable given this parser only ever creates arrays, but defensively checked) | "bad container object, array expected" | read_ebrace |

On every error path, all still-open stack frames are unwound and any
orphaned (non-`top_obj`) array objects are `ucl_object_unref`'d — this is
load-bearing for parity: a Go port's error path must not leak object
references for partially-built nested arrays (mirrors the C fix for issue
#365, leaks B and the deeply-nested variant).

## 6. Every case `test_csexp.c` asserts

All tests build a fresh `ucl_parser_new(flags | UCL_PARSER_DISABLE_MACRO)`,
feed the byte array via `ucl_parser_add_chunk_full(..., UCL_PARSE_CSEXP)`,
and either expect success + inspect the object tree (`parse_ok`), or expect
failure (`parse_fails`, using plain `UCL_PARSER_DISABLE_MACRO` flags).

Valid-input tests (`parse_ok`, must succeed and produce the described tree):

1. `test_single_element` — `(3:abc)`: top is `UCL_ARRAY` size 1; the one
   element is `UCL_STRING` "abc" (length 3).
2. `test_multi_digit_length` — `(11:hello world)`: multi-digit length
   parses correctly; element length 11, content "hello world".
3. `test_multiple_elements` — `(3:abc3:def)`: two atoms back-to-back with no
   separator; array size 2, in order "abc", "def" (validated via
   `ucl_object_iterate`).
4. `test_empty_list` — `()`: array size 0.
5. `test_nested_lists` — `((3:abc)(4:test))`: top array size 2, each element
   is itself a `UCL_ARRAY` of size 1 containing "abc" and "test"
   respectively (validated via `ucl_array_find_index`).
6. `test_binary_data` — `(3:\x00\x01\x02)`: raw NUL/control bytes inside an
   atom are preserved exactly, and the element carries `UCL_OBJECT_BINARY`.
7. `test_zerocopy` — same as case 1 but parsed with `UCL_PARSER_ZEROCOPY`;
   content must still read back correctly (aliased, not copied).
8. `test_deeply_nested` — `((6:nested))`: double nesting collapses correctly
   (outer array size 1 -> inner array size 1 -> string "nested").

Error-input tests (`parse_fails`, must return `false`/an error):

9. `test_err_truncated` — `(15:abc)`: declared length 15 but only 3 bytes
   available before the immediate `)` — issue #361 regression (must not
   over-read).
10. `test_err_bad_start` — `abc` (no leading paren) — bad starting char.
11. `test_err_bad_length_char` — `(a:abc)` — non-numeric length digit.
12. `test_err_zero_length` — `(0:data)` — zero-length atoms rejected.
13. `test_err_extra_brace` — `(3:abc))` — extra unmatched closing paren.
14. `test_err_unclosed` — `(3:abc` (no closing paren at all) — truncated
    input / unclosed list.

Issue #365 regression tests (`parse_fails`; these specifically guard against
a NULL-deref and memory leaks introduced/fixed around that issue):

15. `test_err_value_after_close` — `()3:abc`: a value token appears after
    the outermost list has already closed (`parser->stack` is `NULL` at that
    point) — must be rejected as "value outside of any list", not crash.
16. `test_err_unclosed_nested` — `((`: two opened, zero closed lists; must
    fail and must not leak the inner (orphaned, never-appended) array.
17. `test_err_unclosed_deeply_nested` — `(((`: three levels of unclosed
    nesting, same leak-freedom requirement, deeper.
18. `test_err_open_after_close` — `()(`: an empty list followed by another
    unclosed open paren.
19. `test_err_unclosed_with_data` — `((3:abc`: unclosed lists where the
    inner (never-appended) array actually has a child element — the child
    must also be freed along with its parent frame.

All 19 cases run sequentially from `main()`; a global `failed` counter turns
any `CHECK` failure into a nonzero process exit (`test_csexp.test` wrapper
just execs the binary with no args — pure smoke/assert test, no golden
file). For a Go port: table-driven `t.Run` per case name above, asserting
either the resulting object shape (valid cases) or `err != nil` (error
cases) is sufficient to reach parity; the memory-leak-freedom assertions
(cases 16-19) are automatically satisfied in Go by the garbage collector,
but the *behavioral* requirement — parsing must still return a clean error,
not panic or return a partially-built/dangling tree — still needs a real
test since a Go port using manual linked-list stack frames (mirroring
`struct ucl_stack`) could still construct an inconsistent object graph on
these inputs if the pop/cleanup logic is ported incorrectly.
