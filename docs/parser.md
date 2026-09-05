# libucl parser — implementation spec for a pure-Go port

Source of truth: libucl v0.9.4 (`adef4bb`), files `src/ucl_parser.c` (3269 lines),
`src/ucl_internal.h`, `src/ucl_chartable.h`, `include/ucl.h`, and the macro/include
implementations in `src/ucl_util.c`.

This document is written so that the parser can be implemented **without reading the C**.
Where the C behaviour is surprising or looks like a bug, that is called out explicitly —
it must still be reproduced, because the golden `.res` files in `tests/basic/` depend on it.

Scope: the parser only. The object model (`ucl_object_t`, the hash container, refcounting),
the emitters, msgpack, csexp and the schema validator are specified elsewhere; this document
only states the *contracts* it needs from them.

---

## 0. Contracts assumed from the object model

The parser needs these object-model facilities. Names are the C ones; the Go port may rename.

| C | Meaning |
|---|---|
| `ucl_object_t` | value node: `type`, `value` union, `key`/`keylen`, `next`/`prev` (implicit-array chain), `len`, `flags` (16 bits), `trash_stack[2]` |
| `ucl_object_new_full(type, priority)` | allocate with a priority baked into `flags` |
| `ucl_object_typed_new(type)` | `= ucl_object_new_full(type, 0)` |
| `ucl_hash_t` | insertion-ordered map from `(key,keylen)` → `*ucl_object_t`; created with a `ignore_case` flag |
| `ucl_hash_insert`, `ucl_hash_search`, `ucl_hash_replace` | map ops; `replace` keeps the *position* of the old key in iteration order |
| `ucl_array_append(top, elt)` | append to a `UCL_ARRAY` |
| `ucl_object_insert_key(top, elt, key, keylen, copy_key)` | insert, forming an implicit array on collision |
| `ucl_object_get_priority` / `ucl_object_set_priority` | see §6 |
| `ucl_object_copy` | deep copy (used by `.inherit`) |

`ucl_type_t` ordering is load-bearing (msgpack + the Go binding depend on it):

```
UCL_OBJECT=0, UCL_ARRAY=1, UCL_INT=2, UCL_FLOAT=3, UCL_STRING=4,
UCL_BOOLEAN=5, UCL_TIME=6, UCL_USERDATA=7, UCL_NULL=8
```

Object flags (16 bits; **the top 4 bits are the priority**, see §6):

```
UCL_OBJECT_ALLOCATED_KEY   = 1<<0
UCL_OBJECT_ALLOCATED_VALUE = 1<<1
UCL_OBJECT_NEED_KEY_ESCAPE = 1<<2   /* set by the parser for JSON-quoted keys containing escapes */
UCL_OBJECT_EPHEMERAL       = 1<<3
UCL_OBJECT_MULTILINE       = 1<<4   /* set by the parser for heredoc values */
UCL_OBJECT_MULTIVALUE      = 1<<5   /* set by the parser on the head of an implicit array */
UCL_OBJECT_INHERITED       = 1<<6   /* set by .inherit, and by comment attach (see §10) */
UCL_OBJECT_BINARY          = 1<<7
UCL_OBJECT_SQUOTED         = 1<<8   /* set by the parser for single-quoted values */
```

Error codes (`ucl_error_t`), in order:

```
UCL_EOK=0, UCL_ESYNTAX=1, UCL_EIO=2, UCL_ESTATE=3, UCL_ENESTED=4,
UCL_EUNPAIRED=5, UCL_EMACRO=6, UCL_EINTERNAL=7, UCL_ESSL=8, UCL_EMERGE=9
```

Constants:

```
UCL_MAX_RECURSION = 16     /* max nested chunks (includes) */
UCL_MAX_NESTING   = 1024   /* declared but never used by the parser */
UCL_TRASH_KEY     = 0
UCL_TRASH_VALUE   = 1
PRIOBITS          = 4
```

---

## 1. The state machine

### 1.1 States

`enum ucl_parser_state` — the numeric order matters only for debugging, not semantics:

```
UCL_STATE_INIT = 0
UCL_STATE_OBJECT        /* declared, NEVER entered */
UCL_STATE_ARRAY         /* declared, NEVER entered */
UCL_STATE_KEY
UCL_STATE_KEY_OBRACE
UCL_STATE_VALUE
UCL_STATE_AFTER_VALUE
UCL_STATE_ARRAY_VALUE   /* declared, NEVER entered */
UCL_STATE_SCOMMENT      /* declared; only read by ucl_skip_comments, never assigned */
UCL_STATE_MCOMMENT      /* declared; only read by ucl_skip_comments, never assigned */
UCL_STATE_MACRO_NAME
UCL_STATE_MACRO
UCL_STATE_ERROR
```

Only **8** states are live: `INIT`, `KEY`, `KEY_OBRACE`, `VALUE`, `AFTER_VALUE`,
`MACRO_NAME`, `MACRO`, `ERROR`. The Go port should keep the dead ones in the enum
(so numeric values stay stable if anything ever prints them) but never enter them.

`parser.prev_state` is a **single** saved slot, not a stack. It is used for exactly two
things: (a) remembering where to return after a macro (`MACRO` sets
`state = prev_state` when done), and (b) being clobbered on the way into
`UCL_STATE_ERROR` (never read back).

### 1.2 The driver loop

```
ucl_state_machine(parser):
    chunk = parser.chunks            /* head of the chunk list = current chunk */
    if parser.top_obj == nil: parser.state = UCL_STATE_INIT
    p = chunk.pos
    while chunk.pos < chunk.end:
        switch parser.state { ... }
    /* epilogue: flush pending comment; check for unmatched open braces */
```

Note the loop condition uses `chunk.pos`, but each arm works with a **local** `p` that
must be resynced (`p = chunk.pos`) after any helper that advances the chunk. In Go
there is no separate `p` — see §12.

`c` is a loop-local cursor used only by the macro states: `UCL_STATE_KEY` sets
`c = chunk.pos` when `ucl_parse_key` switched the state to `MACRO_NAME`, and
`MACRO_NAME` uses `c` as the start of the macro name. `c` is **not** reset between
iterations — this matters if a macro name spans awkwardly, but in practice
`MACRO_NAME` is always reached via `KEY`.

### 1.3 State-by-state

#### `UCL_STATE_INIT`

Entered at the start of every chunk (and forced whenever `top_obj == nil`).
Also forced to `INIT` by `ucl_parser_insert_chunk` and by the include handlers
before they add a nested chunk.

1. `ucl_skip_comments(parser)`. On failure → `ERROR`, return false.
2. Skip characters with class `WHITESPACE_UNSAFE` (`\t \n \v \f \r ' '`).
3. Look at the first remaining byte (if any):
   - `'['` → `state = VALUE`, consume it, `seen_obrace = true`
   - `'{'` → consume it, `state = KEY_OBRACE`, `seen_obrace = true`
   - anything else (or end of chunk) → `state = KEY`, consume nothing
4. If `parser.top_obj == nil`: create the root container with
   `ucl_parser_add_container(nil, parser, is_array = (state==VALUE), level = 0, has_obrace = seen_obrace)`
   and set `top_obj = cur_obj = obj`.
   (If `top_obj` already exists — i.e. a second chunk or an include — no container is
   created and the existing stack is reused.)

Note: `ucl_parser_add_container` itself overwrites `parser.state` — `KEY` for objects,
`VALUE` for arrays. So for a bare `{`, `KEY_OBRACE` set in step 3 is immediately replaced
by `KEY` in step 4 **only when `top_obj` was nil**. When `top_obj` already exists (an
included file starting with `{`), `state` stays `KEY_OBRACE`. This is exactly how
`.include` of a braced file works (see §7.3 and `tests/basic/23.in`).

#### `UCL_STATE_KEY_OBRACE`

One extra action, then falls through to `KEY`:

```c
parser->stack->e.params.flags |= UCL_STACK_HAS_OBRACE | UCL_STACK_AUTOMATIC;
/* FALLTHROUGH */
```

Meaning: "the container currently on top of the stack was opened by a `{` that belongs to
an *inserted* chunk, so when the matching `}` arrives, clear the flags instead of popping
the frame". See `AFTER_VALUE` below.

#### `UCL_STATE_KEY`

1. Skip `WHITESPACE_UNSAFE`.
2. If at end of chunk **or** current byte is `'}'` → `state = AFTER_VALUE`, `continue`
   (re-dispatch without advancing).
3. If `parser.stack == nil` → error `UCL_ESYNTAX`,
   `"top object is finished but the parser expects a key"` → `ERROR`, return false.
4. `got_content = false`; run `ucl_parse_key` (§3.1). On failure → `ERROR`, return false.
5. If `end_of_object` → resync `p`, `state = AFTER_VALUE`, `continue`.
6. Else if `parser.state != MACRO_NAME` (i.e. `ucl_parse_key` did not see a `.`):
   - if `next_key && stack.obj.type == UCL_OBJECT` → nested-object shorthand:
     `ucl_parser_add_container(cur_obj, parser, is_array=false, level = stack.level + 1,
      has_obrace = (state == KEY_OBRACE))`.
     (This is `key1 key2 { ... }` → `key1 { key2 { ... } }`.)
   - else if `got_content` → `state = VALUE`.
   - else (no content read, e.g. only comments were consumed) → leave state as is.
7. Else (`state == MACRO_NAME`) → `c = chunk.pos` (start of the macro name).
8. Resync `p`.

#### `UCL_STATE_VALUE`

If `parser.cur_obj == nil` → `ERROR`, return false.
Otherwise run `ucl_parse_value` (§4). It sets the next state itself:
`AFTER_VALUE` for scalars, or (for `{` / `[`) `KEY` / `VALUE` via `ucl_parser_add_container`.

#### `UCL_STATE_AFTER_VALUE`

Run `ucl_parse_after_value` (§1.5). On failure → `ERROR`, return false. Then:

- if `parser.stack != nil`: `state = KEY` if `stack.obj.type == UCL_OBJECT`, else `VALUE`.
- if `parser.stack == nil`: **return true immediately** — everything after the top-level
  container is closed is silently ignored.

#### `UCL_STATE_MACRO_NAME`

Two modes.

**If `UCL_PARSER_DISABLE_MACRO` is set:** run `ucl_skip_macro_as_comment` (§7.6).
On success, resync `p` and `state = prev_state`. On failure:

```
"error at %s:%d at column %d: invalid macro"
   fname_or("memory"), chunk.line, chunk.column
```
→ `ERROR`, return false.

**Otherwise:** consume the macro name one byte at a time:

- while the current byte is **not** `WHITESPACE_UNSAFE` and **not** `'('` → consume it.
- when a terminator is hit:
  - if `c != nil && p > c`: the name is `[c, p)`.
    Look it up in the macro table (exact byte match, case sensitive).
    - not found →
      ```
      "error at %s:%d at column %d: unknown macro: '%.*s', character: '%c'"
         fname_or("memory"), chunk.line, chunk.column, len(name), name, *chunk.pos
      ```
      → `ERROR`, return false.
    - found → `SKIP_SPACES_COMMENTS` (skip `WHITESPACE_UNSAFE`, then if the next two
      bytes start a comment, skip comments once), `state = MACRO`.
  - else →
    ```
    "error at %s:%d at column %d: invalid macro name"
    ```
    → `ERROR`, return false.

Note the byte-at-a-time consumption means this state is re-entered from the driver loop
for every character of the macro name.

#### `UCL_STATE_MACRO`

1. If the current byte is `'('` → `macro_args = ucl_parse_macro_arguments` (§7.5),
   resync `p`; if args were produced, `SKIP_SPACES_COMMENTS`.
   Else `macro_args = nil`.
2. `ucl_parse_macro_value` (§7.4) → `macro_start`, `macro_len`. On failure → `ERROR`.
3. `macro_len = ucl_expand_variable(parser, &macro_escaped, macro_start, macro_len)` (§8).
4. `state = prev_state` — **before** the handler runs, so a handler that pushes a nested
   chunk resumes in the right state.
5. Dispatch:
   - `macro_escaped == nil && macro != nil` → call with the raw `macro_start`/`macro_len`.
   - `macro != nil` → call with the expanded buffer, then free it.
   - `macro == nil` → `UCL_EINTERNAL`, `"internal error: parser has macro undefined"`.
   Context macros get `(data, len, args, parser.top_obj, ud)`;
   plain macros get `(data, len, args, ud)`.
6. **`chunk = parser.chunks; p = chunk.pos`** — the handler may have pushed/popped chunks.
7. Unref `macro_args`. If the handler returned false → return false (note: the parser
   state is *not* forced to `ERROR` here).

#### `UCL_STATE_ERROR`

Return false immediately.

#### default

`UCL_EINTERNAL`, `"internal error: parser is in an unknown state"`.

### 1.4 Loop epilogue (runs when `chunk.pos == chunk.end`)

```c
if (parser->last_comment) {
    if (parser->cur_obj)                 ucl_attach_comment(parser, parser->cur_obj, true);
    else if (parser->stack && parser->stack->obj)
                                          ucl_attach_comment(parser, parser->stack->obj, true);
    else if (parser->top_obj)            ucl_attach_comment(parser, parser->top_obj, true);
    else                                  ucl_object_unref(parser->last_comment);
}
```

Then the unmatched-brace check. Walk the stack from the top; **stop at the first frame
whose `chunk` pointer is not the current chunk** (frames belonging to an outer chunk are
not our problem). For every frame still carrying `UCL_STACK_HAS_OBRACE`, *append* to the
error string (this is `utstring_printf`, not `ucl_create_err`, so multiple messages
concatenate):

```
"%s:%d unmatched open brace at %d; "
   fname_or("memory"), chunk.line /* current */, st.line /* where the brace was */
```

If any were appended: `err_code = UCL_EUNPAIRED`, return false. Otherwise return true.

This check is skipped entirely when `parser.state == UCL_STATE_ERROR`.

### 1.5 `ucl_parse_after_value`

Loop over the chunk:

| byte class | action |
|---|---|
| `WHITESPACE` (`\t`, `' '`) | consume |
| starts a comment (`#`, or `/` `*`) — needs `chunk.remain >= 2` | `ucl_skip_comments`; **set `got_sep = true`** (a comment counts as a separator); resync |
| `VALUE_END` (`\0 \n \r # , ; ] }`) | see below |
| anything else | if `!got_sep` → `UCL_ESYNTAX` `"delimiter is missing"`; else **return true** (leave the byte for the next state) |

`VALUE_END` handling:

