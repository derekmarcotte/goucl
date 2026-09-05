# libucl MessagePack codec — implementation spec (v0.9.4)

Source of truth: `src/ucl_msgpack.c` (parser + msgpack-specific emit primitives),
`src/ucl_emitter.c` (`ucl_emit_msgpack_elt` and the vtable), `src/ucl_internal.h`
(`struct ucl_stack`, `struct ucl_chunk`, `struct ucl_parser`, flags),
`tests/test_msgpack.c`, `tests/test_msgpack_malformed.c`.

All multi-byte integers/floats on the wire are **big-endian**. libucl's own code
does the swap manually (`TO_BE16/32/64`, `FROM_BE16/32/64`) rather than relying on
network byte order helpers; behaviourally this is exactly `encoding/binary.BigEndian`.

---

## 1. Byte-level format table

libucl's parser dispatch table (`parsers[]` in `ucl_msgpack.c`) is a small
*prefix/mask* search, scanned **linearly in table order** (first match wins, but
in practice every entry's bit-pattern is disjoint so order does not matter except
for readability). For each format: `prefix` byte pattern, `prefixlen` = number of
significant high bits, `len` = either the fixed payload length (when the flag
`FIXED` is set and `len != 0`), the embedded-length mask value (`FIXED` set and
`len == 0`), or the width in bytes of an external length field (`FIXED` not set).

A byte `t` matches an entry when `(t >> (8 - prefixlen)) == (prefix >> (8 - prefixlen))`.

| Prefix byte(s) | prefixlen | msgpack name | Payload layout | Flags | Decodes to UCL type | Encoded from UCL type / range |
|---|---|---|---|---|---|---|
| `0x00`–`0x7f` | 1 | positive fixint | value = byte itself (0..127) | FIXED, TYPEVALUE | `UCL_INT`, value = `byte & 0x7f` | int in `[0, 0x7f]` |
| `0xe0`–`0xff` | 3 | negative fixint | value = byte itself, sign-extended as `int8_t` | FIXED, TYPEVALUE | `UCL_INT`, value = `(int8_t)byte` | int in `[-32, -1]` (see §3 for exact cutoffs) |
| `0x80`–`0x8f` | 4 | fixmap | low 4 bits = element **pair** count N (0..15); N key/value pairs follow | FIXED, CONTAINER, ASSOC | `UCL_OBJECT` with N key/value pairs | object with `len` (distinct-key count) ≤ 15 |
| `0x90`–`0x9f` | 4 | fixarray | low 4 bits = element count N (0..15); N values follow | FIXED, CONTAINER | `UCL_ARRAY` with N elements | array with `len` ≤ 15 |
| `0xa0`–`0xbf` | 3 | fixstr | low 5 bits = byte length N (0..31); N raw bytes follow | FIXED, KEY | `UCL_STRING` (text) | string, len ≤ 31, not binary |
| `0xc0` | 8 | nil | (no payload) | FIXED, TYPEVALUE | `UCL_NULL` | `UCL_NULL` |
| `0xc2` | 8 | false | (no payload) | FIXED, TYPEVALUE | `UCL_BOOLEAN` = false | bool false |
| `0xc3` | 8 | true | (no payload) | FIXED, TYPEVALUE | `UCL_BOOLEAN` = true | bool true |
| `0xc4` | 8 | bin8 | 1-byte length N, then N raw bytes | KEY (len-width=1) | `UCL_STRING` + `UCL_OBJECT_BINARY` flag | binary string, len ≤ 0xff |
| `0xc5` | 8 | bin16 | 2-byte BE length N, then N bytes | KEY (len-width=2) | same, binary | binary string, 0xff < len ≤ 0xffff |
| `0xc6` | 8 | bin32 | 4-byte BE length N, then N bytes | KEY (len-width=4) | same, binary | binary string, len > 0xffff |
| `0xc7` | 8 | ext8 | 1-byte length N, 1 type byte, N data bytes | EXT (len-width=1) | **ignored / skipped**, produces no value | never emitted |
| `0xc8` | 8 | ext16 | 2-byte BE length N, 1 type byte, N data bytes | EXT (len-width=2) | ignored | never emitted |
| `0xc9` | 8 | ext32 | 4-byte BE length N, 1 type byte, N data bytes | EXT (len-width=4) | ignored | never emitted |
| `0xca` | 8 | float32 | 4-byte BE IEEE-754 single | FIXED (len=4) | `UCL_FLOAT`, widened to double | never emitted (see §3, doubles always emitted as float64) |
| `0xcb` | 8 | float64 | 8-byte BE IEEE-754 double | FIXED (len=8) | `UCL_FLOAT` | `UCL_FLOAT` and `UCL_TIME` |
| `0xcc` | 8 | uint8 | 1 byte, unsigned | FIXED (len=1) | `UCL_INT` | int in `[0x80, 0xff]` |
| `0xcd` | 8 | uint16 | 2-byte BE, unsigned | FIXED (len=2) | `UCL_INT` | int in `[0x100, 0xffff]` |
| `0xce` | 8 | uint32 | 4-byte BE, unsigned | FIXED (len=4) | `UCL_INT` | int in `[0x10000, 0xffffffff]` |
| `0xcf` | 8 | uint64 | 8-byte BE, unsigned | FIXED (len=8) | `UCL_INT` | int > `0xffffffff` |
| `0xd0` | 8 | int8 | 1 byte, signed | FIXED (len=1) | `UCL_INT` | int in `[-127, -33]` (note: not -128, see §3) |
| `0xd1` | 8 | int16 | 2-byte BE, signed | FIXED (len=2) | `UCL_INT` | int in `[-32767, -128]` (not -32768) |
| `0xd2` | 8 | int32 | 4-byte BE, signed | FIXED (len=4) | `UCL_INT` | int in `[-2147483647, -32768]` (not -2147483648) |
| `0xd3` | 8 | int64 | 8-byte BE, signed | FIXED (len=8) | `UCL_INT` | int < -2147483647, and the three "boundary" values excluded above |
| `0xd4` | 8 | fixext1 | 1 type byte + 1 data byte | FIXED (len=1, but parse_ignore treats total as 2), EXT | ignored | never emitted |
| `0xd5` | 8 | fixext2 | 1 type byte + 2 data bytes (table len=2, treated as 3) | FIXED, EXT | ignored | never emitted |
| `0xd6` | 8 | fixext4 | 1 type byte + 4 data bytes (table len=4, treated as 5) | FIXED, EXT | ignored | never emitted |
| `0xd7` | 8 | fixext8 | 1 type byte + 8 data bytes (table len=8, treated as 9) | FIXED, EXT | ignored | never emitted |
| `0xd8` | 8 | fixext16 | 1 type byte + 16 data bytes (table len=16, treated as 17) | FIXED, EXT | ignored | never emitted |
| `0xd9` | 8 | str8 | 1-byte length N, then N raw bytes (UTF-8 text) | KEY (len-width=1) | `UCL_STRING` (text) | string 0x1f < len ≤ 0xff |
| `0xda` | 8 | str16 | 2-byte BE length N, then N bytes | KEY (len-width=2) | `UCL_STRING` (text) | string 0xff < len ≤ 0xffff |
| `0xdb` | 8 | str32 | 4-byte BE length N, then N bytes | KEY (len-width=4) | `UCL_STRING` (text) | string len > 0xffff |
| `0xdc` | 8 | array16 | 2-byte BE element count N, then N values | CONTAINER (len-width=2) | `UCL_ARRAY` | array 0xf < len ≤ 0xffff |
| `0xdd` | 8 | array32 | 4-byte BE element count N, then N values | CONTAINER (len-width=4) | `UCL_ARRAY` | array len > 0xffff |
| `0xde` | 8 | map16 | 2-byte BE pair count N, then N pairs | CONTAINER, ASSOC (len-width=2) | `UCL_OBJECT` | object 0xf < len ≤ 0xffff |
| `0xdf` | 8 | map32 | 4-byte BE pair count N, then N pairs | CONTAINER, ASSOC (len-width=4) | `UCL_OBJECT` | object len > 0xffff |
| `0xc1` | — | (reserved, "never used") | — | not in table | **decode error**: "unknown msgpack format" | never emitted |

