package goucl

import (
	"encoding/binary"
	"math"
)

// MessagePack decoding, porting ucl_parse_msgpack and ucl_msgpack_consume from
// src/ucl_msgpack.c.
//
// The C runs an explicit state machine over one flat byte cursor rather than
// recursive descent, with a linked-list stack of open containers; that shape is
// reproduced here one state at a time, because the end-of-input legality check
// and the "container finishes exactly when its last value does" bookkeeping are
// both load-bearing for the malformed-input tests. Nesting is bounded by the
// explicit msgpackMaxNesting counter, not by the Go stack.
//
// Unlike the general UCL parser, the msgpack decoder is not incremental: the C
// decodes one whole chunk at a time, so a []byte plus a cursor is the faithful
// as well as the simplest representation.

// msgpackState is enum e_msgpack_parser_state.
type msgpackState int

const (
	msgpackReadType msgpackState = iota
	msgpackStartAssoc
	msgpackStartArray
	msgpackReadAssocKey
	msgpackReadAssocValue
	msgpackFinishAssocValue
	msgpackReadArrayValue
	msgpackFinishArrayValue
	msgpackErrorState
)

// msgpackFrame is one open container, mirroring struct ucl_stack: the object
// being filled plus the number of elements (or key/value pairs) still expected.
type msgpackFrame struct {
	obj       *Object
	remaining uint64
}

// msgpackDecoder holds what the C keeps in struct ucl_parser plus the locals of
// ucl_msgpack_consume.
//
// p and remain mirror the C's two cursors, which it advances in lockstep; the
// invariant p+remain == len(buf) holds at every step. Both are kept so that the
// bounds checks can be transcribed from the C without reinterpretation.
type msgpackDecoder struct {
	buf    []byte
	p      int
	remain int

	stack []msgpackFrame
	cur   *Object // parser->cur_obj
	key   []byte  // parser->... the pending map key (a slice of buf)

	// priority is applied to every object created, as the C applies
	// parser->chunks->priority.
	priority uint
}

// DecodeMsgPack decodes a complete MessagePack document into an Object tree.
//
// It mirrors ucl_parser_add_chunk_full(..., UCL_DUPLICATE_APPEND,
// UCL_PARSE_MSGPACK) followed by ucl_parser_get_object, for a parser holding no
// previous state. Duplicate keys are appended, so a repeated key becomes an
// implicit array, exactly as UCL_DUPLICATE_APPEND does.
//
// As in the C, empty input is not an error: it produces an empty TypeObject.
// That case is handled by ucl_parser_add_chunk_full before the msgpack code
// runs at all (which asserts a non-empty chunk), and is reproduced here.
//
// The top-level value must be a map or an array; every other document is
// rejected, including a lone empty container such as a bare 0x90, which the C
// rejects with "read assoc value when no container represented".
func DecodeMsgPack(data []byte) (*Object, error) {
	return DecodeMsgPackFull(data, 0)
}

// DecodeMsgPackFull is DecodeMsgPack with an explicit priority, matching the
// priority argument of ucl_parser_add_chunk_full: every object the decode
// creates is given that priority.
func DecodeMsgPackFull(data []byte, priority uint) (*Object, error) {
	if len(data) == 0 {
		// ucl_parser_add_chunk_full's len == 0 special case: synthesize an
		// empty top-level object and return success without entering the
		// msgpack parser, whose first act is to assert a non-empty chunk.
		return NewFull(TypeObject, priority), nil
	}

	// The top-level gate from ucl_parse_msgpack. It rejects positive fixints
	// outright; anything else that is not really a container is caught later by
	// the state machine, which has nowhere to put a bare scalar.
	if data[0]&0x80 != 0x80 && !(data[0] >= 0xdc && data[0] <= 0xdf) {
		return nil, msgpackErrf("bad top level object for msgpack")
	}

	d := &msgpackDecoder{buf: data, remain: len(data), priority: priority}
	if err := d.consume(); err != nil {
		return nil, err
	}
	if d.cur == nil {
		return nil, msgpackErrf("bad top level object for msgpack")
	}
	// docs/reference/msgpack.md section 2.1: the first-byte heuristic alone does not
	// guarantee a container, so check the result too. Unreachable in practice
	// -- the state machine has already rejected every non-container document --
	// but it is the assertion that makes "the root is a container" a contract.
	if d.cur.ObjType != TypeObject && d.cur.ObjType != TypeArray {
		return nil, msgpackErrf("bad top level object for msgpack")
	}
	return d.cur, nil
}