- `'}'` or `']'`:
  - `parser.stack == nil` → `"end of array or object detected without corresponding start"`.
  - type must match (`}`↔`UCL_OBJECT`, `]`↔`UCL_ARRAY`), else
    `"unexpected terminating symbol detected"`.
  - `st = parser.stack`. If `!(st.flags & UCL_STACK_HAS_OBRACE)`:
    ```
    err_code = UCL_EUNPAIRED
    "%s:%d object closed with } is not opened with { at line %d"
       fname_or("memory"), parser.chunks.line, st.line
    ```
    return false. (Note the message says `}` even for `]`.)
  - if `st.flags & UCL_STACK_AUTOMATIC` → `st.flags = 0` (**keep the frame**),
    else → pop the frame.
  - if `cur_obj != nil` → `ucl_attach_comment(parser, cur_obj, before = true)`.
  - **Collapse the implicit nesting chain**: while `parser.stack != nil`:
    ```
    st = parser.stack
    if st.next == nil                                 break
    if st.next.level == st.level                      break
    parser.stack = st.next;  parser.cur_obj = st.obj;  free(st)
    ```
    This unwinds the frames created by the `key1 key2 {}` shorthand (each of which was
    pushed with `level+1`), stopping when two adjacent frames share a level.
  - if `parser.stack == nil` → **return true** (ignore the rest of the input).
    Else consume the closing byte. `got_sep = true`.
- any other `VALUE_END` byte (`\0 \n \r # , ;`) → `got_sep = true`, consume.

Note `#` is both `VALUE_END` and a comment start; the comment branch is tested first
(it requires `remain >= 2`), so a `#` as the very last byte of a chunk falls into
the `VALUE_END` branch.

### 1.6 Stack frames and nesting

```c
enum ucl_stack_flags {
    UCL_STACK_HAS_OBRACE = 1u<<0,   /* opened by an explicit '{' or '[' */
    UCL_STACK_AUTOMATIC  = 1u<<1,   /* frame belongs to an inserted/included chunk */
    UCL_STACK_MAX        = 1u<<2,
};

struct ucl_stack {
    ucl_object_t *obj;
    struct ucl_stack *next;         /* singly-linked, prepend = push */
    union { struct { uint16_t level; uint16_t flags; uint32_t line; } params;
            uint64_t len; } e;
    struct ucl_chunk *chunk;        /* the chunk that pushed this frame */
};
```

`ucl_parser_add_container(obj, parser, is_array, level, has_obrace)`:

1. If `obj == nil` → allocate a new `UCL_ARRAY`/`UCL_OBJECT` with
   `priority = parser.chunks.priority`.
2. Else (reusing an existing object, e.g. duplicate-merge):
   - if `obj.type` is the *opposite* container type →
     `UCL_EMERGE`, `"cannot merge an object with an array"`, return nil.
   - else reuse it and force `obj.type` to the requested container type.
     (**A scalar is silently converted to a container here** — `key = 1; key = { a = 2 }`
     under MERGE turns `key` into an object.)
3. If object: create the hash map if absent (`ignore_case = UCL_PARSER_KEY_LOWERCASE`)
   and set `parser.state = UCL_STATE_KEY`.
   If array: `parser.state = UCL_STATE_VALUE`.
4. If `level >= 65535` (`UINT16_MAX`) →
   `UCL_ENESTED`, `"objects are nesting too deep (over 65535 limit)"`, return nil.
5. Push a frame: `obj`, `level`, `line = parser.chunks.line`, `chunk = parser.chunks`,
   `flags = UCL_STACK_HAS_OBRACE if has_obrace else 0`. Set `parser.cur_obj = obj`.

`level` semantics: the root frame is level 0. Explicit `{`/`[` inside a value reuse the
*current* frame's level (`ucl_parse_value` passes `parser->stack->e.params.level`, not
`+1`). Only the `key1 key2 {}` shorthand uses `level + 1`. That is what makes the
`AFTER_VALUE` collapse loop terminate correctly.

### 1.7 State transition table

`ws` = `WHITESPACE_UNSAFE`. "→X" means `parser.state` becomes X.

| From | Lookahead / condition | Consumes | To |
|---|---|---|---|
| INIT | comments, ws | comments + ws | INIT (falls through same iteration) |
| INIT | `[` | `[` | VALUE (root array created) |
| INIT | `{` | `{` | KEY_OBRACE, then KEY if root created here |
| INIT | anything else / EOF | — | KEY |
| KEY_OBRACE | — | — | (sets HAS_OBRACE\|AUTOMATIC) → KEY logic |
| KEY | ws | ws | KEY |
| KEY | EOF or `}` | — | AFTER_VALUE |
| KEY | `.` (macro enabled) | `.` | MACRO_NAME (prev_state = KEY/KEY_OBRACE) |
| KEY | key chars then `KEY_SEP` | key + separator + ws/comments | VALUE |
| KEY | key then more keys then `{`/`[` | keys | KEY (new container pushed at level+1) |
| KEY | `}` found by `ucl_parse_key` | — | AFTER_VALUE |
| KEY | invalid char | — | ERROR |
| VALUE | `"` | quoted string | AFTER_VALUE |
| VALUE | `'` | squoted string | AFTER_VALUE |
| VALUE | `{` | `{` | KEY (container pushed, HAS_OBRACE) |
| VALUE | `[` | `[` | VALUE (array pushed, HAS_OBRACE) |
| VALUE | `]` and top of stack is an array | — | AFTER_VALUE (the `]` is left for AFTER_VALUE) |
| VALUE | `<<TERM\n` | heredoc | AFTER_VALUE |
| VALUE | `VALUE_DIGIT_START` and parses as a number | number | AFTER_VALUE |
| VALUE | otherwise | unquoted atom | AFTER_VALUE |
| AFTER_VALUE | ws / separators / comments | those | KEY (object) or VALUE (array) |
| AFTER_VALUE | `}`/`]` matching top frame | that byte | pops frame → KEY/VALUE, or **return true** if stack empties |
| AFTER_VALUE | non-separator without a preceding separator | — | ERROR ("delimiter is missing") |
| MACRO_NAME | non-ws, non-`(` | 1 byte | MACRO_NAME |
| MACRO_NAME | ws or `(`, name found | ws + comments | MACRO |
| MACRO_NAME | DISABLE_MACRO | whole macro line | prev_state |
| MACRO | — | args + value + trailing `;`/ws | prev_state (KEY or KEY_OBRACE) |
| any | error | — | ERROR |
| ERROR | — | — | ERROR (returns false) |

---

## 2. Chunk management, position tracking, error formats

### 2.1 `struct ucl_chunk`

```c
struct ucl_chunk {
    const unsigned char *begin, *end, *pos;
    char *fname;                    /* strdup(parser->cur_file) at push time, or NULL */
    size_t remain;                  /* end - pos, maintained incrementally */
    unsigned int line;              /* 1-based */
    unsigned int column;            /* 0-based */
    unsigned priority;
    enum ucl_duplicate_strategy strategy;
    enum ucl_parse_type parse_type;
    struct ucl_parser_special_handler_chain *special_handlers;
    struct ucl_chunk *next;         /* singly linked; parser->chunks is the TOP */
};
```

Chunks form a stack (`LL_PREPEND`). `parser->chunks` is always the chunk being parsed.
`parser->recursion` is incremented on every push in `ucl_parser_add_chunk_full` and
decremented by the include handler / `ucl_parser_insert_chunk` when they pop.

Initial values on push: `line = 1`, `column = 0`, `pos = begin`, `remain = len`.

### 2.2 The advance primitive

```c
#define ucl_chunk_skipc(chunk, p) do {                \
    if (p == chunk->end) break;                       \
    if (*(p) == '\n') { (chunk)->line++; (chunk)->column = 0; }  \
    else (chunk)->column++;                           \
    (p++); (chunk)->pos++; (chunk)->remain--;         \
} while (0)
```

Exactly this: `\n` bumps the line and zeroes the column; everything else (including `\r`
and tabs) bumps the column by one. Column counts **bytes**, not runes.

Two places bypass the macro and patch the counters by hand — reproduce them literally:

- **numbers** (`ucl_lex_number`): `remain -= n; column += n; pos = newpos` — no line
  counting, so a number can never contain a newline (true).
- **heredocs**: entering, `remain -= (p - c + 1); pos = p + 1; column = 0; line++`;
  on the terminator, `remain -= term_len; pos = p + term_len; column = term_len`
  (the column is *set*, not added — an oddity worth keeping).

### 2.3 Error formatting

The parser error is a single string, produced once (`ucl_create_err` is a no-op if an
error string already exists — **the first error wins**). `ucl_parser_clear_error` resets
both the string and `err_code`.

`ucl_set_err(parser, code, str)` builds the message:

```
if (chunk->pos < chunk->end):
    if isgraph(*chunk->pos):
        "error while parsing %s: line: %d, column: %d - '%s', character: '%c'"
    else:
        "error while parsing %s: line: %d, column: %d - '%s', character: '0x%02x'"
    args: filename, chunk->line, chunk->column, str, *chunk->pos
else:
    "error while parsing %s: at the end of chunk: %s"
    args: filename, str
```

`filename` is `parser->cur_file` if set, otherwise the literal `"<unknown>"`.
`isgraph` = printable and not space, i.e. bytes `0x21..0x7E` in the C locale.
`%02x` is lowercase hex, zero padded to 2.

`ucl_set_err` also sets `parser->err_code = code` and `parser->state = UCL_STATE_ERROR`.

Messages built directly with `ucl_create_err` (bypassing `ucl_set_err`) do **not** carry
the `error while parsing ...` prefix and do **not** set the state to `ERROR`; they use
`chunk->fname` (falling back to the literal `"memory"`) rather than `parser->cur_file`:

```
"error at %s:%d at column %d: invalid macro"
"error at %s:%d at column %d: invalid macro name"
"error at %s:%d at column %d: unknown macro: '%.*s', character: '%c'"
"%s:%d object closed with } is not opened with { at line %d"
"%s:%d unmatched open brace at %d; "          /* appended, possibly repeatedly */
"maximum include nesting limit is reached: %d"
"invalid chunk added"
"invalid string added"
"cannot allocate chunk structure"
"call for external handler failed"
"a parser is in an invalid state"
```

### 2.4 Complete catalogue of `ucl_set_err` messages

All of these are wrapped by the `error while parsing ...` template above.

| Code | Message | Raised by |
|---|---|---|
| ENESTED | `unfinished multiline comment` | `ucl_skip_comments` |
| ESYNTAX | `numeric value out of range` | number lexer, on `ERANGE` |
| ESYNTAX | `unexpected newline` | `"` string, raw `\n` |
| ESYNTAX | `unexpected control character` | `"` string, byte `< 0x1F` other than `\n` |
| ESYNTAX | `unfinished escape character` | `"` or `'` string, `\` at end of chunk |
| ESYNTAX | `invalid utf escape` | `\u` followed by a non-hex digit |
| ESYNTAX | `no quote at the end of json string` | unterminated `"` |
| ESYNTAX | `no quote at the end of single quoted string` | unterminated `'` |
| ESYNTAX | `unexpected end of input while parsing key` | `ucl_parse_key` at EOF |
| ESYNTAX | `key must begin with a letter` | first key byte not `KEY_START`/`"`/`}`/`.` |
| ESYNTAX | `invalid character in a key` | unquoted key byte neither `KEY` nor `KEY_SEP` |
| ESYNTAX | `unfinished key` | ran off the chunk after reading key content |
| ESYNTAX | `unexpected '=' character` | second `=`/`:` after a key |
| ESYNTAX | `unexpected ':' character` | ditto |
| ESYNTAX | `empty keys are not allowed` | key length 0 after copy/unescape |
| ESYNTAX | `object value must be a part of an object` | `{` with no container |
| ESYNTAX | `array value must be a part of an object` | `[` with no container |
| ESYNTAX | `multiline value must be a part of an object` | `<` with no container |
| ESYNTAX | `value must be a part of an object` | scalar with no container |
| ESYNTAX | `unterminated multiline value` | heredoc without terminator |
| ESYNTAX | `string value must not be empty` | unquoted atom of length ≤ 0 |
| ESYNTAX | `end of array or object detected without corresponding start` | `}`/`]` with empty stack |
| ESYNTAX | `unexpected terminating symbol detected` | `}` closing an array or `]` closing an object |
| ESYNTAX | `delimiter is missing` | two values with no separator |
| ESYNTAX | `top object is finished but the parser expects a key` | `KEY` with empty stack |
| ESYNTAX | `unexpected end of input while parsing macro value` | `MACRO` at EOF |
| ESYNTAX | `unterminated macro value, missing '}'` | `.macro { ...` unterminated |
| ESYNTAX | `macro arguments parsing error` | args sub-parser failed |
| ENESTED | `objects are nesting too deep (over 65535 limit)` | `level >= UINT16_MAX` |
| EMERGE | `cannot merge an object with an array` | container type clash |
| EMERGE | `duplicate element for key '%s' found` | `UCL_DUPLICATE_ERROR` |
| EINTERNAL | `cannot allocate memory for a string` | OOM |
| EINTERNAL | `cannot allocate memory for an object` | OOM |
| EINTERNAL | `internal error: parser has macro undefined` | macro slot nil in `MACRO` |
| EINTERNAL | `internal error: parser is in an unknown state` | default arm |

Include/macro errors from `ucl_util.c` (plain `ucl_create_err`, no prefix):

```
"cannot open file %s: %s"                              (filename, strerror(errno))
"cannot stat file %s: %s"
"file %s is not a regular file"
"cannot mmap file %s: %s"
"cannot stat fd %d: %s"
"cannot mmap fd %d: %s"
"trying to include the file %s from itself"
"cannot match any files for pattern %s"
"cannot find file: %.*s in search path"
"Conflicting type for key: %s, asked %s, has %s"
"invalid URL %s: %s"  /  "cannot fetch URL %s: %s"  /  "error fetching URL %s: %s"
"URL support is disabled"
"Invalid priority value in macro: %s"
"Unable to parse priority macro"
"No Key specified in load macro"
"Key %s already exists"
"Unable to parse load macro"
"Unable to find inherited object %.*s"
"Invalid inherit context"
"cannot check signatures without openssl"
```

Note the inconsistent capitalisation (`Unable`, `Invalid`, `No Key`, `Key %s`) — keep it.

### 2.5 `ucl_parser_add_chunk_full`

```
ucl_parser_add_chunk_full(parser, data, len, priority, strat, parse_type) -> bool
```

1. `parser == nil` → false.
2. `data == nil && len != 0` → `"invalid chunk added"`, false.
3. If `parser.state == UCL_STATE_ERROR` → fall to the tail: `"a parser is in an invalid state"`, false.
4. Allocate the chunk. Run every registered **special handler** in registration order:
   a handler applies if it has `UCL_SPECIAL_HANDLER_PREPROCESS_ALL` or if
   `len >= magic_len && data[:magic_len] == magic`. Each handler rewrites `(data, len)`;
   the replacements are recorded on `chunk.special_handlers` (prepended, so freeing is
   reverse order). A handler returning false →
   `"call for external handler failed"`, false.
5. `parse_type == UCL_PARSE_AUTO && len > 0` → sniff the first byte:
   `data[0] & 0x80` → `UCL_PARSE_MSGPACK`; `data[0] == '('` → `UCL_PARSE_CSEXP`;
   otherwise `UCL_PARSE_UCL`.