Notes:
- **Keys** (map keys) may be *any* of: fixstr, str8/16/32, bin8/16/32 — the parser only
  checks the generic `MSGPACK_FLAG_KEY` bit, it does not distinguish text vs binary
  for keys. A key that is itself an int/float/bool/nil/array/map is a decode error
  ("bad type for key ... expected string").
- **Extensions** (`ext8/16/32`, `fixext1/2/4/8/16`) are fully unsupported for
  decoding: their bytes are skipped (length-validated) and they insert **no value**
  at all into the surrounding container (not even null) — see §2 (`read_array_value`
  handles `parser->cur_obj == NULL` specially: "we have parsed ext, ignore it"). If an
  ext appears as a map **value**, the same silent-skip happens but the key is still
  consumed (asserted non-null) — net effect: the pair's slot is *not* filled with any
  ucl object though the container's expected-length counter is still decremented.
  If ext appears as a map **key**, it's a hard decode error (ext types don't set
  `MSGPACK_FLAG_KEY`).
- libucl **never emits** ext/fixext, float32, or any UCL_TIME as anything other
  than float64 — encoder always chooses among the types explicitly listed in §3.

---

## 2. DECODER — parser state-machine integration

Entry point: `bool ucl_parse_msgpack(struct ucl_parser *parser)`, called from
`ucl_parser_add_chunk_full()` when `parse_type == UCL_PARSE_MSGPACK` **and**
`len > 0` (an empty chunk never reaches the msgpack code at all — see §4).

### 2.1 Top-level gate

Before running the state machine, if there is no existing container on the
parser stack (`parser->stack == NULL`, i.e. this is the very first chunk),
libucl requires the first byte to look "container-ish":
```
if ((*p & 0x80) != 0x80 && !(*p >= 0xdc && *p <= 0xdf))
    error "bad top level object for msgpack"
```
This rejects positive fixints (`0x00`-`0x7f`) immediately, but it is **not**
sufficient by itself to guarantee a map/array at top level — a bare fixstr,
bool, nil, or float byte has its high bit set and passes this gate, then fails
later via the state machine's `error_state` (see §2.4). Net effect for the Go
port: **top-level value must decode to `UCL_OBJECT` or `UCL_ARRAY`**, full stop;
replicate by checking the decoded root type at the end, not only the first-byte
heuristic (or implement both checks — the C does both and either can fire).

### 2.2 States

`ucl_msgpack_consume()` runs an explicit state machine over one flat byte
cursor `p`/`remain` (`enum e_msgpack_parser_state`):

- `read_type` — read one type-prefix byte via `ucl_msgpack_get_parser_from_type`,
  decode its length (embedded, fixed-width, or external field — exactly the
  three cases in §1), advance `p`/`remain` past the header, then branch:
  - ASSOC flag set → `start_assoc`
  - CONTAINER flag set (but not ASSOC) → `start_array`
  - else → `next_state` (the state that was queued before entering `read_type`)
