# MessagePack notes

Notes from porting `src/ucl_msgpack.c` and the msgpack half of
`src/ucl_emitter.c` (`msgpack.go`, `msgpack_decode.go`, `msgpack_encode.go`).

Nothing here asks for a change to any file this component does not own. No bug
was found in the object API.

## 1. Public naming: `EmitMsgPack` and `ParseMsgPack` are already constants

`types.go` defines `EmitMsgPack` (an `Emitter`) and `ParseMsgPack` (a
`ParseType`), so neither name is available for a function -- the same collision
`NOTES-emitter.md` §1 hit. The entry points are therefore:

```go
func DecodeMsgPack(data []byte) (*Object, error)                  // ucl_parse_msgpack
func DecodeMsgPackFull(data []byte, priority uint) (*Object, error)
func EncodeMsgPack(obj *Object) ([]byte, error)                   // ucl_object_emit_len
func EncodeMsgPackTo(w io.Writer, obj *Object) error
```

**What the dispatcher should call.** A general `EmitObject`/`(*Object).Emit`
should route `EmitMsgPack` to `EncodeMsgPackTo` (or `EncodeMsgPack` for the
`[]byte`-returning shape) and the four text formats to `EmitTextFull`, exactly
as `NOTES-emitter.md` §1 describes. A general parser entry point should route
`ParseMsgPack` to `DecodeMsgPackFull(data, priority)`; note it needs the whole
chunk up front, since libucl's msgpack decoder is not incremental either.

Duplicate-key policy is hard-coded to append (`InsertKey`), which is what
`UCL_DUPLICATE_APPEND` does for msgpack and what both C test drivers use. If
the parser layer ever needs the other three strategies here, the hook is
`msgpackDecoder.insertObject`; they need the parser's priority-aware
`ucl_parser_process_object_element`, which does not exist yet.

## 2. Deliberate divergence: ext/fixext is a hard decode error

Taken on the recommendation of `docs/msgpack.md` §4. The C skips an ext value's
bytes, produces no object, and then neither inserts anything nor decrements the
enclosing container's expected-element count.

I measured what that actually costs, against the reference build:

* 200 000 random byte strings (biased towards msgpack type bytes): C accepted
  125, this port accepted 118. All 7 differences contained an ext byte.
* 60 000 documents emitted by this port plus mutations of them: 45 755 accepted
  by C, 2 differences, both mutations that introduced an ext byte.

The mechanism in the C is: the ext consumes input but fills no slot, so the
container is one element short and swallows whatever follows. If enough
elements follow, the container finishes and the parse *succeeds* with the ext
element silently missing; if not, it fails at EOF with "incomplete container".
Either way the result is not something a caller can rely on, no test exercises
it, and it is not reachable from anything this library emits. Rejecting ext
outright is the documented divergence.

## 3. Container header lengths come from the contents, not `Object.length`

The C writes `obj->len` into the map/array header and then iterates
separately. This port writes the number of pairs/elements it is really about to
emit (`hash.Len()` for objects, the non-nil slot count for arrays). Two reasons:

* `ArrayMerge` miscounts `length` upstream (see its doc comment), so `obj->len`
  and the element count can disagree -- and a header that contradicts its
  payload produces a document *nothing* can decode.
* The C's msgpack parser increments `obj->len` for a wire-level duplicate key
  even though the hash gains no entry. Re-emitting such a tree gives
  `82 a1 61 01` -- a header saying two pairs followed by one pair. Captured
  from the reference build; `TestMsgPackEncodeDuplicateKeyKeepsFirstValue`
  records it.

For every tree built through the object API the two counts agree, so this does
not affect the byte-identity contract; `test_msgpack.c`'s documents are all in
that class.

## 4. New upstream finding: an empty root emits a document libucl cannot reparse

Candidate entry for `docs/findings.md` (I do not own that file, so it is
recorded here instead):

> **msgpack emission of an empty root cannot be reparsed.** Emitting an object
> or array with no members produces a single byte, `0x80` or `0x90`. Feeding
> that back to `ucl_parser_add_chunk_full(..., UCL_PARSE_MSGPACK)` fails with
> "read assoc value when no container represented": the finishing-state branch
> for an empty container checks `parser->stack` *before* inserting, and at the
> top level there is nothing on it. This is reachable from empty input, which
> the parser accepts by synthesising an empty `UCL_OBJECT`.
>
> **Decision: replicate.** Same reasoning as finding #3 -- byte-identical
> output is the contract, reparseable output is not something libucl provides.
> `TestMsgPackEmptyRootDoesNotReparse` pins both halves, and the decoder's
> fuzz target excludes single-byte emissions from its re-decode invariant for
> this reason.

A second quirk in the same family, also reproduced: a **zero-length value that
ends a map** (`81 a1 6b a0`, i.e. `{"k": ""}`) is rejected with "cannot insert
object with no key", because the C's end-of-input branch for `read_assoc_value`
passes a NULL key. The same map with another pair after it parses fine.
Confirmed against the reference build; the random differential corpus hit this
7 times in 20 000 documents and this port rejected exactly the same 7.

The two interact, which is how the fuzzer found the second one: a document with
duplicate keys can *become* one that ends in a zero-length value, because
re-emission drops the extra values of a duplicate key
(`82 a130 82 a130 a0 a13030 a13030` re-emits as `81 a130 81 a130 a0`). So
"decode -> encode -> decode" is not a property this codec has, and
`FuzzMsgPackDecode` asserts only that re-encoding always succeeds and that a
re-decode which *does* succeed re-encodes identically.

## 5. Strings are copied, not aliased

`docs/msgpack.md` §7 leaves this open. The decoder copies (via `FromLString`),
matching the C's default non-`UCL_PARSER_ZEROCOPY` behaviour, so the returned
tree neither aliases nor pins the caller's buffer.
`TestMsgPackDecodeCopiesInput` pins it.

Worth knowing: the C's copy for *binary* strings sets `trash_stack[UCL_TRASH_VALUE]`
but never repoints `value.sv` at it, so a decoded binary string still points
into the caller's chunk after the parse returns. Copying sidesteps that
entirely; there is nothing to reproduce because nothing observable in-process
depends on it.

## 6. Nothing needed from the frozen core or the object API

* `emitWriter` from `emit.go` is reused as the sink, unchanged.
* String objects are built with `FromLString`, which satisfies the
  `invariant_test.go` length invariant; the binary flag is then set directly
  (`o.flags |= FlagBinary`), the same way `object_test.go` and
  `test_msgpack.c` do it, because no exported setter exists. If one is ever
  added, `parseString` and the test helper `mpBinary` should use it.
* `ToStringSafe` returns `""` for a binary string (a documented C wart), so the
  tests read binary values through `ToLString`. Not a bug -- just a trap.

## 7. Test sizing

`test_msgpack.c` runs 20 iterations seeded from `/dev/urandom`.
`TestMsgPackRoundTripByteIdentity` uses a fixed seed (so a failure reproduces)
and 5 iterations, 1 under `-short`: each iteration builds a document with two
65 537-element containers plus a 100 KB binary string, and 5 iterations already
take ~4.5s. The generators, the recursion cap and the duplicate-key insertion
are all mirrored from the C.