// consume is ucl_msgpack_consume: the state machine proper.
func (d *msgpackDecoder) consume() error {
	state := msgpackReadType
	// error_state is the poisoned initial value: a scalar read with nothing
	// queueing a follow-up state lands here, which is how a top-level scalar
	// is rejected.
	nextState := msgpackErrorState

	var entry *msgpackParserEntry
	var length uint64

	for d.p < len(d.buf) {
		switch state {
		case msgpackReadType:
			entry = msgpackDispatch[d.buf[d.p]]
			if entry == nil {
				return msgpackErrf("unknown msgpack format: %x", d.buf[d.p])
			}

			if entry.flags&msgpackFlagFixed != 0 {
				if entry.length == 0 {
					// Embedded size: the low bits of the type byte.
					length = uint64(d.buf[d.p] &^ entry.prefix)
				} else {
					// Note that remain still counts the type byte here, so
					// this check is one byte weaker than it looks; the
					// per-type parse function below repeats it properly.
					if d.remain < int(entry.length) {
						return msgpackErrf("not enough data remain to read object's "+
							"length: %d remain, %d needed", d.remain, entry.length)
					}
					length = uint64(entry.length)
				}

				if entry.flags&msgpackFlagTypeValue == 0 {
					// Step over the type byte; the payload starts here.
					if d.remain > 0 {
						d.p++
						d.remain--
					}
				} else {
					// The type byte is the value, so it is consumed by the
					// parse function instead, and the length is irrelevant.
					length = 0
				}
			} else {
				// The length lives in a big-endian field after the type byte.
				d.remain--
				if d.remain < int(entry.length) {
					return msgpackErrf("not enough data remain to read object's "+
						"length: %d remain, %d needed", d.remain, entry.length)
				}
				d.p++

				switch entry.length {
				case 1:
					length = uint64(d.buf[d.p])
				case 2:
					length = uint64(binary.BigEndian.Uint16(d.buf[d.p:]))
				case 4:
					length = uint64(binary.BigEndian.Uint32(d.buf[d.p:]))
				case 8:
					length = binary.BigEndian.Uint64(d.buf[d.p:])
				default:
					return msgpackErrf("invalid length of the length field: %d", entry.length)
				}

				d.p += int(entry.length)
				d.remain -= int(entry.length)
			}

			switch {
			case entry.flags&msgpackFlagAssoc != 0:
				state = msgpackStartAssoc
			case entry.flags&msgpackFlagContainer != 0:
				state = msgpackStartArray
			default:
				state = nextState
			}

		case msgpackStartAssoc, msgpackStartArray:
			typ := TypeObject
			if state == msgpackStartArray {
				typ = TypeArray
			}
			if err := d.startContainer(typ, length); err != nil {
				return err
			}
			// The C clears key/keylen in start_assoc but not in start_array;
			// the difference is invisible, because an array opened as a map
			// value only ever inserts its own elements (with no key) before
			// the next read_assoc_key overwrites the stale one.
			d.key = nil

			if length > 0 {
				state = msgpackReadType
				if typ == TypeObject {
					nextState = msgpackReadAssocKey
				} else {
					nextState = msgpackReadArrayValue
				}
			} else if typ == TypeObject {
				state = msgpackFinishAssocValue
			} else {
				state = msgpackFinishArrayValue
			}

		case msgpackReadAssocKey:
			// Keys must be str or bin; the C checks only the generic KEY flag,
			// so a binary key is accepted and used verbatim.
			if entry.flags&msgpackFlagKey == 0 {
				return msgpackErrf("bad type for key: %d, expected string", entry.format)
			}
			if length == 0 || length > uint64(d.remain) {
				return msgpackErrf("too long or empty key")
			}
			d.key = d.buf[d.p : d.p+int(length)]
			d.p += int(length)
			d.remain -= int(length)

			state = msgpackReadType
			nextState = msgpackReadAssocValue

		case msgpackReadArrayValue, msgpackReadAssocValue:
			assoc := state == msgpackReadAssocValue
			if len(d.stack) == 0 {
				return msgpackErrf("read assoc value when no container represented")
			}
			n, err := d.parseValue(entry, length)
			if err != nil {
				return err
			}
			d.p += n
			d.remain -= n
			// CONSUME_RET drops the parser; the next read_type installs a new
			// one. Nothing may dereference it in between.
			entry = nil

			if assoc && len(d.key) == 0 {
				// The C asserts key != NULL && keylen > 0 here. It can only
				// fire if the state machine itself is broken.
				return msgpackErrf("cannot insert object with no key")
			}
			if d.cur != nil {
				var key []byte
				if assoc {
					key = d.key
				}
				if err := d.insertObject(key, d.cur); err != nil {
					return err
				}
			}
			if assoc {
				d.key = nil
			}

			if d.stack[len(d.stack)-1].remaining == 0 {
				if assoc {
					state = msgpackFinishAssocValue
				} else {
					state = msgpackFinishArrayValue
				}
			} else {
				state = msgpackReadType
				if assoc {
					nextState = msgpackReadAssocKey
				} else {
					nextState = msgpackReadArrayValue
				}
			}

		case msgpackFinishArrayValue, msgpackFinishAssocValue:
			// GET_NEXT_STATE: unwind every finished frame, then continue in
			// whatever container is now on top.
			frame := d.getNextContainer()
			if frame == nil {
				return msgpackErrf("empty container")
			}
			if frame.obj.ObjType == TypeObject {
				nextState = msgpackReadAssocKey
			} else {
				nextState = msgpackReadArrayValue
			}
			state = msgpackReadType

		case msgpackErrorState:
			return msgpackErrf("invalid state machine state")
		}
	}

	// Stopping is legal only in some states; this is the switch that runs
	// after the C's while loop.
	switch state {
	case msgpackStartArray, msgpackStartAssoc:
		// An empty container as the very last thing in the input: the main
		// loop never got to materialize it, so it is built here instead.
		if length != 0 {
			return msgpackErrf("invalid non-empty container at the end; len=%d", length)
		}
		typ := TypeObject
		if state == msgpackStartArray {
			typ = TypeArray
		}
		// The C checks the stack before inserting rather than after, which is
		// what makes a *lone* empty container -- a bare 0x90 or 0x80 -- a
		// parse error rather than an empty document.
		if len(d.stack) == 0 {
			return msgpackErrf("read assoc value when no container represented")
		}
		if err := d.startContainer(typ, 0); err != nil {
			return err
		}

	case msgpackReadArrayValue, msgpackReadAssocValue:
		if length != 0 {
			return msgpackErrf("unfinished value at the end")
		}
		if len(d.stack) == 0 {
			return msgpackErrf("read assoc value when no container represented")
		}
		if entry == nil {
			return msgpackErrf("invalid state machine finishing state: %d", state)
		}
		n, err := d.parseValue(entry, length)
		if err != nil {
			return err
		}
		d.p += n
		d.remain -= n
		if d.cur != nil {
			// The C passes a NULL key even in the assoc case, so a
			// zero-length value that ends a map -- {"k": ""} as the last pair
			// -- fails with "cannot insert object with no key". Reproduced:
			// insertObject rejects an empty key for an object container.
			if err := d.insertObject(nil, d.cur); err != nil {
				return err
			}
		}

	case msgpackFinishArrayValue, msgpackFinishAssocValue, msgpackReadType:
		// Valid finishing states.

	default:
		return msgpackErrf("invalid state machine finishing state: %d", state)
	}

	// Unwind any trailing finished frames, then insist that nothing is left
	// open: an unclosed container at EOF is always an error.
	d.getNextContainer()
	if len(d.stack) != 0 {
		return msgpackErrf("incomplete container")
	}
	return nil
}