- `start_assoc` — allocate a new `UCL_OBJECT`, insert it into the parent
  container (if any) under the pending key, push a new frame onto
  `parser->stack` (`ucl_msgpack_get_container`) recording `e.len = N` (pair
  count), then call the map "parser function" (a no-op that just stashes
  `container->obj = parser->cur_obj`). If `N > 0`, loop back to `read_type`
  with `next_state = read_assoc_key`; if `N == 0`, go straight to
  `finish_assoc_value` (empty object).
- `start_array` — mirror of `start_assoc` for `UCL_ARRAY`; `next_state =
  read_array_value` when `N > 0`, else `finish_array_value`.
- `read_assoc_key` — the just-read type **must** have `MSGPACK_FLAG_KEY` set
  (fixstr/str8/16/32/bin8/16/32) or it's a decode error ("bad type for key").
  Records `key = p, keylen = len` (no copy yet — zero-copy over the input
  buffer unless `UCL_PARSER_ZEROCOPY` is unset, in which case the string
  parser function below copies it into "trash" storage owned by the object).
  Bounds-checks `len == 0 || len > remain` → error "too long or empty key".
  Advances `p`/`remain` past the key bytes, loops to `read_type` with
  `next_state = read_assoc_value`.
- `read_assoc_value` — invokes the value's type-specific parse function
  (int/float/bool/null/string/map/array/ignore) to produce `parser->cur_obj`,
  then calls `ucl_msgpack_insert_object(parser, key, keylen, parser->cur_obj)`
  which: sets `obj->key/keylen`, copies the key into trash storage if not
  zero-copy, calls `ucl_parser_process_object_element` (this is what handles
  duplicate-key policy, e.g. `UCL_DUPLICATE_APPEND` chains multiple values
  under one key as a linked list via `next`), and decrements the container's
  `e.len` counter. Clears `key`/`keylen`. If the ext-skip case fired,
  `parser->cur_obj == NULL` and insertion is skipped entirely (pair vanishes,
  but the len counter was still decremented by the container-length bookkeeping
  path — actually note: `ucl_msgpack_insert_object` itself decrements `e.len`,
  and it is *not called* when `cur_obj == NULL` for array elements; but for
  **assoc values** the code has no null-guard before calling
  `ucl_msgpack_insert_object` — `assert(key != NULL && keylen > 0)` fires
  unconditionally and insertion proceeds only `if (parser->cur_obj)`; if null,
  neither `e.len` is decremented nor the pair inserted, which would make
  `ucl_msgpack_is_container_finished` never see the container finish — in
  practice an ext-as-map-value is therefore a **hang/stuck** container that will
  surface as "incomplete container" or "unfinished value" at end-of-input. The
  Go port should simply treat ext as a value in any position as a decode error
  to avoid replicating this edge case ambiguity — it is not exercised by any
  test and the C behavior here is effectively a bug, not a contract.)
  Then: if `ucl_msgpack_is_container_finished` (container's `e.len == 0`) →
  `finish_assoc_value`; else loop to `read_type`/`read_assoc_key` for the next
  pair.
- `read_array_value` — same as `read_assoc_value` but no key; inserts via
  `ucl_msgpack_insert_object(parser, NULL, 0, cur_obj)` (array append), skips
  insertion silently if `cur_obj == NULL` (the ext-ignore case — here it *is*
  safe because `container->e.len--` still happens inside
  `ucl_msgpack_insert_object`... but that function is only called `if
  (parser->cur_obj)`, so for array-ext the `e.len` is **not** decremented
  either → same "container never finishes" trap. Reproduce faithfully as: ext
  anywhere is a hard decode error in the Go port).
  Finished check same as above.
- `finish_array_value` / `finish_assoc_value` — call `ucl_msgpack_get_next_container`,
  which: if the current stack frame's `e.len == 0`, pops it (`parser->stack =
  cur->next`), sets `parser->cur_obj = cur->obj` (the just-completed
  container becomes the "value" for whatever comes next — a key's value or an
  array element in the *parent* frame), frees the frame, and **recurses** to pop
  further finished frames (handles nested containers finishing back-to-back).
  Determines `next_state` from the (possibly new) top frame's object type:
  `UCL_OBJECT → read_assoc_key`, else `read_array_value`. Then goes back to
  `read_type` to read the next key or value in the parent container.
- `error_state` — reaching this state at all inside the main loop is itself a
  decode error ("invalid state machine state"); it is the poisoned initial
  value of `next_state` and is only ever entered if a scalar type appears with
  nothing queuing a legitimate follow-up state (i.e. top-level scalar, see §2.1).

### 2.3 Container tracking

`struct ucl_stack` (from `ucl_internal.h`) is a **singly linked list acting as
a stack**, one frame per currently-open map/array: `{ obj *ucl_object_t; next
*ucl_stack; e.len uint64 (remaining pair/element count); chunk }`. Depth is
capped at `UCL_MAX_NESTING = 1024` (`ucl_msgpack_get_container` counts frames
each time a new container opens and errors "msgpack containers are nested too
deep" beyond that). This is a genuine **streaming stack**, not recursion —
there is no recursive descent for nested containers; nesting depth is bounded
only by this explicit counter.

Key vs value distinction is purely a function of current state (`read_assoc_key`
vs `read_assoc_value`/`read_array_value`) — there is no lookahead; the state
machine enforces that every odd element read inside a map is a key (must satisfy
`MSGPACK_FLAG_KEY`) and every even element is a value.

### 2.4 Completion signalling

The outer `while (p < end)` loop runs until all input bytes are consumed, then
a **separate switch on the final `state`** validates that stopping there was
legal:
- `start_array`/`start_assoc` as the terminal state is only valid if `len == 0`
  (i.e. we'd just opened an *empty* container as the very last thing and hadn't
  yet gotten around to actually materializing/inserting it in the main loop) —
  the code duplicates the container-creation logic here as a special case.
- `read_array_value`/`read_assoc_value` as terminal state requires `len == 0`
  (no partially-read value pending) then performs one final parse-and-insert
  (handles the case where the loop exited exactly as the last value's bytes
  were fully consumed, before the "is container finished" bookkeeping ran).
- `finish_array_value`/`finish_assoc_value`/`read_type` are valid "clean" ends.
- Anything else (notably `error_state`, or being mid-key) → decode error
  ("invalid state machine finishing state").

After that, `ucl_msgpack_get_next_container(parser)` is called once more to
unwind any fully-finished trailing frames, and finally **the whole parse is
rejected if `parser->stack != NULL`** ("incomplete container") — i.e. an
unclosed map/array at EOF is always an error, regardless of the state-switch
outcome above.

On success, `ucl_parse_msgpack` sets `parser->top_obj = parser->cur_obj` (only
if `top_obj` was previously unset — supports multi-chunk incremental feeds).

**Go port takeaway**: implement this as an explicit iterative state machine
(not recursion) operating over a byte slice + cursor, with an explicit stack of
`{obj, remainingCount}` frames capped at 1024 deep, mirroring the states above
1:1. Do not attempt to simplify into naive recursive-descent — the C's
handling of "container finishes exactly when its sibling value finishes" and
the end-of-input finishing-state legality switch are both load-bearing for the
malformed-input tests in §4.

---

## 3. ENCODER — emitter choices (must be byte-identical to C)

Emission is tree-walking (`ucl_emit_msgpack_elt`, dispatched from the generic
`ucl_object_emit`/`ucl_object_emit_len` via the `UCL_EMIT_MSGPACK` vtable row:
`ucl_emitter_operations{write_elt, start_object, start_array, end_object,
end_array}`; `end_object`/`end_array` are no-ops for msgpack because the count
is front-loaded into the map/array header — there is no closing byte). All
low-level byte writers go through `ctx->func->ucl_emitter_append_len` /
`ucl_emitter_append_character` (the pluggable sink — file, string buffer,
or user callback).

Per UCL type:

- **`UCL_INT`** → `ucl_emitter_print_int_msgpack`, chooses the *smallest*
  encoding by these exact thresholds (int64 value `val`):
  - `val >= 0`:
    - `val <= 0x7f` → positive fixint, 1 byte: `val & 0x7f`
    - `val <= UINT8_MAX (255)` → `0xcc` + 1 byte
    - `val <= UINT16_MAX (65535)` → `0xcd` + 2 bytes BE
    - `val <= UINT32_MAX (4294967295)` → `0xce` + 4 bytes BE
    - else → `0xcf` + 8 bytes BE
  - `val < 0`: let `uval = |val|` computed via the bithack
    `(val ^ (val>>63)) - (val>>63)` (arithmetic shift; for `val == INT64_MIN`
    this bithack itself overflows/wraps in the same way plain C signed overflow
    would — replicate with `uint64(val) * ...` two's-complement math, not
    `math.Abs`, to get identical behavior at `INT64_MIN`):
    - `val >= -32` → negative fixint, 1 byte: `int8(val)` cast raw
    - **else if `uval <= INT8_MAX (127)`** → `0xd0` + 1 byte
    - **else if `uval <= INT16_MAX (32767)`** → `0xd1` + 2 bytes BE
    - **else if `uval <= INT32_MAX (2147483647)`** → `0xd2` + 4 bytes BE
    - else → `0xd3` + 8 bytes BE
  - **Quirk to replicate exactly** (byte-identical requirement): because the
    cutoffs compare the *absolute value* against `INT{8,16,32}_MAX` rather than
    against `INT{8,16,32}_MIN`'s magnitude, the most-negative value
    representable in each width is bumped up to the *next wider* encoding:
    - `-128` (fits int8's range) is encoded as **int16**, not int8, because
      `uval = 128 > INT8_MAX(127)`.
    - `-32768` (fits int16's range) is encoded as **int32**, not int16, because
      `uval = 32768 > INT16_MAX(32767)`.
    - `-2147483648` (fits int32's range) is encoded as **int64**, not int32,
      because `uval = 2147483648 > INT32_MAX(2147483647)`.
    - Net exact negative ranges: fixint `[-32,-1]`; int8 `[-127,-33]`; int16
      `[-32767,-128]`; int32 `[-2147483647,-32768]`; int64 everything else
      (`< -2147483647`, including `INT64_MIN`).
    A Go implementation that "helpfully" uses the mathematically-correct
    smallest-width selection will *not* round-trip byte-identically against the
    C emitter/tests — this must be copied verbatim, quirk included.

- **`UCL_FLOAT`, `UCL_TIME`** → both go through `ucl_object_todouble` then
  `ucl_emitter_print_double_msgpack`: **always** `0xcb` (float64) + 8 bytes BE
  IEEE-754 double. float32 (`0xca`) is never produced by the encoder (decode-only
  format). `UCL_TIME` has no distinct wire representation — it round-trips back
  as `UCL_FLOAT`, not `UCL_TIME` (lossy for the Go port too, by design, matching C).

- **`UCL_BOOLEAN`** → `ucl_emitter_print_bool_msgpack`: single byte, `0xc3`
  (true) or `0xc2` (false).

- **`UCL_STRING`** → checks `obj->flags & UCL_OBJECT_BINARY`:
  - **not** binary → `ucl_emitter_print_string_msgpack`: str family chosen by
    length `len` (`obj->len`, byte length — not the C string's `strlen`, msgpack
    strings are not NUL-terminated on the wire):
    - `len <= 0x1f` → fixstr, 1 header byte `(len | 0xa0)`
    - `len <= 0xff` → `0xd9` + 1 length byte
    - `len <= 0xffff` → `0xda` + 2 length bytes BE
    - else → `0xdb` + 4 length bytes BE
    then the raw bytes.
  - **is** binary → `ucl_emitter_print_binary_string_msgpack`: bin family
    (no fixed/small form exists for bin — always at least a 1-byte-length
    prefix):
    - `len <= 0xff` → `0xc4` + 1 length byte
    - `len <= 0xffff` → `0xc5` + 2 length bytes BE
    - else → `0xc6` + 4 length bytes BE
    then the raw bytes.
  - Note the asymmetry: **strings** get a compact fixstr form for `len ≤ 31`
    that **binary** data never gets (binary always costs ≥ 2 bytes of header
    even for a 1-byte blob).

- **`UCL_NULL`** → `ucl_emitter_print_null_msgpack`: single byte `0xc0`.

- **`UCL_OBJECT`** → header via `ucl_emitter_print_object_msgpack(ctx, obj->len)`
  where `obj->len` is the **distinct-key count** (the hash's element count, not
  counting extra values chained onto a duplicate key):
  - `len <= 0xf` → fixmap, 1 byte `(len | 0x80)`
  - `len <= 0xffff` → `0xde` + 2 length bytes BE
  - else → `0xdf` + 4 length bytes BE
  then, for each distinct key (iteration order = hash/insertion iteration
  order via `ucl_object_iterate(obj, &it, true)`), emit the key as a string
  (via `ucl_emitter_print_key_msgpack`, i.e. always `ucl_emitter_print_string_msgpack`
  — **map keys are never emitted as binary**, regardless of any `UCL_OBJECT_BINARY`
  flag on the key material) followed by exactly **one** value: `LL_FOREACH`
  over the duplicate-key chain but `break`s after the first iteration — **only
  the first value of a multi-value (duplicate) key survives an msgpack
  round-trip; the rest are silently dropped.** This is called out explicitly in
  a code comment in `ucl_emitter.c`. The Go port must replicate: when emitting,
  ignore everything but `list.Front()` per key; the emitted map length must
  match "one value per distinct key", consistent with `obj->len` already being
  the distinct-key count.

- **`UCL_ARRAY`** → header via `ucl_emitter_print_array_msgpack(ctx, obj->len)`
  (`obj->len` = element count):
  - `len <= 0xf` → fixarray, 1 byte `(len | 0x90)`
  - `len <= 0xffff` → `0xdc` + 2 length bytes BE
  - else → `0xdd` + 4 length bytes BE
  then every element in order, no key prefix.

- **`UCL_USERDATA`** → treated as a string: if the userdata has an `emitter`
  callback it is invoked (`ud->emitter(obj->value.ud)`, falling back to the
  literal text `"null"` if the callback returns `NULL`) **but the C code then
  discards `ud_out` and emits `obj->value.sv`/`obj->len` unconditionally** via
  `ucl_emitter_print_string_msgpack` — i.e. as currently written, calling the
  emitter callback is dead code for msgpack output; whatever `obj->value.sv`/`len`
  already hold (the userdata's string-typed backing fields) is what actually
  gets emitted, as a plain (non-binary) string, never as binary regardless of
  any `UCL_OBJECT_BINARY` flag. Replicate this literally: for the Go port,
  emit userdata's string representation via the plain string path, not the
  binary path, and don't bother wiring up an emitter-callback indirection for
  msgpack specifically (it has no effect on the bytes produced).

Key emission: `ucl_emitter_print_key_msgpack(print_key, ctx, obj)` only fires
when `print_key` is true (i.e., the element is a map value, not an array
element or the top-level object itself) and always calls the **string** (not
binary) writer with `obj->key`/`obj->keylen`.

---

## 4. Error handling for malformed input (`tests/test_msgpack_malformed.c`)

Test harness: `parse_msgpack_fails(data, len)` returns true iff
`ucl_parser_add_chunk_full(parser, data, len, 0, UCL_DUPLICATE_APPEND,
UCL_PARSE_MSGPACK)` returns `false` (parser created with
`UCL_PARSER_DISABLE_MACRO`). `parse_msgpack_ok` is the inverse, additionally
requiring `ucl_parser_get_object` to return non-NULL.

Enumerated cases — **the Go decoder must reject/accept exactly these**:

1. **`test_issue363_fixext2_truncated`** — bytes `{0x9b, 0xd5, 0x61, 0x3a}`:
   fixarray(11) header claims 11 elements, but only a fixext2 (`0xd5`, needs
   type byte + 2 data bytes = 3 bytes) follows with just 2 bytes (`0x61,0x3a`)
   available. **Must fail** (this is the actual upstream regression: heap
   buffer over-read in `ucl_msgpack_parse_ignore` from missing bounds check
   after `len` is reassigned for fixext/ext types — the Go port must bounds-check
   *after* reassigning the ext length, not just against the table's raw `len`).
2. **`test_fixext1_truncated`** — `{0x91, 0xd4, 0xaa}`: fixarray(1) + fixext1
   (needs 2 bytes: type+1 data byte), only 1 byte available. **Fail.**
3. **`test_fixext4_truncated`** — `{0x91, 0xd6, 0x01,0x02,0x03}`: fixarray(1) +
   fixext4 (needs 5 bytes), only 3 available. **Fail.**
4. **`test_fixext8_truncated`** — `{0x91, 0xd7, 0x01,0x02,0x03,0x04}`:
   fixext8 needs 9 bytes, only 4 available. **Fail.**
5. **`test_fixext16_truncated`** — `{0x91, 0xd8, 0x01,0x02,0x03,0x04}`:
   fixext16 needs 17 bytes, only 4 available. **Fail.**
6. **`test_ext8_truncated`** — `{0x91, 0xc7, 0x0a, 0xbb, 0xcc}`: ext8 length
   byte says 10 data bytes (`ucl_msgpack_parse_ignore` requires `10+1=11` total
   including the ext type byte), only 2 remain. **Fail.**
7. **`test_ext16_truncated`** — `{0x91, 0xc8, 0x01,0x00, 0xff}`: ext16 2-byte
   length = 256 (needs `256+1=257`), only 1 byte remains. **Fail.**
8. **`test_empty_input`** — `data=NULL, len=0`. **Must succeed**, producing a
   non-NULL, empty top-level `UCL_OBJECT` — this is handled *before* the
   msgpack parser even runs: `ucl_parser_add_chunk_full` special-cases `len ==
   0` to synthesize `parser->top_obj = ucl_object_new_full(UCL_OBJECT,
   priority)` and return true without touching `ucl_parse_msgpack` at all. The
   Go port's public "parse msgpack chunk" entry point should special-case
   zero-length input the same way rather than routing it into the state
   machine (which itself asserts `remain != 0` and would need a different
   guard, matching the C's `assert(parser->chunks->remain != 0)` in
   `ucl_parse_msgpack`).
9. **`test_truncated_string`** — `{0x91, 0xd9}`: fixarray(1) + str8 header
   with zero length bytes following (needs 1). **Fail** ("not enough data
   remain to read object's length").
10. **`test_truncated_map_key`** — `{0x81, 0xa5, 'a', 'b'}`: fixmap(1) header,
    key declared as fixstr(5) but only 2 bytes ('a','b') follow. **Fail**
    (string parse function's `len > remain` check on the key, `"too long or
    empty key"`/`ret==-1` path).
11. **`test_truncated_int`** — `{0x91, 0xce, 0x00, 0x01}`: fixarray(1) +
    uint32 (needs 4 payload bytes), only 2 available. **Fail.**
12. **`test_unknown_format`** — `{0x91, 0xc1}`: `0xc1` is msgpack's permanently
    reserved/never-used byte, absent from libucl's dispatch table entirely.
    **Fail** ("unknown msgpack format").

General principles to extract for the Go port's error surface (not literal
test cases, but implied by the mechanism read in §2):
- Every "read N more bytes" step (header length field, string/binary/ext
  payload, fixed-width scalar payload) must check `N <= remaining_bytes` and
  produce a hard parse error otherwise — **never** read past the supplied
  buffer, never panic/index-out-of-range.