6. Initialise the chunk (`line=1`, `column=0`, `priority`, `strategy`, `parse_type`),
   `fname = strdup(parser.cur_file)` if set, push it, `parser.recursion++`.
7. `recursion > 16` → `"maximum include nesting limit is reached: %d"` with the
   *current* recursion value, return false. **The chunk stays pushed.**
8. If `len > 0` → dispatch to `ucl_state_machine` / `ucl_parse_msgpack` / `ucl_parse_csexp`.
   If `len == 0` → if `top_obj == nil`, create an empty `UCL_OBJECT` with the given
   priority as `top_obj`; return true. (This is why `.include()` with empty parens
   yields an empty args object, and why adding an empty string still produces a result.)

Chunks are **not** popped by `add_chunk_full`; the caller pops (include handlers,
`ucl_parser_insert_chunk`). Top-level `ucl_parser_add_string`/`add_file` leave their
chunk on the list until `ucl_parser_free` (the data must therefore stay alive — this is
what `UCL_PARSER_ZEROCOPY` is about).

Convenience wrappers:

```
ucl_parser_add_chunk(p, d, l)           = add_chunk_full(p,d,l, p.default_priority, APPEND, UCL_PARSE_UCL)
ucl_parser_add_chunk_priority(p,d,l,pr) = add_chunk_full(p,d,l, pr,                 APPEND, UCL_PARSE_UCL)
ucl_parser_add_string(p, s, l)          = add_string_priority(p, s, l, p.default_priority)
ucl_parser_add_string_priority(p,s,l,pr): s==nil -> "invalid string added"; l==0 -> l=strlen(s);
                                          then add_chunk_priority
```

### 2.6 `ucl_parser_insert_chunk`

Used to splice a chunk into an in-progress parse (this is what a user macro would use).

```
if parser == nil || parser.top_obj == nil: return false
saved_state = parser.state
parser.state = UCL_STATE_INIT
if parser.stack != nil && parser.stack.next != nil:
    parser.stack.level = parser.stack.next.level    /* stop the inserted chunk from closing us */
res = add_chunk_full(parser, data, len,
                     parser.chunks.priority, parser.chunks.strategy, parser.chunks.parse_type)
pop and free the top chunk; parser.recursion--
parser.state = saved_state
return res
```

---

## 3. Lexical rules

### 3.1 Character classes

`ucl_test_character(c, mask) == (ucl_chartable[c] & mask) != 0`. Bits:

```
DENIED            = 1<<0    KEY               = 1<<1    KEY_START      = 1<<2
WHITESPACE        = 1<<3    WHITESPACE_UNSAFE = 1<<4    VALUE_END      = 1<<5
VALUE_STR         = 1<<6    VALUE_DIGIT       = 1<<7    VALUE_DIGIT_START = 1<<8
ESCAPE            = 1<<9    KEY_SEP           = 1<<10   JSON_UNSAFE    = 1<<11
UCL_UNSAFE        = 1<<12
```

The table verbatim (dumped from the compiled header — reproduce exactly, e.g. as a
`[256]uint16` literal):

| bytes | classes |
|---|---|
| `0x00` | VALUE_END, JSON_UNSAFE, UCL_UNSAFE |
| `0x01`–`0x07` | DENIED |
| `0x08` (`\b`) | JSON_UNSAFE, UCL_UNSAFE |
| `0x09` (`\t`) | WHITESPACE, WHITESPACE_UNSAFE, KEY_SEP, JSON_UNSAFE, UCL_UNSAFE |
| `0x0a` (`\n`) | WHITESPACE_UNSAFE, VALUE_END, JSON_UNSAFE, UCL_UNSAFE |
| `0x0b` (`\v`) | WHITESPACE_UNSAFE |
| `0x0c` (`\f`) | WHITESPACE_UNSAFE, JSON_UNSAFE, UCL_UNSAFE |
| `0x0d` (`\r`) | WHITESPACE_UNSAFE, VALUE_END, JSON_UNSAFE, UCL_UNSAFE |
| `0x0e`–`0x1f` | DENIED |
| `0x20` (space) | WHITESPACE, WHITESPACE_UNSAFE, VALUE_STR, KEY_SEP, UCL_UNSAFE |
| `!` | VALUE_STR |
| `"` | VALUE_STR, ESCAPE, JSON_UNSAFE, UCL_UNSAFE |
| `#` | VALUE_END |
| `$ % & ' ( ) *` | VALUE_STR |
| `+` | VALUE_STR, VALUE_DIGIT, UCL_UNSAFE |
| `,` | VALUE_END |
| `-` | KEY, VALUE_STR, VALUE_DIGIT, VALUE_DIGIT_START |
| `.` | KEY, VALUE_STR, VALUE_DIGIT |
| `/` | KEY, KEY_START, VALUE_STR, ESCAPE |
| `0`–`9` | KEY, KEY_START, VALUE_STR, VALUE_DIGIT, VALUE_DIGIT_START |
| `:` | VALUE_STR, KEY_SEP, UCL_UNSAFE |
| `;` | VALUE_END |
| `<` | VALUE_STR |
| `=` | VALUE_STR, KEY_SEP, UCL_UNSAFE |
| `> ? @` | VALUE_STR |
| `A`–`Z`, `a`–`z` | KEY, KEY_START, VALUE_STR, VALUE_DIGIT |
| `b f n r t u` (lowercase only) | additionally ESCAPE |
| `[` | VALUE_STR, UCL_UNSAFE |
| `\` | VALUE_STR, ESCAPE, JSON_UNSAFE, UCL_UNSAFE |
| `]` | VALUE_END |
| `^` | VALUE_STR |
| `_` | KEY, KEY_START, VALUE_STR |
| `` ` `` | VALUE_STR |
| `{` | VALUE_STR, UCL_UNSAFE |
| `\|` | VALUE_STR |
| `}` | VALUE_END |
| `~` | VALUE_STR |
| `0x7f` | DENIED |
| `0x80`–`0xff` | KEY, KEY_START, VALUE_STR |

Derived sets, for reference:

- `WHITESPACE` = `{\t, ' '}` — **only two bytes**. Used for "spaces on a line".
- `WHITESPACE_UNSAFE` = `{\t, \n, \v, \f, \r, ' '}` — all whitespace.
- `VALUE_END` (= "atom end") = `{0x00, \n, \r, #, ,, ;, ], }}`. Note `}` and `]` end
  atoms but `{`/`[` do not.
- `KEY_START` = `/`, `0`–`9`, `A`–`Z`, `a`–`z`, `_`, `0x80`–`0xff`.
- `KEY` = `KEY_START` plus `-` and `.`.
- `KEY_SEP` = `{\t, ' ', :, =}`.
- `VALUE_DIGIT_START` = `{-, 0..9}` — decides whether the value lexer *tries* a number.
- `ESCAPE` = `{", /, \, b, f, n, r, t, u}` — the set of legal characters after `\`
  in a double-quoted string.
- `DENIED` is never tested by the parser (only `UCL_CHARACTER_DENIED` exists as a bit);
  emitters use `JSON_UNSAFE`/`UCL_UNSAFE`.
- `JSON_UNSAFE` / `UCL_UNSAFE` are used by the emitter; the parser only reads
  `UCL_UNSAFE` inside `ucl_lex_json_string` to set the `ucl_escape` flag, which in turn
  sets `UCL_OBJECT_NEED_KEY_ESCAPE` on keys.

### 3.2 Comments — `ucl_skip_comments`

Called from `INIT`, `ucl_parse_key` (twice), `ucl_parse_value`, `ucl_parse_after_value`,
and `SKIP_SPACES_COMMENTS`. It loops (via a `goto start`) so consecutive comments are all
consumed in one call.

**Line comments (`#`):**
Only if `parser.state` is neither `SCOMMENT` nor `MCOMMENT` (dead condition — those states
are never assigned, so it is always true). Record `beg = p`; consume until `\n`.
On `\n`: if `SAVE_COMMENTS`, save `[beg, p)` (**not** including the newline), clear `beg`,
consume the `\n`, and restart the whole function. If the chunk ends first, fall through
to the tail save.

**Block comments (`/* ... */`), requires `remain >= 2`:**
`beg = p`, `comments_nested = 1`, consume `/*`. Then, with a `quoted` toggle:

```
if *p == '"' && p[-1] != '\\'   -> quoted = !quoted; consume
else if quoted                  -> consume
else if remain>=2 && *p=='*' && p[1]=='/' -> consume 2; nested--;
                                   if nested==0 { save [beg, p+1) if SAVE_COMMENTS; restart }
else if remain>=2 && *p=='/' && p[1]=='*' -> consume 2; nested++
else                            -> consume
```

Notes and gotchas that must be reproduced:

- **Nesting is real**: `/* /* */ */` is one comment. `tests/basic/comments.in` relies on
  five levels of nesting on one line.
- **Double quotes suppress `*/` detection** inside a block comment, so
  `/* key = "*/value" */` is a single comment. `p[-1]` is read even at the start of the
  buffer — in Go, guard the index (use `p > start` as an extra condition; when `p` is the
  first byte the C reads the `/` of `/*`, which is never `\\`, so treating an
  out-of-range read as "not a backslash" is equivalent).
- The saved length for a block comment is `p - beg + 1`, i.e. **one byte past** the
  closing `/`. Since `p` has already advanced past `*/`, this captures the whole comment
  plus one trailing byte. This is what the C does; reproduce it if `SAVE_COMMENTS` output
  is being compared.
- If the chunk ends with `comments_nested != 0` →
  `UCL_ENESTED`, `"unfinished multiline comment"`, return false.
- Tail: `if (beg && p > beg && SAVE_COMMENTS) save [beg, p)` — this is the unterminated
  `#` comment at end of chunk.
- `ucl_lex_is_comment(c1, c2)` = `c1=='/' && c2=='*'`, or `c1=='#'` (c2 ignored).
  Every caller guards it with `chunk->remain >= 2`, so a `#` that is the last byte of a
  chunk is *not* treated as a comment start.
- `//` is **not** a comment.

### 3.3 Keys — `ucl_parse_key`

Outputs: `next_key`, `end_of_object`, `got_content`. Returns false on error.

**Step 0.** If at end of chunk →
`UCL_ESYNTAX` `"unexpected end of input while parsing key"`, set `end_of_object = true`,
**return true** (not false — the driver then goes to `AFTER_VALUE`, but the state is
already `ERROR`, so the loop returns false on the next dispatch).

**Step 1.** If the first byte is `.` and `DISABLE_MACRO` is not set: consume it,
`prev_state = state`, `state = MACRO_NAME`, `end_of_object = false`, return true.

**Step 2 — find the start of the key.** Loop with `c == nil`:

- comment start (`remain >= 2`) → skip comments, resync
- `WHITESPACE_UNSAFE` → consume
- `KEY_START` → `c = p`, consume, `got_content = true`
- `"` → `c = p + 1`, `got_quote = true`, `got_content = true`, consume
- `}` → `end_of_object = true`, return true
- `.` with macros enabled → consume, `prev_state = state`, `state = MACRO_NAME`, return true
  (note: `end_of_object` is **not** reset here — it keeps whatever the caller passed in;
  the driver checks `state != MACRO_NAME` first so this is harmless)
- otherwise → `UCL_ESYNTAX` `"key must begin with a letter"`, false

**Step 3 — body of the key** (`c != nil`):

- unquoted: `KEY` chars are consumed (`got_content = true`);
  a `KEY_SEP` char (`\t`, space, `:`, `=`) sets `end = p` and breaks;
  anything else → `UCL_ESYNTAX` `"invalid character in a key"`, false.
- quoted: run `ucl_lex_json_string` from the current position; on success
  `end = chunk.pos - 1` (the closing quote), `p = chunk.pos`, break.
  Keys parsed this way get `UCL_OBJECT_NEED_KEY_ESCAPE` if the string contained an escape
  or a `UCL_UNSAFE` byte.

**Step 4.** `if (p >= chunk.end && got_content)` → `"unfinished key"`, false.
`if (!got_content)` → return true with `end_of_object` unchanged (nothing was read;
this happens when the chunk contained only comments/whitespace).

**Step 5 — separator.** `end_of_object = false`. Loop:

- `WHITESPACE` (tab/space only) → consume
- `=` → if neither `got_eq` nor `got_semicolon` yet: consume, `got_eq = true`;
  else `"unexpected '=' character"`, false
- `:` → same with `got_semicolon`; error `"unexpected ':' character"`
- comment start (`remain >= 2`) → skip comments, resync
- anything else → break (start of the value)

Then `if (p >= chunk.end && got_content)` → `"unfinished key"`, false.
(The C tests the *pointer* `got_content` here, which is always non-NULL — so this is
effectively `if (p >= chunk->end)`. Reproduce the effective behaviour: error at EOF.)

**Step 6 — nested-key lookahead**, only when no `=`/`:` was seen:

```
t = p;  next_key = false
while (*t is WHITESPACE) t++          /* NOTE: no bounds check in C */
if (*t != '{' && *t != '[') {
    while (t < chunk->end) {
        if (*t == ',' || *t == ';' || *t == '\n' || *t == '\r') break;
        if (*t == '{' || *t == '[') { next_key = true; break; }
        t++;
    }
}
```

Interpretation: `key1 key2 { ... }` → `next_key = true` → the driver nests.
`key1 value` → the first terminator found is a newline/`;`/`,` → `next_key = false` →
linear key/value. If the value *is* directly `{` or `[`, `next_key` stays false (the value
parser handles the brace). In Go, bound the first `while` by `chunk.end`.

**Step 7 — build the object.** `nobj = ucl_object_new_full(UCL_NULL, chunk.priority)`.
`keylen = ucl_copy_or_store_ptr(parser, c, ..., end - c, need_unescape,
KEY_LOWERCASE, need_expand = false, unescape_squote = false)`.

- `keylen == -1` → false (allocation error already reported).
- `keylen == 0` → `UCL_ESYNTAX` `"empty keys are not allowed"`, false.

Set `nobj.key`, `nobj.keylen`, then `ucl_parser_process_object_element(parser, nobj)`
(§5). Finally set `UCL_OBJECT_NEED_KEY_ESCAPE` if `ucl_escape`.

**Keys are never variable-expanded.** Only values and macro bodies are.

### 3.4 Double-quoted strings — `ucl_lex_json_string`

Entered with `chunk.pos` **after** the opening `"`. Sets `need_unescape`, `ucl_escape`,
`var_expand`. Consumes the closing quote and returns true.

Per byte:

- `c < 0x1F` (strictly less than 0x1F — so `0x1F` itself is allowed!):
  `\n` → `"unexpected newline"`; anything else → `"unexpected control character"`. false.
- `\` → consume it. At end of chunk → `"unfinished escape character"`, false.
  Then look at the escaped character:
  - if it is in class `ESCAPE` (`" / \ b f n r t u`):
    - `u` → consume the `u`, then consume up to 4 bytes requiring `isxdigit` each;
      a non-hex digit → `"invalid utf escape"`, false; running off the chunk after the
      loop → `"unfinished escape character"`, false.
    - otherwise consume the one character.
  - if it is **not** in `ESCAPE` (e.g. `\q`, `\0`, `\e`): nothing is consumed here, and
    the loop `continue`s — so the offending character is re-examined as an ordinary
    character on the next iteration. `\q` therefore lexes fine and unescapes to `q`.
  - either way `need_unescape = true` and `ucl_escape = true`, then `continue`.