// startContainer is the shared body of the start_assoc and start_array states:
// build the container, insert it into its parent, push a frame for it, and
// record it as the frame's object (which is all ucl_msgpack_parse_map and
// ucl_msgpack_parse_array do).
func (d *msgpackDecoder) startContainer(typ Type, length uint64) error {
	d.cur = NewFull(typ, d.priority)

	// Insert into the enclosing container first: the parent's remaining count
	// must drop before the new frame goes on the stack.
	if len(d.stack) > 0 {
		if err := d.insertObject(d.key, d.cur); err != nil {
			return err
		}
	}
	if len(d.stack) >= msgpackMaxNesting {
		return msgpackErrf("msgpack containers are nested too deep (over %d)", msgpackMaxNesting)
	}
	d.stack = append(d.stack, msgpackFrame{obj: d.cur, remaining: length})
	return nil
}

// insertObject is ucl_msgpack_insert_object: add obj to the innermost open
// container and decrement that container's expected-element count.
//
// A nil key means "array append"; for an object container an empty key is the
// C's "cannot insert object with no key" error.
func (d *msgpackDecoder) insertObject(key []byte, obj *Object) error {
	if len(d.stack) == 0 || obj == nil {
		// Both are assertions in the C.
		return msgpackErrf("read assoc value when no container represented")
	}
	frame := &d.stack[len(d.stack)-1]
	if frame.remaining == 0 {
		// assert(container->e.len > 0) in the C: the state machine only ever
		// inserts into a container that still expects something.
		return msgpackErrf("bad container type")
	}

	switch frame.obj.ObjType {
	case TypeArray:
		ArrayAppend(frame.obj, obj)

	case TypeObject:
		if len(key) == 0 {
			return msgpackErrf("cannot insert object with no key")
		}
		// InsertKey with copyKey is the non-zero-copy path
		// (ucl_copy_key_trash), and its duplicate handling -- splice onto the
		// existing chain, leaving the distinct-key count alone -- is what
		// UCL_DUPLICATE_APPEND does for msgpack input.
		frame.obj.InsertKey(obj, key, true)

	default:
		return msgpackErrf("bad container type")
	}

	frame.remaining--
	return nil
}