- A **zero-length map/array key** is an error ("too long or empty key"), even
  if otherwise well-formed (e.g. an empty fixstr as a key).
- An **unclosed container at EOF** (stack non-empty when input is exhausted)
  is always an error ("incomplete container"), even if every byte read so far
  was individually well-formed.
- A **non-container top-level value** is an error, whether caught by the
  first-byte heuristic or by the state machine ending in a state other than
  `read_type`/`finish_array_value`/`finish_assoc_value`.
- Nesting deeper than 1024 containers is an error ("nested too deep").
- A map key of a non-string/non-binary type (int, bool, nil, float, nested
  map/array) is an error ("bad type for key").
- `ext`/`fixext` types anywhere: matching the *letter* of the C code, a
  top-level or array-context ext is parsed-and-silently-skipped (no value
  emitted, array shrinks by one slot effectively vanishing that element as far
  as bookkeeping goes since... see the caveat in §2.4). Given this area of the
  C code is subtly under-specified/buggy and **not covered by any test**, the
  recommended Go behavior (deviating slightly from the literal C for
  robustness) is to treat `ext`/`fixext` as a **hard decode error** in any
  position (key, value, or standalone) rather than attempt to replicate the
  "silently produces no value, container bookkeeping possibly never
  completes" behavior. Document this as an intentional, minor divergence if
  taken.