- `"` → consume, **return true**.
- byte in class `UCL_UNSAFE` → `ucl_escape = true` (then falls through to consume).
- `$` → `var_expand = true` (then consume).
- else consume.

Running off the end → `"no quote at the end of json string"`, false.

`str_len` at the call site is `chunk.pos - c - 2` where `c` pointed at the opening quote,
i.e. the content between the quotes.

**Unescaping** (`ucl_unescape_json_string`, in-place, tortoise/hare):

| escape | result |
|---|---|
| `\n` | LF |
| `\r` | CR |
| `\b` | BS |
| `\t` | TAB |
| `\f` | FF |
| `\\` | `\` |
| `\"` | `"` |
| `\uXXXX` | UTF-8 encoding of the code point, see below |
| `\` + anything else | that character, backslash dropped (`\/` → `/`, `\q` → `q`) |
| trailing lone `\` at the very end | kept as a literal `\` (issue #74) |

`\uXXXX`: only attempted if at least 4 bytes remain (`len > 3`); otherwise the literal
`u` is emitted and the escape is abandoned. The 4 hex digits are accumulated MSB-first;
a non-hex digit **breaks out of the digit loop but the accumulated value is still used**
and 4 source bytes are still consumed. Encoding:

```
uval < 0x80     -> 1 byte
uval < 0x800    -> 2 bytes  0xC0|(uval>>6), 0x80|(uval&0x3F)
uval < 0x10000  -> 3 bytes  0xE0|(uval>>12), 0x80|((uval>>6)&0x3F), 0x80|(uval&0x3F)
otherwise       -> the single byte '?'
```

Surrogate pairs are **not** decoded — `😀` produces two 3-byte sequences
(CESU-8), not one emoji. Values ≥ 0x10000 cannot arise from 4 hex digits, so the `?`
branch is unreachable. See `tests/basic/escapes.in` → `escapes.res`:
`ὤ0` becomes `ὤ` followed by a literal `0` (only 4 digits are consumed), and
`￿` becomes `￿`.

### 3.5 Single-quoted strings — `ucl_lex_squoted_string`

Entered after the opening `'`. Only two rules:

- `\` → consume it; at end of chunk → `"unfinished escape character"`, false;
  otherwise consume the next byte too, `need_unescape = true`, `continue`.
- `'` → consume, return true.
- everything else (including raw newlines and control characters) → consume.

Unterminated → `"no quote at the end of single quoted string"`.

The resulting object gets `UCL_OBJECT_SQUOTED`. `var_expand` is **not** set by this lexer,
but `ucl_parse_value` still passes the (false) `var_expand` through, so **single-quoted
strings are not variable-expanded**.

**Unescaping** (`ucl_unescape_squoted_string`):

| escape | result |
|---|---|
| `\'` | `'` |
| `\` + LF | nothing (line continuation) |
| `\` + CR | nothing; a following LF is also swallowed |
| `\` + anything else | **backslash kept**, i.e. two bytes `\` and the character |
| trailing lone `\` | kept as `\` |

`tests/basic/squote.in`/`.res` is the reference:
`'b\n\'a'` → `b\n'a` (the `\n` stays a literal backslash-n),
`'\<newline>aaa'` → `aaa`,
`'\0\e\\\\\\\\\<newline>\''` → `\0\e\\\\\\\\'`.

### 3.6 Heredocs / multiline strings

Triggered in `ucl_parse_value` when the current byte is `<` **and** `chunk.end - p > 3`
(so at least 4 bytes remain) **and** the next two bytes are `<<`.

Syntax: `<<` followed by one or more **uppercase ASCII letters only** (`A`–`Z`), then a
newline. Digits, lowercase and underscores are not allowed in the terminator.

```
p += 2
while p < end && 'A' <= *p <= 'Z': p++
if p == end          -> "unterminated multiline value", false
if *p != '\n'        -> fall through to ordinary string parsing (NOT an error)
```

If it is a newline: the terminator is `[c+2, p)`; then
`chunk.remain -= p - c + 1; chunk.pos = p + 1; chunk.column = 0; chunk.line++`
and `ucl_parse_multiline_string` runs:

```
c = p = chunk.pos;  newline = false;  len = 0
while p < chunk.end:
    if newline:
        if chunk.end - p < term_len:  return 0                  /* cannot fit */
        if p[:term_len] == term:
            tend = p + term_len
            if tend < chunk.end && *tend not in {'\n', ';', ','}:
                consume 1; continue                              /* not a real terminator */
            len = p - c
            chunk.remain -= term_len; chunk.pos = p + term_len; chunk.column = term_len
            *beg = c
            break
    if *p == '\n': newline = true
    else { if *p == '$' { *var_expand = true }; newline = false }
    consume 1
return len
```

Key consequences:

- The terminator is only recognised at the **start of a line** (`newline` is only true
  right after a `\n`), and the very first line can never be the terminator.
- The terminator must be followed by end-of-chunk, `\n`, `;` or `,`. `EOD` followed by
  anything else is treated as content (`tests/basic/heredoc_eod.in` covers this indirectly).
- `tests/basic/issue319.in` (`key = <<EOD\nvalue\nEOD` with no trailing newline) works
  because `tend == chunk->end` fails the `tend < chunk->end` guard.
- The returned `len` includes the `\n` that precedes the terminator; the caller stores
  `len - 1` bytes, dropping it. So `<<EOD\nvalue\nEOD` → `"value"`.
- `len == 0` (no terminator found, **or an empty heredoc**) → the caller reports
  `UCL_ESYNTAX` `"unterminated multiline value"`. An empty heredoc `<<EOD\nEOD\n` is
  therefore an error.
- The value gets `UCL_OBJECT_MULTILINE`, type `UCL_STRING`, no unescaping, but **variable
  expansion is applied** if a `$` was seen.

### 3.7 Unquoted values — `ucl_parse_string_value`

Scans forward tracking three independent brace counters (round/square/figure), each with
an open and a close count:

```
'{' -> figure.open++
'}' -> figure.close++; if figure.close <= figure.open { consume; continue }   /* not a terminator */
'[' -> square.open++
']' -> square.close++; if square.close <= square.open { consume; continue }
'$' -> var_expand = true
'\\'-> need_unescape = true; consume the backslash and (if present) the next byte; continue
then: if VALUE_END(*p) || (remain >= 2 && is_comment(p, p+1)) -> break
      else consume
```

The round-brace counter is declared and never touched — `(` and `)` are ordinary
characters in an unquoted value.

Effect: `}` and `]` terminate an unquoted atom **unless** they close a brace that was
opened inside the same atom. So `key = a{b}c` is the single string `a{b}c`, while
`obj { key = value }` terminates `value` at the `}`.

After the scan, `ucl_parse_value` strips trailing `WHITESPACE` (tab/space only, not
newlines — those are `VALUE_END` and already stopped the scan):

```c
stripped_spaces = 0;
while (ucl_test_character(*(chunk->pos - 1 - stripped_spaces), UCL_CHARACTER_WHITESPACE))
    stripped_spaces++;
str_len = chunk->pos - c - stripped_spaces;
```

This reads *backwards* with no lower bound. In Go, bound it at the atom start `c`.

`str_len <= 0` → `UCL_ESYNTAX` `"string value must not be empty"`.

---

## 4. Value parsing

`ucl_parse_value` first skips leading `WHITESPACE_UNSAFE` and comments (only if the
current byte is whitespace or starts a comment), then switches on the first byte:

| byte | handling |
|---|---|
| `"` | JSON string → `UCL_STRING`, unescape if needed, expand vars if `$` seen |
| `'` | single-quoted → `UCL_STRING` + `UCL_OBJECT_SQUOTED`, squote-unescape |
| `{` | `ucl_parser_get_container` then `ucl_parser_add_container(obj, false, stack.level, true)`; consume `{`; return (state set to KEY) |
| `[` | same with `is_array = true`; state set to VALUE |
| `]` | if the top of the stack is an array → `state = AFTER_VALUE`, return true, **without consuming** (empty array `[]`, or trailing comma `[1,]`). Otherwise fall through to `parse_string` |
| `<` | heredoc if `<<[A-Z]+\n`, else fall through to `parse_string` |
| other | `parse_string` |

`ucl_parser_get_container(parser)`:
- `parser`, `stack`, or `stack.obj` nil → nil.
- top of stack is an array → allocate a fresh `UCL_NULL` object with
  `priority = chunks.priority`, append it to the array, set `cur_obj`,
  `ucl_attach_comment(obj, before=false)`, return it.
- otherwise → return `parser.cur_obj` (the object created by `ucl_parse_key`).

`parse_string` path:

1. If `obj == nil`, get the container. Still nil → `"value must be a part of an object"`.
2. If the first byte is `VALUE_DIGIT_START` (`-` or `0`–`9`): try `ucl_lex_number`.
   - success → `state = AFTER_VALUE`, return true.
   - failure with `state == ERROR` (only `ERANGE`) → return false.
   - failure otherwise → **fall through to string parsing**. This is the critical
     "malformed number becomes a string" rule.
3. `ucl_parse_string_value`, strip trailing spaces, compute `str_len`.
4. Classify the resulting `[c, c+str_len)` byte range, in this exact order:
   - `str_len == 4 && bytes == "null"` → `UCL_NULL`, `len = 0` (case sensitive)
   - `str_len == 3 && bytes == "nan"` → `UCL_FLOAT`, value NaN, `len = 0` (case sensitive)
   - `str_len == 3 && bytes == "inf"` → `UCL_FLOAT`, value +Inf, `len = 0` (case sensitive)
   - `ucl_maybe_parse_boolean` (case **insensitive**, see below)
   - else `UCL_STRING` with unescape/expand as flagged.
5. `state = AFTER_VALUE`.

### 4.1 Booleans

`ucl_maybe_parse_boolean(obj, start, len)` — exact lengths, `strncasecmp`:

| len | literals |
|---|---|
| 5 | `false` → false |
| 4 | `true` → true |
| 3 | `yes` → true; `off` → false |
| 2 | `no` → false; `on` → true |

Nothing else. `t`/`f`/`y`/`n` alone are strings. `-inf` is a string (only bare `inf`).

### 4.2 Numbers — `ucl_maybe_parse_number`

```c
int ucl_maybe_parse_number(ucl_object_t *obj, const char *start, const char *end,
                           const char **pos, bool allow_double, bool number_bytes,
                           bool allow_time);
```

Returns `0` (success, `*pos` = one past the number), `EINVAL` (22, not a number) or
`ERANGE` (34). The parser calls it as

```c
ucl_maybe_parse_number(obj, chunk->pos, chunk->end, &pos,
                       /*allow_double*/ true, /*number_bytes*/ false,
                       /*allow_time*/ (parser->flags & UCL_PARSER_NO_TIME) == 0);
```

On `0`: `chunk->remain -= pos - chunk->pos; chunk->column += pos - chunk->pos; chunk->pos = pos`.
On `ERANGE`: `UCL_ESYNTAX` `"numeric value out of range"` (fatal).
On `EINVAL`: fall back to string parsing.

**Phase 1 — scan.** State: `got_dot`, `got_exp`, `need_double`, `is_time`,
`valid_start`, `is_hex`, `is_neg`. `p = c = start`.

```
if *p == '-': is_neg = 1; c++; p++
while p < end:
    if is_hex && isxdigit(*p):                p++
    elif isdigit(*p):                         valid_start = true; p++
    elif !is_hex && (*p=='x' || *p=='X'):     is_hex = true; allow_double = false; c = p+1; p++
    elif allow_double:
        if p == c:                            return EINVAL (*pos = start)   /* empty digit run */
        elif *p == '.':
            if got_dot: return EINVAL
            got_dot = true; need_double = true; p++
        elif *p == 'e' || *p == 'E':
            if got_exp: return EINVAL
            got_exp = true; need_double = true; p++
            if p >= end: return EINVAL
            if !isdigit(*p) && *p != '+' && *p != '-': return EINVAL
            p++
        else: break                            /* end of the numeric part */
    elif !allow_double && *p == '.':          return EINVAL
    else: break
if !valid_start || p == c:                    return EINVAL (*pos = start)
```

Note `+` is **not** accepted as a leading sign (only `-`), and there is no `0b`/`0o`
support. `is_hex` is only entered from an `x`/`X` — but nothing requires a preceding `0`,
so `x10` … no: `x` is reached only after the `isdigit` branch fails and `!is_hex`, and
`valid_start` would then be false, so `x10` fails at the `!valid_start` check. `0x10`
works. `00x10` works too.

**Phase 2 — convert.** Copy `[c, p)` into a 128-byte stack buffer, prefixed with `-`
when `is_neg`. If `p - c + 1 >= 128` → `EINVAL`. Then:

- `need_double` → `strtod(numbuf, &endptr)`
- else if `is_hex` → `strtoimax(numbuf, &endptr, 16)`
- else → `strtoimax(numbuf, &endptr, 10)`

`errno == ERANGE` → return `ERANGE`.

**Phase 3 — map `endptr` back into the input**:

```c
long shift = endptr - numbuf - is_neg;
endptr = (char *) c + shift;
if (endptr >= end)                                  { p = end;    goto set_obj; }
if (endptr == NULL || ucl_lex_is_atom_end(*endptr) || *endptr == '\0') { p = endptr; goto set_obj; }
```

**Phase 4 — suffixes** (only when `endptr < end && endptr != start`). `p = endptr`,
switch on `*p`:

`'m' 'M' 'g' 'G' 'k' 'K'`:
- if `end - p >= 2`:
  - `p[1]` is `s`/`S` → **milliseconds/seconds scaling**: promote to double if needed
    (`dv = lv`), `is_time = true`;
    if `*p` is `m`/`M` → `dv /= 1000.0`, else `dv *= mult_normal(*p)`; `p += 2`.
    Then if `end - p > 0 && !atom_end(*p)` → `EINVAL`. → set_obj.
    (`ks`/`gs` therefore mean "kilo-seconds"/"giga-seconds".)
  - else if `number_bytes || p[1] in {b, B}` → **bytes**: demote to int if needed
    (`lv = dv`), `lv *= mult_bytes(*p)`, `p += 2`, same trailing check → set_obj.
  - else if `atom_end(p[1])` → plain multiplier:
    `dv *= mult_normal` if `need_double` else `lv *= mult(*p, number_bytes)`; `p++` → set_obj.
  - else if `allow_time && end - p >= 3 && lower(p[0..2]) == "min"` → **minutes**:
    promote to double, `is_time = true`, `dv *= 60`, `p += 3`, trailing check → set_obj.
  - else → `break` out of the switch → `*pos = c; return EINVAL`.
- else (`end - p == 1`): plain multiplier as above, `p++`, trailing check → set_obj.