// getNextContainer is ucl_msgpack_get_next_container: pop every frame whose
// element count has reached zero, publishing each completed container as
// cur_obj -- so the innermost finished container becomes the value its parent
// was waiting for -- and return the frame now on top, or nil when the stack has
// been emptied.
//
// The C recurses; a loop is the same thing without the stack depth.
func (d *msgpackDecoder) getNextContainer() *msgpackFrame {
	for len(d.stack) > 0 {
		top := &d.stack[len(d.stack)-1]
		if top.remaining != 0 {
			return top
		}
		d.cur = top.obj
		d.stack = d.stack[:len(d.stack)-1]
	}
	return nil
}

// parseValue dispatches to the per-type parse function the table row names and
// returns the number of bytes the value occupies from d.p onwards. It sets
// d.cur to the object produced.
//
// Every branch bounds-checks its payload against remain before reading, which
// is the C's `if (len > remain) return -1` guard; CONSUME_RET turns that into
// the "cannot parse type %d of len %u" error.
func (d *msgpackDecoder) parseValue(e *msgpackParserEntry, length uint64) (int, error) {
	if length > uint64(d.remain) {
		return 0, msgpackErrf("cannot parse type %d of len %d", e.format, length)
	}

	switch e.format {
	case msgpackFixmap, msgpackMap16, msgpackMap32,
		msgpackFixarray, msgpackArray16, msgpackArray32:
		// ucl_msgpack_parse_map / _array: the container was already built by
		// the start_assoc/start_array state, and these consume no bytes.
		return 0, nil

	case msgpackFixstr, msgpackStr8, msgpackStr16, msgpackStr32,
		msgpackBin8, msgpackBin16, msgpackBin32:
		return d.parseString(e, length)

	case msgpackPositiveFixint, msgpackNegativeFixint,
		msgpackUint8, msgpackUint16, msgpackUint32, msgpackUint64,
		msgpackInt8, msgpackInt16, msgpackInt32, msgpackInt64:
		return d.parseInt(e)

	case msgpackFloat32, msgpackFloat64:
		return d.parseFloat(e)

	case msgpackTrue, msgpackFalse:
		return d.parseBool(e)

	case msgpackNil:
		// ucl_msgpack_parse_null. Like the booleans this is a type-value
		// format, so the type byte itself is what gets consumed.
		if d.remain < 1 {
			return 0, msgpackErrf("cannot parse type %d of len %d", e.format, 1)
		}
		d.cur = NewFull(TypeNull, d.priority)
		return 1, nil

	default:
		// ext and fixext. See msgpackParseIgnore.
		return d.parseIgnore(e, length)
	}
}

// parseString is ucl_msgpack_parse_string. bin8/16/32 additionally set the
// binary flag, which is what makes the emitter choose the bin family again on
// the way out.
//
// The C aliases the input buffer and then copies into the object's trash
// storage unless UCL_PARSER_ZEROCOPY is set; this port always copies, so the
// tree never pins or aliases the caller's slice. (The C's copy for *binary*
// strings leaves value.sv pointing at the input chunk -- a dangling pointer
// once the chunk is freed -- which copying sidesteps entirely.)
func (d *msgpackDecoder) parseString(e *msgpackParserEntry, length uint64) (int, error) {
	n := int(length)
	obj := FromLString(d.buf[d.p : d.p+n])
	obj.SetPriority(d.priority)
	if e.format >= msgpackBin8 && e.format <= msgpackBin32 {
		obj.flags |= FlagBinary
	}
	d.cur = obj
	return n, nil
}