---

## 5. Endianness & integer-width edge cases (recap)

- Wire byte order is **big-endian** throughout (lengths, ints ≥1 byte,
  floats). Go: use `encoding/binary.BigEndian.{Uint16,Uint32,Uint64}` for
  decode and `.Put*` for encode; no manual byte-swapping needed (unlike the C,
  which has to conditionally swap because it assumes native/host order and
  reads structs via `memcpy`).
- Decoded integers are always stored as a **signed 64-bit** value
  (`obj->value.iv`, `int64_t`) regardless of wire signedness — e.g. `uint64`
  values ≥ `2^63` are decoded via a `uint64→int64` bit-reinterpretation
  (`obj->value.iv = uiv64` where `uiv64` is `uint64_t`), which in C silently
  wraps to negative for the top half of the `uint64` range. **Reproduce this
  exactly in Go**: `int64(uiv64)` (Go's conversion is also a bit-reinterpreting
  wraparound for out-of-range unsigned→signed, matching C's implementation-
  defined-but-effectively-universal behavior) — do NOT clamp, saturate, or
  error on "unrepresentable" uint64 values; silently wrap, matching the C.
- Decoded float64 payload: the C reads the raw 8 bytes big-endian into a
  `uint64_t`, byte-swaps, and assigns that **bit pattern** directly into the
  object's `int64` union slot (`obj->value.iv = uiv64`) relying on C union
  aliasing to make it simultaneously readable as `obj->value.dv` (a `double`).
  In Go (no union), the direct equivalent is `math.Float64frombits(beUint64)`
  stored into the `float64` field. Do not round-trip through `int64` casts —
  use the bits verbatim.