`'s' 'S'`: if `allow_time && (p == end-1 || atom_end(p[1]))` → promote to double,
`is_time = true`, `p++` → set_obj. (No scaling: seconds are the base unit.)
Otherwise break → `EINVAL`.

`'h' 'H' 'd' 'D' 'w' 'W' 'y' 'Y'`: if `allow_time && (p == end-1 || atom_end(p[1]))` →
promote to double, `is_time = true`, `dv *= time_mult(*p)`, `p++` → set_obj.
Otherwise break → `EINVAL`.

`'\t' ' '`: skip all `WHITESPACE`; if now at `end` or at an atom end → set_obj
(with `p` pointing past the spaces). Otherwise break → `EINVAL`.
(This is what makes `key = 10 ;` and `key = 10\n` work.)

anything else → break → `*pos = c; return EINVAL`.

Also, before the switch: `else if (endptr == end) { p = end; goto set_obj; }` — dead
because phase 3 already handled `endptr >= end`.

Multiplier tables:

```
ucl_lex_num_multiplier(c, is_bytes):   /* tolower(c) */
   'm' -> 1000*1000        or 1024*1024
   'k' -> 1000             or 1024
   'g' -> 1000*1000*1000   or 1024*1024*1024
   otherwise -> 1

ucl_lex_time_multiplier(c):            /* tolower(c) */
   'm' -> 60          'h' -> 3600      'd' -> 86400
   'w' -> 604800      'y' -> 31536000  (365 days)
   otherwise -> 1
```

**set_obj:**

```c
if (allow_double && (need_double || is_time)) {
    obj->type = is_time ? UCL_TIME : UCL_FLOAT;
    obj->value.dv = dv;
} else {
    obj->type = UCL_INT;
    obj->value.iv = lv;
}
*pos = p;  return 0;
```

Because `allow_double` is set to `false` the moment a hex prefix is seen, a hex literal
always yields `UCL_INT`.

### 4.3 Worked edge cases (all from `tests/basic/1.in` → `1.res`)

| input | result | why |
|---|---|---|
| `-0xdeadbeef` | `UCL_INT` `-3735928559` | `is_neg`, `c` moves past `x`, `numbuf = "-deadbeef"`, `strtoimax(...,16)` |
| `0xdeadbeef.1` | `UCL_STRING` `"0xdeadbeef.1"` | after the hex digits, `!allow_double && *p=='.'` → `EINVAL` → string fallback |
| `0xreadbeef` | `UCL_STRING` `"0xreadbeef"` | after `x`, `c = p+1` points at `r`; `r` is not a hex digit → break with `p == c` → `EINVAL` |
| `1.0` | `UCL_FLOAT` `1.0` | |
| `-1e-10` | `UCL_FLOAT` `-1e-10` | |
| `1` | `UCL_INT` `1` | |
| `value` | `UCL_STRING` | not `VALUE_DIGIT_START` |
| `no` / `yes` | `UCL_BOOLEAN` false / true | |
| `10s` (NO_TIME clear) | `UCL_TIME` `10.0` | |
| `10s` (NO_TIME set) | `UCL_STRING` `"10s"` | `allow_time` false → `EINVAL` → string |
| `10ms` (NO_TIME set) | **`UCL_TIME` `0.01`** | the `ms` branch is *not* guarded by `allow_time` — a C quirk that must be reproduced |
| `1kb` | `UCL_INT` `1024` | bytes branch |
| `1k` | `UCL_INT` `1000` | plain multiplier |
| `1.5k` | `UCL_FLOAT` `1500.0` | |
| `10min` | `UCL_TIME` `600.0` | |
| `1 ` (trailing space then `;`) | `UCL_INT` `1` | whitespace suffix branch |
| `12abc` | `UCL_STRING` | suffix switch default → `EINVAL` |
| `1.2.3` | `UCL_STRING` | second `.` → `EINVAL` |
| a 128+ digit number | `UCL_STRING` | `numbuf` overflow guard → `EINVAL` |
| `99999999999999999999` | error `numeric value out of range` | `ERANGE` is **fatal**, not a string fallback |

### 4.4 `UCL_PARSER_NO_TIME`

Sets `allow_time = false` in the number lexer, which:

- makes bare `s h d w y` suffixes and the `min` suffix fail → the atom becomes a string;
- does **not** affect `ms`/`ks`/`gs` (which still produce `UCL_TIME`), nor `k`/`m`/`g`/
  `kb`/`mb`/`gb`, nor plain integers/floats.

No `UCL_TIME` object is ever produced from a bare unit suffix under `NO_TIME`.

---

## 5. Duplicate keys

### 5.1 `ucl_parser_process_object_element`

Called from `ucl_parse_key` with the freshly created `nobj` (type `UCL_NULL`, value not
yet parsed). This is where duplicates are resolved — **before** the value is known, which
is why `UCL_DUPLICATE_MERGE` can hand the *existing* object back to the value parser.

```c
container = parser->stack->obj->value.ov;

DL_FOREACH(parser->stack->obj, cur) {          /* walk the implicit-array chain of the container */
    if (cur->type == UCL_OBJECT) {
        tobj = ucl_hash_search_obj(cur->value.ov, nobj);
        if (tobj != NULL) {
            if (cur->value.ov != container) { tobj = NULL; continue; }   /* different container: ignore */
            break;
        }
    }
}
```

The `DL_FOREACH` walks `stack->obj` and its `next` siblings — i.e. when the container
itself is one element of an implicit array (`section_a { } section_a { }`), the search
covers all of them but only accepts a hit in the *same* hash. This is exactly what makes
`tests/basic/issue312` produce two separate `section_a` blocks instead of merging them.

**Not found:** insert into the hash (`ignore_case = KEY_LOWERCASE`),
`nobj->prev = nobj; nobj->next = NULL;`, `stack->obj->len++`.

**Found:** `priold = priority(tobj)`, `prinew = priority(nobj)`, then by
`parser->chunks->strategy`:

#### `UCL_DUPLICATE_APPEND` (0, the default)

```
if (tobj->flags & UCL_OBJECT_INHERITED) prinew = priold + 1;   /* inherited values always lose */
if      (priold == prinew)  ucl_parser_append_elt(parser, container, tobj, nobj);
else if (priold >  prinew)  DL_APPEND(parser->trash_objs, nobj);   /* new value discarded */
else                      { ucl_hash_replace(container, tobj, nobj); unref(tobj); }
```

#### `UCL_DUPLICATE_MERGE` (1)

```
if (tobj->type == UCL_OBJECT || tobj->type == UCL_ARRAY) {
    unref(nobj);  nobj = tobj;                 /* reuse the existing container */
}
else if (priold == prinew)  ucl_parser_append_elt(...)
else if (priold >  prinew)  DL_APPEND(trash_objs, nobj)
else                      { ucl_hash_replace(container, tobj, nobj); unref(tobj); }
```

The first branch is the whole point: `cur_obj` becomes the *existing* object, so
`ucl_parse_value` writes into it. For `{...}` it calls `ucl_parser_add_container(tobj,...)`
which reuses the hash and merges keys; for `[...]` it appends to the existing array; for
a scalar it **overwrites the container in place**, converting it to a scalar.
The `UCL_OBJECT_INHERITED` bump is *not* applied under MERGE.

`tests/basic/19.in`/`19.res` is the authoritative example:

- `okey_merge` (object + object) → one object; the inner `key` collides at equal priority
  under the *merge* strategy → falls to `priold == prinew` → implicit array
  (`key = "value"; key = "value1";`) and `key1` is added.
- `akey_merge` (array + array) → one array `["value", "value3"]`.
- `skey_merge` (string + string) → implicit array `skey_merge = "value"; skey_merge = "value4";`.

#### `UCL_DUPLICATE_REWRITE` (2)

`ucl_hash_replace(container, tobj, nobj); unref(tobj);` — unconditional, priority ignored.
`19.res`: `okey_rewrite` keeps only the include's content.

#### `UCL_DUPLICATE_ERROR` (3)

```c
snprintf(errmsg, 256, "duplicate element for key '%s' found", nobj->key);
ucl_set_err(parser, UCL_EMERGE, errmsg, &parser->err);
return false;
```

Note `nobj->key` is not NUL-terminated in the zero-copy case; the C prints whatever
follows. In Go, print exactly `keylen` bytes of the key.

**Epilogue (all paths):**

```c
parser->stack->obj->value.ov = container;
parser->cur_obj = nobj;
ucl_attach_comment(parser, nobj, false);
```

### 5.2 `ucl_parser_append_elt` — implicit vs explicit arrays

```c
if ((parser->flags & UCL_PARSER_NO_IMPLICIT_ARRAYS) == 0) {
    top->flags |= UCL_OBJECT_MULTIVALUE;
    DL_APPEND(top, elt);                    /* doubly-linked chain via next/prev */
    parser->stack->obj->len++;
} else {
    if (top->flags & UCL_OBJECT_MULTIVALUE) {
        ucl_array_append(top, elt);          /* already converted */
    } else {
        nobj = ucl_object_typed_new(UCL_ARRAY);
        nobj->key = top->key;  nobj->keylen = top->keylen;
        nobj->flags |= UCL_OBJECT_MULTIVALUE;
        ucl_array_append(nobj, top);
        ucl_array_append(nobj, elt);
        ucl_hash_replace(cont, top, nobj);
    }
}
```

**Implicit array (default).** The hash still maps the key to the *first* object; the
duplicates hang off `next`. `prev` on the head points at the tail (`DL_APPEND`'s circular
prev convention). Emitters and iterators walk the `next` chain. `stack->obj->len` counts
every element, not distinct keys.

**`UCL_PARSER_NO_IMPLICIT_ARRAYS`.** The first duplicate converts the entry into a real
`UCL_ARRAY` (flagged `MULTIVALUE`) holding both values, and later duplicates are appended
to it. Note the new array object **does not copy the key trash** — it aliases
`top->key`/`top->keylen`, so the original must stay alive (it does: it is now an array
element). Also `stack->obj->len` is **not** incremented on this path, unlike the implicit
one — a real asymmetry in the C.

A subtlety: an entry converted by `NO_IMPLICIT_ARRAYS` carries `MULTIVALUE`, and so does
an array written literally as `key = [...]`? No — a literal array is built by
`ucl_parser_add_container` and never gets `MULTIVALUE`. So under `NO_IMPLICIT_ARRAYS`,
`key = [1]` followed by `key = 2` finds `tobj` of type `UCL_ARRAY` without `MULTIVALUE`
and *wraps it in another array*: `[[1], 2]`. Under the default (implicit) mode the same
input produces the chain `[1]` → `2`.

---

## 6. Priorities

**Storage.** The priority lives in the **top 4 bits** of the 16-bit `flags` field:

```c
#define PRIOBITS 4
unsigned ucl_object_get_priority(obj) { return obj->flags >> (16 - PRIOBITS); }        /* >> 12 */
void ucl_object_set_priority(obj, prio) {
    prio &= 0x0F;  prio <<= 12;
    prio |= obj->flags & 0x0FFF;
    obj->flags = prio;
}
```

So the effective range is **0..15**; anything larger is silently truncated (the header
comment says "0 .. 16", which is wrong). In Go, model `flags` as a `uint16` with the same
layout so that `ucl_object_get_priority` is bit-identical, or store the priority
separately but mask it to 4 bits on the way in.

**Where a priority comes from.**

1. `parser.default_priority` (set by `ucl_parser_set_default_priority`, default 0) is used
   by `ucl_parser_add_chunk` / `add_string` / `add_file`.
2. `ucl_parser_add_chunk_priority` / `add_file_priority` / `add_fd_priority` take it
   explicitly; it is stored on the chunk (`chunk.priority`).
3. The `.priority` macro overwrites `parser.chunks.priority` **for the rest of the current
   chunk** (§7.3).
4. `.include(priority=N)` and `.load(priority=N)` set the priority of the *nested* chunk.
   Note the include default is **0**, not `parser.default_priority` — an include without
   an explicit priority always runs at priority 0.

**Where it is applied.** Every object the parser creates takes `parser->chunks->priority`
at creation time:

- keys: `ucl_object_new_full(UCL_NULL, parser->chunks->priority)` in `ucl_parse_key`
- array elements: `ucl_object_new_full(UCL_NULL, parser->chunks->priority)` in
  `ucl_parser_get_container`
- containers: `ucl_object_new_full(type, parser->chunks->priority)` in
  `ucl_parser_add_container`

An object that is *reused* (duplicate-merge, or `ucl_parser_add_container` with a non-nil
`obj`) keeps its **original** priority.

**Interaction with duplicates.** Only `UCL_DUPLICATE_APPEND` and `UCL_DUPLICATE_MERGE`
consult priorities (§5.1):

- equal → implicit/explicit array (both values kept);
- old higher → the new value is discarded (parked on `trash_objs` so it is freed with the
  parser but can never be reached);
- new higher → the old value is replaced in place, preserving iteration position.

`UCL_OBJECT_INHERITED` (set by `.inherit` and by `ucl_attach_comment(..., before=true)` —
see §10) forces `prinew = priold + 1` under APPEND, i.e. anything overrides an inherited
value regardless of the real priorities.

`UCL_DUPLICATE_REWRITE` and `UCL_DUPLICATE_ERROR` ignore priorities entirely.

`ucl_parser_get_default_priority` returns `-1` for a nil parser, else the value.

---

## 7. Macros

### 7.1 Registration

```c
struct ucl_macro { char *name; union { ucl_macro_handler handler;
                                       ucl_context_macro_handler context_handler; } h;
                   void *ud; bool is_context; UT_hash_handle hh; };
```

Stored in a hash keyed by the exact name bytes. Registering the same name twice **adds a
second entry**; `HASH_FIND` returns the first inserted one, so a user cannot override a
built-in by re-registering the same name. (Reproduce this, or at minimum do not let a
re-registration silently win.)

```c
bool ucl_parser_register_macro(parser, const char *macro, ucl_macro_handler h, void *ud);
bool ucl_parser_register_context_macro(parser, const char *macro,
                                       ucl_context_macro_handler h, void *ud);
```
Both return false if `macro == NULL || handler == NULL`.

Handler signatures:

```c
typedef bool (*ucl_macro_handler)(const unsigned char *data, size_t len,
                                  const ucl_object_t *arguments, void *ud);
typedef bool (*ucl_context_macro_handler)(const unsigned char *data, size_t len,
                                          const ucl_object_t *arguments,
                                          const ucl_object_t *context, void *ud);
```

`arguments` is `NULL` when the macro had no `(...)`, and an owned `ucl_object_t` (usually
`UCL_OBJECT`, possibly empty) otherwise; the parser unrefs it after the call.
`context` is `parser->top_obj`.

`ucl_parser_new` registers exactly six built-ins, in this order:

| name | kind | handler | notes |
|---|---|---|---|
| `include` | plain | `ucl_include_handler` | `default_try = false`, `default_sign = false` |
| `try_include` | plain | `ucl_try_include_handler` | `default_try = true`, `default_sign = false` |
| `includes` | plain | `ucl_includes_handler` | `default_try = false`, `default_sign = true` (signature check) |
| `priority` | plain | `ucl_priority_handler` | |
| `load` | plain | `ucl_load_handler` | |
| `inherit` | **context** | `ucl_inherit_handler` | |

There is no `.includes` variant beyond signature checking, and **no** `.if`/`.env`/etc.
That is the complete list.

### 7.2 Grammar

```
'.' NAME [ '(' args ')' ] value [ ';' ] [whitespace]
```

- `.` must be the first non-whitespace/non-comment character where a key would start.
- `NAME` runs until whitespace or `(`.
- `args`, if present, is parsed as a UCL fragment (§7.5) yielding a `UCL_OBJECT`.
- `value` is a quoted string, a `{...}` block, or a bare atom (§7.4).
- Trailing `WHITESPACE_UNSAFE` and `;` are consumed.

Variable expansion is applied to the macro **value** (§8) but not to the name or args
(the args sub-parser inherits the flags but *not* the variables, so `${CURDIR}` inside
args is left literal — only the value gets expanded; `tests/basic/19.in` uses
`.include(duplicate="append") "${CURDIR}/19-append.inc"`, where the expansion happens on
the value).

### 7.3 The built-in macros

#### `.include`, `.try_include`, `.includes`

All three funnel into `ucl_include_common(data, len, args, parser, default_try, default_sign)`.

Defaults:

```
soft_fail       = default_try
must_exist      = !default_try
check_signature = default_sign
allow_glob      = false
use_prefix      = false
prefix          = NULL
target          = "object"
priority        = 0
parse_type      = UCL_PARSE_UCL
strat           = UCL_DUPLICATE_APPEND
allow_url       = false
```

Argument object keys (matched by `strncmp(param->key, "name", param->keylen)` — i.e. a
**prefix match on keylen**, so `t`, `tr`, `try` all match `"try"`; reproduce with a
prefix comparison if you want bug-for-bug fidelity, otherwise exact match is fine for
all real inputs):

| key | type | effect |
|---|---|---|
| `try` | bool | `must_exist = !value` (note: does **not** touch `soft_fail`) |
| `sign` | bool | `check_signature` |
| `glob` | bool | `allow_glob` |
| `url` | bool | `allow_url` |
| `prefix` | bool | `use_prefix` |
| `key` | string | `prefix` (the key name to nest under) |
| `target` | string | `"object"` (default) or `"array"` |
| `duplicate` | string | `"append"` / `"merge"` / `"rewrite"` / `"error"`; anything else is ignored |
| `priority` | int | chunk priority for the included content |
| `path` | array | `ucl_set_include_path(parser, value)` — replaces the parser's search path |

Resolution:

```
if parser.includepaths == NULL:
    if allow_url and strnstr(data, "://", len):   ucl_include_url(...)
    elif data != NULL:                            ucl_include_file(...)
else:
    if allow_url and strnstr(data, "://", len):   ucl_include_url(...)
    for each string element P of parser.includepaths:
        candidate = sprintf("%s/%.*s", P, len, data)
        if ucl_include_file(candidate, ...) succeeds:
            if !allow_glob: break            /* first hit wins unless globbing */
    if nothing succeeded: "cannot find file: %.*s in search path" -> false
```

`ucl_include_file`: if `!allow_glob` → single file. Otherwise scan the path for `*` or
`?`; if found, `glob(3)` it and include every match in glob order (soft-fail skips
failures); `0` matches with `must_exist` → `"cannot match any files for pattern %s"`.
No glob metacharacters → single file. (On Win32 globbing is a no-op.)

`ucl_include_file_single(data, len, parser, params)` — the core:

1. Return false immediately if `parser->state == UCL_STATE_ERROR`.
2. `realpath(filename)`. On failure:
   `soft_fail` → false; `!must_exist && errno != EPERM` → **true** (silently skipped);
   otherwise `"cannot open file %s: %s"` → false.
3. If the resolved path equals `parser->cur_file` →
   `"trying to include the file %s from itself"` (or false if `soft_fail`).
4. `ucl_fetch_file` (open + fstat + `S_ISREG` + mmap; empty files give `buf = NULL, len = 0`).
   Failure: `soft_fail` → false; `must_exist || err != NULL` → false; else clear the error
   and return **true**.
5. Optional signature check against `<path>.sig` using the registered public keys
   (OpenSSL only; without OpenSSL, `ucl_parser_pubkey_add` fails with
   `"cannot check signatures without openssl"`). The Go port can stub this out and fail
   with the same message.
6. Save `parser->cur_file`, unlink the current `CURDIR`/`FILENAME` variables from the
   variable list (keeping the old structs), and call
   `ucl_parser_set_filevars(parser, realpath, need_expand = false)` to install new ones.
7. `prev_state = parser->state; parser->state = UCL_STATE_INIT;`
8. **Prefixed include** (`use_prefix` or an explicit `key`):
   - if `use_prefix && prefix == NULL`, derive it from `basename(realpath)` with a
     trailing `.conf` or `.ucl` stripped.
   - `target == "array"` (case-insensitive `strcasecmp`):
     - key absent → create a `UCL_ARRAY` under that key, plus a fresh `UCL_OBJECT`
       appended to it — that object becomes the include's container.
     - key exists and is an array → append a fresh `UCL_OBJECT` to it.
     - key exists and is not an array → wrap it: new `UCL_ARRAY` (flagged `MULTIVALUE`,
       aliasing the old key), containing `[old, fresh_object]`, hash-replacing the old.
   - `target` anything else (i.e. "object"):
     - key absent → create a fresh `UCL_OBJECT` under that key.
     - key exists and is an object → the include merges into it.
     - key exists and is not an object →
       `"Conflicting type for key: %s, asked %s, has %s"` → false.
   - Push a stack frame for the nest object copying `level`, `flags` and `line` from the
     current top frame, with `chunk = parser->chunks`; set `cur_obj = nest_obj`.
9. `ucl_parser_add_chunk_full(parser, buf, buflen, priority, strat, parse_type)`.
10. On success: pop the prefix frame (if pushed), pop and free the chunk,
    `parser->recursion--`, restore `cur_file`, remove the include's `CURDIR`/`FILENAME`
    and re-append the saved ones, `parser->state = prev_state`.
    **On failure none of this is restored** — the parser is in an error state anyway.
11. munmap the buffer.

`ucl_include_url` is the same shape minus the file-variable and prefix handling; it does
**not** decrement `recursion` when popping its chunk (a leak in the C).

Because the included chunk starts in `UCL_STATE_INIT` with a non-nil `top_obj`, a file
that begins with `{` enters `KEY_OBRACE` and marks the current frame
`HAS_OBRACE|AUTOMATIC`; its closing `}` then clears the flags instead of popping the
caller's frame. That is what makes `tests/basic/23.in` (four includes, two with braces
and two without) produce a single flat merge.

#### `.priority`

```
.priority 5
.priority(priority=5)
```

Body (if `len > 0`): `strtol(value, &leftover, 10)`; any trailing garbage →
`"Invalid priority value in macro: %s"` → false. Otherwise the argument
`priority` (an int) is used. If neither is present → `"Unable to parse priority macro"`.
On success: `parser->chunks->priority = priority` — the **current chunk's** priority,
in effect from this point to the end of the chunk. Note the value is **not** masked here;
the masking happens per-object in `ucl_object_set_priority`.

#### `.load`

```
.load(key="name", target="string"|"int", try=bool, multiline=bool,
      escape=bool, trim=bool, priority=N) "path"
```

Reads a file and stores its *contents* as a value.

| arg | type | effect |
|---|---|---|
| `key` | string | **required**; missing/empty → `"No Key specified in load macro"` |
| `target` | string | `"string"` (default) or `"int"`, `strcasecmp` |
| `try` | bool | on fetch failure, clear the error and return true |
| `multiline` | bool | set `UCL_OBJECT_MULTILINE` on the resulting string |
| `escape` | bool | add `UCL_STRING_ESCAPE` to the string-construction flags |
| `trim` | bool | add `UCL_STRING_TRIM` |
| `priority` | int | `ucl_object_set_priority` on the result |

Behaviour: if the key already exists in `parser->stack->obj` →
`"Key %s already exists"` → false. `target == "int"` does `strtoll(contents, NULL, 10)`.
`target` values other than `string`/`int` silently produce **no object at all** (and the
macro still returns true). Empty macro body (`len == 0`) →
`"Unable to parse load macro"`.