// parseInt is ucl_msgpack_parse_int. Every width lands in the same signed
// 64-bit slot, so a uint64 above 2^63-1 wraps to a negative value exactly as
// the C's assignment through the union does.
func (d *msgpackDecoder) parseInt(e *msgpackParserEntry) (int, error) {
	var iv int64
	var n int

	switch e.format {
	case msgpackPositiveFixint:
		if d.remain < 1 {
			return 0, msgpackErrf("cannot parse type %d of len %d", e.format, 1)
		}
		iv = int64(d.buf[d.p] & 0x7f)
		n = 1
	case msgpackNegativeFixint:
		if d.remain < 1 {
			return 0, msgpackErrf("cannot parse type %d of len %d", e.format, 1)
		}
		iv = int64(int8(d.buf[d.p]))
		n = 1
	case msgpackUint8:
		iv = int64(d.buf[d.p])
		n = 1
	case msgpackInt8:
		iv = int64(int8(d.buf[d.p]))
		n = 1
	case msgpackUint16:
		iv = int64(binary.BigEndian.Uint16(d.buf[d.p:]))
		n = 2
	case msgpackInt16:
		iv = int64(int16(binary.BigEndian.Uint16(d.buf[d.p:])))
		n = 2
	case msgpackUint32:
		iv = int64(binary.BigEndian.Uint32(d.buf[d.p:]))
		n = 4
	case msgpackInt32:
		iv = int64(int32(binary.BigEndian.Uint32(d.buf[d.p:])))
		n = 4
	case msgpackUint64, msgpackInt64:
		iv = int64(binary.BigEndian.Uint64(d.buf[d.p:]))
		n = 8
	}

	obj := NewFull(TypeInt, d.priority)
	obj.setInt(iv)
	obj.SetPriority(d.priority)
	d.cur = obj
	return n, nil
}

// parseFloat is ucl_msgpack_parse_float. The C reads the float64 payload as a
// bit pattern and stores it in the int64 half of the union, relying on aliasing
// to make it readable as a double; math.Float64frombits is the direct
// equivalent. float32 widens to float64, and is decode-only -- the emitter
// never produces it.
func (d *msgpackDecoder) parseFloat(e *msgpackParserEntry) (int, error) {
	obj := NewFull(TypeFloat, d.priority)
	var n int
	if e.format == msgpackFloat32 {
		obj.setFloat(float64(math.Float32frombits(binary.BigEndian.Uint32(d.buf[d.p:]))))
		n = 4
	} else {
		obj.setFloat(math.Float64frombits(binary.BigEndian.Uint64(d.buf[d.p:])))
		n = 8
	}
	obj.SetPriority(d.priority)
	d.cur = obj
	return n, nil
}

// parseBool is ucl_msgpack_parse_bool. The type byte is the value, so one byte
// is consumed.
func (d *msgpackDecoder) parseBool(e *msgpackParserEntry) (int, error) {
	if d.remain < 1 {
		return 0, msgpackErrf("cannot parse type %d of len %d", e.format, 1)
	}
	obj := NewFull(TypeBoolean, d.priority)
	obj.setBool(e.format == msgpackTrue)
	obj.SetPriority(d.priority)
	d.cur = obj
	return 1, nil
}

// parseIgnore stands in for ucl_msgpack_parse_ignore.
//
// Deliberate divergence (docs/reference/msgpack.md sections 2.2 and 4): the C skips an
// ext/fixext value's bytes and produces no object at all, but then neither
// inserts anything nor decrements the enclosing container's expected-element
// count -- so the container can never finish and the parse fails at EOF with
// "incomplete container" regardless. Since every position an ext can appear in
// is therefore already an error in the C (as a key it fails the KEY-flag
// check; at top level it falls into error_state; inside a container it hangs
// the container), rejecting it outright changes only the error message, and it
// avoids reproducing a bug that no test exercises.
//
// The C's own bounds handling here is the subject of issue #363: it validated
// the table's raw length but not the reassigned ext length (data + 1 type
// byte), which over-read the heap. Those inputs are rejected here for the more
// basic reason that ext is not supported at all.
func (d *msgpackDecoder) parseIgnore(e *msgpackParserEntry, length uint64) (int, error) {
	return 0, msgpackErrf("bad type: %x", e.format)
}