- Decoded float32 payload: read 4 bytes BE as `uint32`, `math.Float32frombits`,
  then convert to `float64` (implicit widening, matching C's `float d.f`
  assigned into a `double obj->value.dv`).
- Signed small-int emission has the "absolute value vs `INT*_MAX`" boundary
  quirk detailed in §3 — the single most important byte-exact-compatibility
  trap in this codec. `INT64_MIN` negation via bithack must also be replicated
  bit-for-bit (two's complement absolute value, which for `INT64_MIN` doesn't
  actually invert the sign in real arithmetic — using `uint64(-val)` in Go
  when `val` is `math.MinInt64` requires care: compute
  `uval := uint64(val) ^ uint64(val>>63); uval -= uint64(val>>63)` performed in
  unsigned arithmetic mirrors the C bithack exactly for all `int64` inputs,
  including `MinInt64`, without triggering a Go signed-overflow panic (Go
  doesn't panic on signed overflow, but plain `-val` for `MinInt64` is still
  `MinInt64` due to wraparound, which happens to be numerically "wrong" as an
  abs value but is exactly what the C's `-val` would also produce if it took
  that path — replicating the bithack removes any ambiguity).
- Map/array/string/binary length fields are read/written as unsigned 8/16/32-bit
  quantities (`uint8`/`uint16`/`uint32`), never signed, and libucl imposes no
  extra ceiling beyond the wire format's own (`0xffffffff` for the 32-bit
  forms) — a 32-bit length is used as-is (cast up to `size_t`/`uint64_t`);
  there's no additional "sanity" cap besides the 1024-deep nesting counter and
  the remaining-bytes check for the payload.