`tests/basic/load.in`/`load.res` exercises all eight flag combinations — note that
`escape` and `trim` are applied by `ucl_object_fromstring_common`, which is object-model
territory: `TRIM` strips leading and trailing whitespace, `ESCAPE` JSON-escapes the
content (so `\n` in the file becomes the two characters `\` `n`).

#### `.inherit` (context macro)

```
key { .inherit "other_key"  }
key { .inherit(replace=true) "other_key" }
```

- `parent = ucl_object_lookup_len(context /* = top_obj */, data, len)`;
  nil or not `UCL_OBJECT` → `"Unable to find inherited object %.*s"` → false.
- `parser->stack->obj` must exist and be a `UCL_OBJECT`, else `"Invalid inherit context"`.
- `replace` (bool arg, default false).
- For each key of `parent`: if `!replace` and the key already exists in the target, skip.
  Otherwise deep-copy it, set `UCL_OBJECT_INHERITED` on the copy **when `!replace`**, and
  `ucl_object_insert_key(target, copy, ...)`.

The `INHERITED` flag is what makes a later real assignment win regardless of priority
under `UCL_DUPLICATE_APPEND` (§5.1).

### 7.4 `ucl_parse_macro_value`

At end of chunk → `UCL_ESYNTAX` `"unexpected end of input while parsing macro value"`.

| first byte | body |
|---|---|
| `"` | JSON-string lex; body is the content between the quotes (**escapes are not decoded here** — the raw bytes are handed to the handler; only variable expansion is applied afterwards) |
| `{` | consume `{`, skip leading `WHITESPACE_UNSAFE`, then everything up to the first `}` (no nesting, no quoting). Unterminated → `"unterminated macro value, missing '}'"` |
| other | everything up to the first `VALUE_END` byte (`\0 \n \r # , ; ] }`) |

Then: consume trailing `WHITESPACE_UNSAFE` and `;` characters (in any order/quantity).

### 7.5 `ucl_parse_macro_arguments`

Requires `chunk->remain >= 2` and the current byte to be `(`; otherwise returns nil
(and the caller treats it as "no args").

Saves `(line, column, pos, remain)` so it can roll back. Consumes the `(`, marks `c` as
the start, then a two-state scanner counting `args_len`:

```
state 0: '(' -> obraces++ ; ')' -> ebraces++ ; '"' -> state = 1
         if obraces == ebraces -> state = 99      /* do NOT count this byte */
         else args_len++
         if chunk->remain == 0 -> rollback, return NULL
         consume
state 1: if *p=='"' && p[-1] != '\\' -> state = 0
         if chunk->remain == 0 -> rollback, return NULL
         args_len++ ; consume
state 99: parse [c, c+args_len) with a fresh parser created with the SAME flags:
             params_parser = ucl_parser_new(parser->flags)
             if !ucl_parser_add_chunk(params_parser, c, args_len):
                 ucl_set_err(parser, UCL_ESYNTAX, "macro arguments parsing error")
                 res = NULL
             else res = ucl_parser_get_object(params_parser)
             ucl_parser_free(params_parser)
             return res
```

Notes:

- Nested parentheses are balanced, and `"` toggles a quote mode where parens do not count.
- `args_len` excludes the final `)` because the `obraces == ebraces` test runs before the
  increment.
- The sub-parser is a **completely fresh parser**: it inherits the flags but not the
  variables, macros (well, it gets the built-ins again), `cur_file`, or priority. So
  `${VAR}` inside macro args is not expanded, and a `.macro` inside args would be resolved
  against the fresh parser's built-ins.
- `.include()` with empty parens: `args_len == 0` → `add_chunk` with `len == 0` → the
  sub-parser creates an empty `UCL_OBJECT` as its top object → args is an **empty object**,
  not nil.
- Since the args are UCL, both `,` and `;` and newlines separate them:
  `.include(prefix=true; key="prefix2"; target="array"; glob=true)` is valid
  (`tests/basic/9.in`).
- After a successful args parse the caller runs `SKIP_SPACES_COMMENTS`.

### 7.6 `UCL_PARSER_DISABLE_MACRO`

Two effects:

1. In `ucl_parse_key`, a leading `.` no longer switches to `MACRO_NAME` — but note that
   `.` is in class `KEY` and **not** in `KEY_START`, so a line starting with `.` still
   fails with `"key must begin with a letter"` unless… actually the `MACRO_NAME` branch is
   the only handler for a leading `.`, so with macros disabled the `.` reaches the
   "invalid identifier" arm. **However** the driver never gets there, because
   `UCL_STATE_MACRO_NAME` is only entered from `ucl_parse_key`, which with the flag set
   never sets it. In practice `.include ...` with `DISABLE_MACRO` produces
   `"key must begin with a letter"`. (The `ucl_skip_macro_as_comment` path in
   `UCL_STATE_MACRO_NAME` is therefore only reachable if something else put the parser in
   `MACRO_NAME` — dead code in the current C. Implement it anyway for fidelity, but do not
   be surprised that the `-M` test mode simply fails to parse macro lines.)
2. `ucl_expand_variable` returns immediately with `*dst = NULL` — i.e. **variable
   expansion is also disabled**.

`ucl_skip_macro_as_comment(parser, chunk)` — a small state machine that swallows a macro
line as if it were a comment:

```
macro_skip_start: if !WHITESPACE(*p)            -> macro_has_symbols
                  elif WHITESPACE_UNSAFE(*p)    -> macro_save (without consuming)
                  consume
macro_has_symbols: '{' -> macro_has_sqbrace ; '(' -> macro_has_obrace ;
                   '"' -> macro_has_quote  ; '\n' -> macro_save (without consuming)
                   consume
macro_has_obrace:  '\\' -> save state, macro_has_backslash ; ')' -> macro_has_symbols ; consume
macro_has_sqbrace: '\\' -> ... ; '}' -> macro_save ; consume
macro_has_quote:   '\\' -> ... ; '"' -> macro_save ; consume
macro_has_backslash: return to the saved state; consume
macro_save: if SAVE_COMMENTS: save [c, p) ; return true
```

Returns false if the chunk ends before reaching `macro_save`.
(The names are misleading: `macro_has_sqbrace` handles `{`, not `[`.)

---

## 8. Variable expansion

Applies to: **unquoted string values, double-quoted string values, heredoc bodies, and
macro bodies**. Never to keys, never to single-quoted strings.

Only performed when the lexer set `var_expand` (a `$` was seen) *and*
`parser->variables != NULL` — see `ucl_copy_or_store_ptr` (§8.4).

### 8.1 Syntax

- `$$` → a literal `$` (the second `$` is emitted, the first dropped).
- `${NAME}` — "strict" form. `NAME` is everything up to the first `}`. This is the only
  form that can reach the fallback `var_handler`.
- `$NAME` — "loose" form: the longest **registered** variable whose name is a prefix of
  the remaining text wins (the list is scanned in registration order and the *first*
  match wins, so it is really "first registered prefix match", not longest).
  There is no character-class delimiter — `$FOOBAR` with `FOO` registered expands `FOO`
  and leaves `BAR`.
- A `$` at the very last byte of the input is emitted literally (the scan loop requires
  `p + 1 != end`).
- An unresolved `${NAME}` is left **verbatim**, braces included.
- An unresolved `$NAME` leaves the `$` and continues scanning from the next character.

### 8.2 Two-pass algorithm

`ucl_expand_variable(parser, &dst, src, in_len) -> ssize_t`

**Pass 1** (`ucl_check_variable`) measures the output length and sets `vars_found`.
If `DISABLE_MACRO` → `*dst = NULL; return in_len` (no expansion at all).
If no variable was actually found → `*dst = NULL; return in_len` (the caller keeps the
original buffer — the "trivial case").

**Pass 2** (`ucl_expand_single_variable`) writes into a buffer of exactly
`out_len + 1` bytes and NUL-terminates. The loop is bounded by both `p != end` and
`d != d_end`, so a mismatch between the two passes truncates rather than overflows.

`ucl_check_variable(parser, ptr /* points after the '$' */, remain, out_len, vars_found)`:

```
if *ptr == '{':
    scan for '}' within remain
    on '}': ucl_check_variable_safe(parser, ptr+1, len_between, out_len, strict=true, &found)
            found -> ret = (position after '}'), vars_found = true
            not found -> *out_len += 2        /* the two braces; the name bytes are counted
                                                 by the outer loop as it rescans them */
    no '}' found before the end: (*out_len)++
elif *ptr != '$':
    ucl_check_variable_safe(parser, ptr, remain, out_len, strict=false, &found)
    if !found: (*out_len)++
else:   /* '$$' */
    ret++;  (*out_len)++
```

`ucl_check_variable_safe` walks `parser->variables`:
- strict: requires `remain == var->var_len` and an exact byte match;
- loose: requires `remain >= var->var_len` and a prefix match.
On a hit: `*out_len += var->value_len`, `found = true`, return the position after the name.
On a miss with `strict && parser->var_handler != NULL`: call the handler; if it returns
true, `*out_len = dstlen` (**assignment, not `+=`** — a real bug that under-counts when a
handled variable is not the first thing in the string; keep it or fix it, but know that
pass 2 is bounded so the C truncates rather than corrupts), free `dst` if `need_free`,
and return `ptr + remain`.

`ucl_expand_single_variable(parser, ptr /* points AT the '$' */, in_len, &d, out_len)`:

```
p = ptr + 1;  ret = ptr + 1;  in_len--
if *p == '$':  *d++ = *p++;  return p                    /* '$$' -> '$' */
if *p == '{':  p++; in_len--; strict = true; ret += 2
for each registered var:
    if out_len >= var->value_len && in_len >= var->var_len + (strict ? 1 : 0)
       && memcmp(p, var->var, var->var_len) == 0
       && (!strict || p[var->var_len] == '}'):
           copy the value; ret += var->var_len; found = true; break
if !found and strict and var_handler:
    call handler(p, in_len, &dst, &dstlen, &need_free, ud)
    if it returns true and dstlen <= out_len:  copy dst; ret += in_len; found = true
if !found:
    if strict && out_len >= 2:  copy the two bytes "${"; ret--       /* leaves the name to be rescanned */
    else:                       copy the single byte '$'
return ret
```

Note that on the strict-not-found path `ret` is decremented so the caller resumes just
after `${`, and the variable name and the closing `}` are then copied byte by byte by the
outer loop — which is how `${UNKNOWN}` survives verbatim.

Also note that in the strict branch the handler is passed `in_len` (the remaining length
of the *whole* string after `${`), not the length of the name — so a `var_handler` must
find the `}` itself. `ucl_check_variable_safe` however passes the exact name length. This
asymmetry is real; a Go `var_handler` should be documented as receiving "the rest of the
string" in the expansion pass.

### 8.3 Registered and built-in variables

```c
struct ucl_variable { char *var, *value; size_t var_len, value_len; struct ucl_variable *prev, *next; };
```

`ucl_parser_register_variable(parser, var, value)`:
- `var == NULL` → no-op.
- `value == NULL` → delete the variable if present, else no-op.
- existing → replace the value in place (position in the list preserved).
- new → `DL_APPEND` (so lookup order is registration order).

**File variables** (`ucl_parser_set_filevars(parser, filename, need_expand)`):

| filename | `FILENAME` | `CURDIR` | `parser->cur_file` |
|---|---|---|---|
| non-NULL | the path (realpath'd if `need_expand`) | `dirname(path)` | the path |
| NULL | the literal `"undef"` | `getcwd()` | unchanged |

Called from:
- `ucl_parser_new`, with `NULL`, unless `UCL_PARSER_NO_FILEVARS` is set;
- `ucl_parser_add_file_full`, with the realpath'd filename (`need_expand = false`,
  because the caller already resolved it);
- `ucl_include_file_single`, with the realpath'd include target, after stashing the
  outer `CURDIR`/`FILENAME` (restored when the include finishes successfully).

`dirname` follows POSIX: `dirname("/a/b/c.conf") == "/a/b"`, `dirname("x") == "."`.
Note the C `dirname()` may modify its argument buffer, which is why `realbuf` is passed
and `FILENAME` is registered *before* `CURDIR`.

Only `FILENAME` and `CURDIR` are built in. There is no `INCLUDEDIR`, `HOSTNAME`, etc.

### 8.4 `ucl_copy_or_store_ptr`

The single funnel through which every key and string value passes:

```c
ssize_t ucl_copy_or_store_ptr(parser, src, &dst /* trash slot */, &dst_const /* obj value */,
                              in_len, need_unescape, need_lowercase, need_expand,
                              unescape_squote);
```

```
if need_unescape || need_lowercase || (need_expand && parser->variables != NULL)
   || !(parser->flags & UCL_PARSER_ZEROCOPY):
        allocate in_len+1 and copy (lowercasing if need_lowercase, via ucl_strlcpy_tolower)
        if need_unescape:  ret = unescape_squote ? ucl_unescape_squoted_string(dst, ret)
                                                 : ucl_unescape_json_string(dst, ret)
        if need_expand:    ret = ucl_expand_variable(parser, dst, tmp, ret)
                           (if the expansion returned NULL, keep tmp and its length)
        *dst_const = *dst
else:
        *dst_const = src        /* zero-copy: points into the caller's buffer, NOT NUL-terminated */
        ret = in_len
return ret
```

`ucl_strlcpy_unsafe(dst, src, siz)` = `memcpy(dst, src, siz-1); dst[siz-1] = 0; return siz-1`
— it copies exactly `in_len` bytes regardless of embedded NULs.
`ucl_strlcpy_tolower` stops at the first NUL (it is a real strlcpy with `tolower`).
Consequence: **`UCL_PARSER_KEY_LOWERCASE` truncates a key at an embedded NUL, while the
default path does not.** Also `ucl_strlcpy` returns `s - src - 1` and
`ucl_strlcpy_tolower` returns `s - src` — off by one from each other; the lowercase path
therefore reports a keylen one larger. In Go, just use the byte-slice length; but be aware
that a test comparing key lengths under `KEY_LOWERCASE` may be exercising this.

On allocation failure: `UCL_EINTERNAL`, `"cannot allocate memory for a string"`, and the
function `return false` — i.e. returns `0`, which the caller interprets as
`"empty keys are not allowed"` rather than `-1`. Not reachable in Go.

---

## 9. Parser flags

```c
UCL_PARSER_DEFAULT            = 0
UCL_PARSER_KEY_LOWERCASE      = 1<<0
UCL_PARSER_ZEROCOPY           = 1<<1
UCL_PARSER_NO_TIME            = 1<<2
UCL_PARSER_NO_IMPLICIT_ARRAYS = 1<<3
UCL_PARSER_SAVE_COMMENTS      = 1<<4
UCL_PARSER_DISABLE_MACRO      = 1<<5
UCL_PARSER_NO_FILEVARS        = 1<<6
```

| flag | exact effect |
|---|---|
| `KEY_LOWERCASE` | (a) hash containers are created case-insensitive (`ucl_hash_create(true)`, and `ucl_hash_insert_object(..., ignore_case)`); (b) keys are lowercased on copy via `ucl_strlcpy_tolower` (which stops at NUL, see §8.4). Applied in `ucl_parser_add_container`, `ucl_parser_process_object_element` and `ucl_include_file_single`. |
| `ZEROCOPY` | Skips the copy in `ucl_copy_or_store_ptr` **when no unescape, no lowercase and no expansion are needed**. Values then alias the input buffer and are **not NUL-terminated**; the caller must keep the buffer alive for the object's lifetime. In Go this is naturally satisfied by sub-slicing the input `[]byte`, so the flag can be honoured as "sub-slice instead of copy", or ignored entirely — but the *observable* difference is that `ucl_object_tostring` on a zero-copy string is not NUL-terminated in C; in Go there is no difference. |
| `NO_TIME` | `allow_time = false` in the number lexer (§4.4). |
| `NO_IMPLICIT_ARRAYS` | `ucl_parser_append_elt` builds a real `UCL_ARRAY` instead of a `next` chain (§5.2). |
| `SAVE_COMMENTS` | `ucl_parser_new` allocates `parser->comments` as a `UCL_OBJECT`; `ucl_skip_comments` and `ucl_skip_macro_as_comment` record comment text; `ucl_attach_comment` binds it to objects (§10). |
| `DISABLE_MACRO` | (a) a leading `.` is not treated as a macro in `ucl_parse_key`; (b) `ucl_expand_variable` becomes a no-op — **variables are not expanded either**; (c) `ucl_skip_macro_as_comment` becomes the `MACRO_NAME` handler (currently unreachable, see §7.6). |
| `NO_FILEVARS` | `ucl_parser_new` skips the initial `ucl_parser_set_filevars(parser, NULL, false)`, so `FILENAME`/`CURDIR` are not pre-registered. Note `ucl_parser_add_file_full` and the include handler call `set_filevars` **unconditionally**, so the flag only suppresses the *initial* cwd-based definition. |

The flags are also propagated verbatim to the macro-arguments sub-parser
(`ucl_parser_new(parser->flags)`).

---

## 10. Comment saving

Enabled by `UCL_PARSER_SAVE_COMMENTS`. `parser->comments` is a `UCL_OBJECT` whose **keys
are the raw bytes of the `ucl_object_t *` pointer** (`sizeof(void*)` bytes), mapping to a
string object holding the comment text:

```c
ucl_object_insert_key(parser->comments, parser->last_comment,
                      (const char *) &obj, sizeof(void *), true);
```

`ucl_comments_find(comments, srch)` does the matching lookup. In Go this must become a
`map[*Object]*Object` (or a field on the object); the pointer-as-key trick has no Go
equivalent and there is no observable difference as long as `ucl_comments_find`,
`ucl_comments_move` and `ucl_comments_add` behave the same.

**Accumulation.** `ucl_save_comment(parser, begin, len)` creates a string object and
either sets `parser->last_comment` or `DL_APPEND`s to it — so consecutive comments form
an implicit-array chain that is attached as one unit. `len == 0` or `begin == NULL` is a
no-op.

**Attachment.** `ucl_attach_comment(parser, obj, before)`:

```c
if (parser->last_comment) {
    ucl_object_insert_key(parser->comments, parser->last_comment, &obj, sizeof(void*), true);
    if (before) parser->last_comment->flags |= UCL_OBJECT_INHERITED;
    parser->last_comment = NULL;
}
```

Note the reuse of `UCL_OBJECT_INHERITED` as "this comment came *before* the object"
(as opposed to trailing). This flag lives on the *comment* object, not the commented one,
so it does not interact with the duplicate-priority logic of §5.1.

Attachment sites, all of them:

| site | `before` |
|---|---|
| `ucl_parser_process_object_element`, after a key is resolved | `false` |
| `ucl_parser_get_container`, when allocating a new array element | `false` |
| `ucl_parse_after_value`, right after popping a `}`/`]` frame (on `parser->cur_obj`) | `true` |
| state-machine epilogue at end of chunk (`cur_obj`, else `stack->obj`, else `top_obj`) | `true` |

If `SAVE_COMMENTS` is off, `parser->comments` is nil and `ucl_object_insert_key` on a nil
top is a no-op — but `last_comment` is never populated in the first place, so the attach
calls are all no-ops.

Public API: `ucl_parser_get_comments(parser)` returns the comments object (or nil);
`ucl_comments_find`, `ucl_comments_move`, `ucl_comments_add` operate on it.

Recorded text (§3.2): `#` comments exclude the trailing newline; `/* */` comments include
the delimiters **plus one trailing byte** (`p - beg + 1` after `p` has already passed the
closing `/`).

---

## 11. Parser lifecycle and public API

### 11.1 `struct ucl_parser`

```c
struct ucl_parser {
    enum ucl_parser_state state, prev_state;
    unsigned int recursion;
    int flags;
    unsigned default_priority;
    int err_code;
    ucl_object_t *top_obj, *cur_obj, *trash_objs, *includepaths;
    char *cur_file;
    struct ucl_macro *macroes;                 /* hash */
    struct ucl_stack *stack;                   /* container stack, top first */
    struct ucl_chunk *chunks;                  /* chunk stack, top first */
    struct ucl_pubkey *keys;
    struct ucl_parser_special_handler *special_handlers;
    ucl_include_trace_func_t *include_trace_func;  void *include_trace_ud;
    struct ucl_variable *variables;            /* doubly-linked, registration order */
    ucl_variable_handler var_handler;  void *var_data;
    ucl_object_t *comments, *last_comment;
    UT_string *err;
};
```

### 11.2 Lifecycle

```
ucl_parser_new(flags)
  -> zeroed struct
  -> register the six built-in macros (in order)
  -> parser->flags = flags; includepaths = NULL
  -> if SAVE_COMMENTS: comments = new UCL_OBJECT
  -> if !NO_FILEVARS: ucl_parser_set_filevars(parser, NULL, false)

[ optional: set_default_priority, register_variable, set_variables_handler,
            register_macro / register_context_macro, set_include_path,
            add_special_handler, set_include_tracer, pubkey_add ]

ucl_parser_add_string / add_chunk / add_file / add_fd   (repeatable)

ucl_parser_get_object(parser)
  -> if state != UCL_STATE_ERROR && top_obj != NULL: ucl_object_ref(top_obj)
  -> else NULL

ucl_parser_free(parser)
```

`ucl_parser_free` releases: `top_obj`, `includepaths`, every stack frame, every macro,
every chunk (`ucl_chunk_free` also frees the special-handler chain and `fname`), the
pubkeys, the variables, every object on `trash_objs`, the error string, `cur_file`, and
`comments`.

### 11.3 Public API summary

```c
struct ucl_parser *ucl_parser_new(int flags);
void  ucl_parser_free(struct ucl_parser *);

bool  ucl_parser_set_default_priority(struct ucl_parser *, unsigned prio);
int   ucl_parser_get_default_priority(struct ucl_parser *);        /* -1 if parser == NULL */

bool  ucl_parser_register_macro(struct ucl_parser *, const char *name, ucl_macro_handler, void *ud);
bool  ucl_parser_register_context_macro(struct ucl_parser *, const char *name,
                                        ucl_context_macro_handler, void *ud);
void  ucl_parser_register_variable(struct ucl_parser *, const char *var, const char *value);
void  ucl_parser_set_variables_handler(struct ucl_parser *, ucl_variable_handler, void *ud);

bool  ucl_parser_add_chunk(struct ucl_parser *, const unsigned char *, size_t);
bool  ucl_parser_add_chunk_priority(struct ucl_parser *, const unsigned char *, size_t, unsigned);
bool  ucl_parser_add_chunk_full(struct ucl_parser *, const unsigned char *, size_t,
                                unsigned priority, enum ucl_duplicate_strategy,
                                enum ucl_parse_type);
bool  ucl_parser_insert_chunk(struct ucl_parser *, const unsigned char *, size_t);
bool  ucl_parser_add_string(struct ucl_parser *, const char *, size_t);
bool  ucl_parser_add_string_priority(struct ucl_parser *, const char *, size_t, unsigned);
bool  ucl_parser_add_file(struct ucl_parser *, const char *filename);
bool  ucl_parser_add_file_priority(struct ucl_parser *, const char *, unsigned);
bool  ucl_parser_add_file_full(struct ucl_parser *, const char *, unsigned,
                               enum ucl_duplicate_strategy, enum ucl_parse_type);
bool  ucl_parser_add_fd(struct ucl_parser *, int fd);
bool  ucl_parser_add_fd_priority(struct ucl_parser *, int fd, unsigned);
bool  ucl_parser_add_fd_full(struct ucl_parser *, int fd, unsigned,
                             enum ucl_duplicate_strategy, enum ucl_parse_type);

bool  ucl_set_include_path(struct ucl_parser *, ucl_object_t *paths /* UCL_ARRAY */);
bool  ucl_parser_set_filevars(struct ucl_parser *, const char *filename, bool need_expand);
const char *ucl_parser_get_cur_file(struct ucl_parser *);

ucl_object_t *ucl_parser_get_object(struct ucl_parser *);
ucl_object_t *ucl_parser_get_current_stack_object(struct ucl_parser *, unsigned depth);
unsigned char ucl_parser_chunk_peek(struct ucl_parser *);
bool  ucl_parser_chunk_skip(struct ucl_parser *);

const char *ucl_parser_get_error(struct ucl_parser *);      /* NULL if none */
int   ucl_parser_get_error_code(struct ucl_parser *);       /* 0 if parser == NULL */
unsigned ucl_parser_get_column(struct ucl_parser *);        /* chunks->column, 0 if no chunk */
unsigned ucl_parser_get_linenum(struct ucl_parser *);       /* chunks->line,   0 if no chunk */
void  ucl_parser_clear_error(struct ucl_parser *);

const ucl_object_t *ucl_parser_get_comments(struct ucl_parser *);
bool  ucl_parser_pubkey_add(struct ucl_parser *, const unsigned char *key, size_t len);
void  ucl_parser_add_special_handler(struct ucl_parser *, struct ucl_parser_special_handler *);
void  ucl_parser_set_include_tracer(struct ucl_parser *, ucl_include_trace_func_t, void *ud);
```

`ucl_parser_get_current_stack_object(parser, depth)`: walk `depth` frames down from the
top; every frame on the way (including the target) must hold a `UCL_OBJECT`, else return
nil. Returns a ref'd object.

`ucl_parser_chunk_peek`: the byte at `chunks->pos`, or `0` if there is no chunk / at end.
`ucl_parser_chunk_skip`: `ucl_chunk_skipc` once; false if there is nothing to skip.

Include tracing: `ucl_include_trace_func_t(parser, parent, args, path, pathlen, ud)` is
called once per `.include` (with the raw macro body and the args object) and once per
glob match (with `args == NULL` and the expanded path). `parent` is
`parser->stack->obj` or NULL.

---

## 12. Recommended Go design

### 12.1 Overall shape

```go
package ucl

type parserState uint8
const (
    stateInit parserState = iota
    stateObject      // unused, kept for numeric parity
    stateArray       // unused
    stateKey
    stateKeyOBrace
    stateValue
    stateAfterValue
    stateArrayValue  // unused
    stateSComment    // unused
    stateMComment    // unused
    stateMacroName
    stateMacro
    stateError
)

type chunk struct {
    data              []byte   // the whole chunk
    pos               int      // index into data; `remain` == len(data)-pos, `end` == len(data)
    fname             string
    line, column      int      // line 1-based, column 0-based
    priority          uint
    strategy          DuplicateStrategy
    parseType         ParseType
    specialHandlers   []specialHandlerRec
}

type stackFrame struct {
    obj    *Object
    level  uint16
    flags  uint16
    line   uint32
    chunk  *chunk    // identity comparison only
}

type Parser struct {
    state, prevState parserState
    recursion        int
    flags            ParserFlags
    defaultPriority  uint
    errCode          ErrorCode
    err              error       // first error wins
    topObj, curObj   *Object
    trashObjs        []*Object
    includePaths     []string
    curFile          string
    macros           map[string]*macro   // plus an insertion-order slice if you need
                                         // first-registration-wins semantics
    stack            []*stackFrame       // top == last element
    chunks           []*chunk            // top == last element
    variables        []variable          // ordered; a map for fast lookup alongside
    varHandler       VariableHandler
    comments         *Object
    lastComment      *Object
}
```

### 12.2 Replacing pointer arithmetic

The C keeps three redundant cursors — `p` (a local pointer), `chunk->pos` and
`chunk->remain`. Collapse them into a **single integer index** `chunk.pos`, with
helpers:

```go
func (c *chunk) atEnd() bool          { return c.pos >= len(c.data) }
func (c *chunk) remain() int          { return len(c.data) - c.pos }
func (c *chunk) cur() byte            { return c.data[c.pos] }        // caller checks atEnd
func (c *chunk) peek(n int) (byte, bool)
func (c *chunk) skip() {                                              // == ucl_chunk_skipc
    if c.pos >= len(c.data) { return }
    if c.data[c.pos] == '\n' { c.line++; c.column = 0 } else { c.column++ }
    c.pos++
}
func (c *chunk) skipN(n int)          // for the number/heredoc hand-patched advances
```

Every C `p = chunk->pos` resync disappears. The three places that patch `remain`/`column`
by hand become explicit index assignments plus a manual `column` fix-up:

- numbers: `c.column += n; c.pos += n` (no line counting — correct, numbers cannot span lines).
- heredoc entry: `c.pos = nlIdx + 1; c.column = 0; c.line++`.
- heredoc terminator: `c.pos = termIdx + termLen; c.column = termLen`.

`c` (the start-of-token pointer) becomes a saved `int` index; `[c, p)` becomes
`chunk.data[start:chunk.pos]`.

Backwards scans without a lower bound (the trailing-space strip in `ucl_parse_value`, and
the `p[-1] != '\\'` checks in comments and macro args) **must** be bounded in Go. The
bounded versions are behaviourally identical on all valid inputs; on malformed input the
C reads out of bounds, so there is nothing to preserve.

### 12.3 The state machine

A `for` loop over a `switch` on `p.state`, with each arm a method:

```go
func (p *Parser) stateMachine() error {
    ch := p.top()                       // chunk
    if p.topObj == nil { p.state = stateInit }
    for !ch.atEnd() {
        switch p.state {
        case stateInit:       if err := p.doInit(ch); err != nil { return err }
        case stateKeyOBrace:  p.stack[len(p.stack)-1].flags |= stackHasOBrace | stackAutomatic
                              fallthrough
        case stateKey:        ...
        ...
        case stateMacro:
            ...
            ch = p.top()                // the handler may have swapped chunks
        }
    }
    return p.epilogue(ch)
}
```

Two arms need the C's `continue`-without-consuming semantics (`KEY` seeing `}` or EOF,
and `KEY` after `end_of_object`). Model those as `p.state = stateAfterValue; continue`
inside the loop — but note the loop condition `!ch.atEnd()` would then exit rather than
re-dispatch on an EOF `KEY`. The C has the same behaviour (`continue` re-tests
`chunk->pos < chunk->end`), so a plain `continue` is correct.