---

## 6. What `test_msgpack.c` generates and asserts (mirror this in Go tests)

This is a **randomized round-trip fuzz test**, not fixed golden vectors: it
seeds a PCG32 PRNG from `/dev/urandom`, then for `niter = 20` iterations:

1. Builds a top-level `UCL_OBJECT`.
2. Inserts `ntests = 10` random top-level keys, each keyed by a random
   printable-ASCII string of random length 1..511 (`random_key`: uses
   `isgraph()` filter, so all bytes are visible, non-space ASCII), with a value
   produced by a randomly chosen generator from `{ucl_test_integer,
   ucl_test_string, ucl_test_boolean, ucl_test_map, ucl_test_array,
   ucl_test_null}` (6 generators total, indices 0..5; picked via `pcg32_random()
   % NTESTS`).
3. Additionally always inserts, under 3 more random keys: one
   `ucl_test_large_array` (65537 boolean elements — forces the `array32`
   encoding path), one `ucl_test_large_map` (65537 boolean-valued keys — forces
   `map32`), and one `ucl_test_large_string` (a random length in `[1,99999]`
   marked `UCL_OBJECT_BINARY` — forces `bin8/16/32` boundary crossing).
4. Emits with `ucl_object_emit_len(obj, UCL_EMIT_MSGPACK, &elen)`.
5. Re-parses the emitted bytes with a fresh parser
   (`ucl_parser_add_chunk_full(..., UCL_DUPLICATE_APPEND, UCL_PARSE_MSGPACK)`)
   and asserts it succeeds.
6. Re-emits the reparsed object and asserts **byte-for-byte identity** with the
   first emission (`elen2 == elen && memcmp(emitted, emitted2, elen) == 0`).
   Note this is emit→parse→emit identity (round-trip-emit), **not** a
   structural/value equality assertion on the parsed tree, and it is also not
   comparing against the *original in-memory* `obj` (which was already
   `ucl_object_unref`'d before reparsing) — so what's actually verified is
   "emit is deterministic and parse-of-emit-then-emit-again reproduces the same
   bytes", which implicitly validates both directions agree on the exact same
   type/width choices in §1/§3.

Generator details worth mirroring 1:1 in a Go fuzz/property test:

- `ucl_test_integer`: for each of `count = rand % 20` iterations, appends 14
  array elements: 3 signed/unsigned pairs at each of the 7-bit / 16-bit /
  32-bit / 64-bit width boundaries (`cur % 128`, `-(cur % 128)`, `cur %
  UINT16_MAX`, `-(cur % INT16_MAX)`, `cur % UINT32_MAX`, `-(cur % INT32_MAX)`,
  full 64-bit `cur`, `-cur`), plus several doubles built from scaled-down
  random values (some negative), plus one more int built by
  `memcpy`-reinterpreting a random 64-bit pattern as a double then... (actually
  the C appends it via `ucl_object_fromint(cur)` again — `curf` is computed but
  unused in the final append, effectively a second full-64-bit random int).
  This exercises every integer width bucket and boundary from §3 including the
  "excluded minimum" values only probabilistically (the boundary quirk isn't
  specifically targeted, so a Go test SHOULD add explicit unit vectors for
  exactly `-128, -32768, -2147483648, INT64_MIN` to pin the quirk, since the
  fuzz test won't reliably hit them).
- `ucl_test_string`: appends 4 random-length strings per iteration — two
  plain-text (`UCL_STRING_RAW`, lengths in `[1,127]` and `[1,511]`, i.e.
  crossing the fixstr/str8 and str8/str16 boundaries) and two more of the same
  two length ranges but flagged `UCL_OBJECT_BINARY` (crossing bin8's only
  boundary at 255 within the second range) — plus, once per top-level array,
  one 65537-byte binary string (forces bin32).
- `ucl_test_boolean`: 0..19 random booleans in an array.
- `ucl_test_map` / `ucl_test_array`: recursively nest by picking a random
  generator (any of the 6, including itself), capped by a shared `recursion`
  counter — once recursion depth > 10, re-rolls the generator choice to
  exclude `ucl_test_map`/`ucl_test_array` (bounds runaway nesting, but is a
  test-harness safety valve, not a libucl feature — the Go test can use the
  same cap or rely on its own timeout). `ucl_test_map` notably inserts **each
  key twice** ("multi value key") — this directly exercises the "only first
  value of a duplicate key survives msgpack round-trip" behavior from §3, so a
  Go round-trip test must NOT assert full structural equality of parsed vs.
  original for maps with duplicate keys — only that re-emission is stable.
- `ucl_test_null`: a single `UCL_NULL` object, no array wrapper.

A faithful Go test suite should implement: (a) unit tests with fixed byte
vectors for every row of the §1 table (one direction each: decode-only for
ext/fixext, both directions for everything else) and the signed-int boundary
quirk table in §3; (b) the exact malformed-input byte sequences in §4 as
table-driven "must error" tests; (c) a randomized round-trip test structurally
equivalent to the above (Go's `math/rand` seeded, or `testing/quick`), checking
emit→parse→emit byte-identity rather than tree equality.

---

## 7. Recommended Go design

- **Decoder**: operate over a single `[]byte` buffer with an integer cursor
  (mirrors the C's flat `pos`/`remain` over one chunk — libucl's msgpack
  decoder is not actually incremental/streaming across `io.Reader` boundaries
  despite the general UCL parser supporting chunked input; msgpack decoding
  always requires the *entire* chunk up front). A buffered `[]byte` + cursor
  (or `bytes.Reader` with manual bounds checks) is simpler and sufficient;
  there is no benefit to `io.Reader`-based streaming since libucl itself
  doesn't do it and the malformed-input tests assume a single fixed-size
  buffer. Use `encoding/binary.BigEndian` for all multi-byte length/int/float
  reads — do not hand-roll byte swaps.
- Implement the container stack as `[]struct{ obj *Object; remaining uint64 }`
  (a Go slice used as a stack, capped at 1024 entries) rather than a linked
  list — equivalent semantics to `struct ucl_stack`, simpler in Go, and
  trivially bounds-checked with `len(stack) >= 1024`.
- Implement the state machine as an explicit `switch` over a small `state`
  enum exactly mirroring §2.2's states (`readType, startAssoc, startArray,
  readAssocKey, readAssocValue, finishAssocValue, readArrayValue,
  finishArrayValue`) — do not attempt to collapse it into recursive-descent;
  the finishing-state legality check in §2.4 depends on knowing precisely
  which state the loop was in when the buffer ran out.
- Keys/values for strings can be zero-copy slices into the input `[]byte]`
  (Go slices alias safely, no need for libucl's `UCL_PARSER_ZEROCOPY`
  distinction/trash-stack machinery — that machinery exists in C only to
  manage ownership/lifetime, which Go's GC makes moot). Recommend: **always**
  behave like zero-copy internally (alias the input), and only copy
  defensively if the Go API contract requires the returned tree to outlive the
  input buffer (document this decision either way).
- **Encoder**: a small tree-walking emitter mirroring `ucl_emit_msgpack_elt`,
  writing directly into a `*bytes.Buffer` (or any `io.Writer` — encoding is
  naturally a single forward pass needing no lookahead, so `io.Writer` is fine
  here even though the decoder isn't a good `io.Reader` fit). Implement each
  `ucl_emitter_print_*_msgpack` function as a small pure function `(w
  *bytes.Buffer, v T)` taking the exact branch thresholds from §3 — consider
  a single shared helper for "write msgpack header byte(s) + BE length field"
  parameterized by the four (fixmask, l8/16/32-or-none, threshold) tuples used
  by strings/binary/arrays/maps, since those four all share the same
  "small-fixed / 8 (optional) / 16 / 32" shape.
- Encode signed integers via the exact §3 branch order/comparisons — write a
  dedicated helper function with the same "quirk" thresholds and a unit test
  pinning the four boundary values (`-128,-32768,-2147483648,MinInt64`) plus
  their "one less negative" neighbors, so any future refactor trips a test if
  it "fixes" the quirk.
- Represent the UCL object model's binary-vs-text string distinction as a
  bool flag on the string node (mirroring `UCL_OBJECT_BINARY`), and gate the
  str-vs-bin emission choice on that flag exactly as §3 describes — including
  the fact that **object keys are always emitted as text strings** even if
  somehow flagged binary, and **userdata is always emitted as a text string**
  regardless of its emitter callback's return value.
- Preserve **map iteration order** matching whatever the Go object model's
  natural iteration order is (insertion order is the natural analog of
  libucl's hash iteration order for this purpose) since the round-trip
  byte-identity test depends on stable, repeatable ordering across repeated
  emits of the same in-memory tree — it does not need to match the C library's
  own hash order (there's no cross-language byte-identity requirement, only
  intra-Go-run determinism for the round-trip test), but it must be
  deterministic.
- For duplicate keys (multiple values under one key, "implicit arrays" via a
  `next` chain in libucl's object model): when emitting msgpack, emit only the
  first value per key and use the distinct-key count as the map length —
  exactly mirroring §3's `LL_FOREACH ... break`.