The `fallthrough` for `KEY_OBRACE → KEY` is directly expressible in Go, which is nice; if
the arms grow, factor `doKey()` out and call it from both.

### 12.4 Error handling

Keep the "first error wins" semantics: a `setErr(code, msg)` method that returns early if
`p.err != nil`, formats exactly the templates in §2.3, sets `p.errCode` and
`p.state = stateError`. A second `createErr(format, args...)` for the bare messages that
do **not** set the state. Because the Go tests will compare the message text, produce it
with `fmt.Sprintf` using the same verbs; be careful with:

- `%c` for a byte → `string(rune(b))` only works for ASCII; use `fmt.Sprintf("%c", b)`
  which does the same for `b < 0x80` (all `isgraph` bytes are).
- `%02x` → `fmt.Sprintf("0x%02x", b)`.
- `%.*s` → slice the bytes first.
- The unmatched-brace message is **appended**, so accumulate into a `strings.Builder`
  and only then wrap it as the error.

Expose the message via `Parser.ErrorString() string` and the code via
`Parser.ErrorCode() ErrorCode`, mirroring `ucl_parser_get_error`/`get_error_code`.
`ucl_parser_clear_error` → `Parser.ClearError()`.

### 12.5 Character classes

Generate a `var charTable = [256]uint16{...}` from the table in §3.1 (a `go:generate`
step that reads `ucl_chartable.h`, or just the literal). Then:

```go
const (
    chDenied uint16 = 1 << iota
    chKey; chKeyStart; chWhitespace; chWhitespaceUnsafe; chValueEnd
    chValueStr; chValueDigit; chValueDigitStart; chEscape; chKeySep
    chJSONUnsafe; chUCLUnsafe
)
func is(c byte, mask uint16) bool { return charTable[c]&mask != 0 }
```

Do **not** substitute `unicode.IsSpace` or `strings.ContainsRune` — the table's
definitions are deliberately narrow (`WHITESPACE` is only tab and space).

### 12.6 Strings and zero-copy

`ucl_copy_or_store_ptr` becomes:

```go
func (p *Parser) storeString(src []byte, needUnescape, needLower, needExpand, squote bool) []byte
```

returning either a sub-slice of the chunk (the zero-copy path — safe in Go, since the
chunk keeps the backing array alive) or a freshly allocated slice. Since Go slices carry
their length, the C's NUL-termination concerns vanish; `UCL_PARSER_ZEROCOPY` becomes a
pure allocation optimisation with no observable semantic difference. **Keep the flag** so
existing call sites compile, and keep the "copy anyway when unescaping/lowercasing/
expanding is needed" branch so allocation counts roughly match.

Unescaping is in-place tortoise/hare in C; in Go, write into a fresh slice of the same
capacity (the output is never longer than the input for either unescaper — except
`\uXXXX`, where 6 input bytes become at most 3 output bytes, and the squoted
"backslash kept" case, where `\x` (2 bytes) becomes `\x` (2 bytes)). So `make([]byte, 0,
len(src))` is always sufficient.

### 12.7 The chunk and container stacks

Both are singly-linked in C with `LL_PREPEND`; in Go use slices with the top at the end.
Two places compare frame `chunk` pointers (`st->chunk != parser->chunks` in the epilogue),
so keep `*chunk` identity — do not store chunks by value in a slice that may reallocate;
store `[]*chunk`.

The `AFTER_VALUE` collapse loop and the `DL_FOREACH` over `stack->obj`'s implicit-array
chain both need the object model's `next` pointers, so the `Object` type must expose
them (or an equivalent `siblings []*Object` — but the emitters and `Len()` walk `next`,
so a linked chain is simpler to keep faithful).

### 12.8 Macros and includes

```go
type MacroHandler        func(data []byte, args *Object) bool
type ContextMacroHandler func(data []byte, args, ctx *Object) bool
```

Drop the `void *ud` — a Go closure captures what it needs. But the drop-in binding
(`go-libucl`'s `RegisterMacro(string, MacroFunc)` where `MacroFunc = func(string)`) needs
an adapter that ignores args and the return value (always succeed).

The include handlers are the only part of the parser that touches the filesystem.
Put them behind a small interface so tests can stub it:

```go
type fileSystem interface {
    Abs(path string) (string, error)          // realpath
    ReadFile(path string) ([]byte, error)     // mmap equivalent
    Glob(pattern string) ([]string, error)
}
```

`glob(3)` and Go's `filepath.Glob` differ subtly (`[` classes, `**`); `filepath.Match`
semantics are close enough for the test corpus, but note that C `glob` sorts results
(`GLOB_NOSORT` is not passed) and so does `filepath.Glob` — good.

URL includes: the C uses libfetch or libcurl, and compiles to `"URL support is disabled"`
without either. The simplest faithful port is to return that exact error unless a
`Parser.HTTPClient` is configured.

Signature checking (`.includes`, `sign=true`): without OpenSSL the C's
`ucl_parser_pubkey_add` fails with `"cannot check signatures without openssl"` and
`ucl_sig_check` is compiled out (so `check_signature` silently does nothing). Match the
no-OpenSSL build: make `PubkeyAdd` return that error and treat `sign=true` as a no-op.

### 12.9 Recursion and re-entrancy

`ucl_state_machine` is re-entered recursively through the macro handlers
(`MACRO` → `.include` → `add_chunk_full` → `ucl_state_machine`). Go handles this natively;
just remember that after any handler call the local `ch` must be refreshed from
`p.chunks[len(p.chunks)-1]`, exactly as the C does `chunk = parser->chunks`.

Guard depth with `recursion > 16` at the same point (after the push), and produce the
same message.

### 12.10 Testing strategy

- The character table, the number lexer and both unescapers are pure functions — table-test
  them directly against the cases in §3.4, §3.5 and §4.3.
- `tests/basic/*.in` → `.res` via the config emitter is the integration oracle; run them
  table-driven over the directory. The `.in` files that have no `.res` (e.g. `10.in`)
  only assert "parses without error".
- The error-message catalogue in §2.3/§2.4 should get its own table of
  `(input, expected error string)` pairs; build them by running the C binary once
  (`cmake -B build && cmake --build build` in `libucl/` gives `libucl.so` plus the test
  binaries) and capturing `ucl_parser_get_error`.
- Fuzz `Parser.AddString` against the C via cgo for as long as the C reference is
  available; the C's out-of-bounds reads on malformed input mean a few divergences are
  acceptable (and preferable) — document them rather than reproducing UB.
