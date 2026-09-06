package core

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"io"
	"math"
)

// MessagePack emission, porting ucl_emit_msgpack_elt from src/ucl_emitter.c
// and the ucl_emitter_print_*_msgpack primitives from src/ucl_msgpack.c.
//
// There is no closing byte in msgpack -- a container's element count is written
// in its header -- which is why the C's end_object/end_array vtable entries are
// empty and why nothing here mirrors them.
//
// Byte-for-byte compatibility with the C emitter is the contract: test_msgpack.c
// asserts emit -> parse -> emit identity, so the width chosen for every value
// must match, including the negative-integer quirk reproduced in
// msgpackWriteInt.

// EncodeMsgPack renders obj as a MessagePack document, mirroring
// ucl_object_emit_len(obj, UCL_EMIT_MSGPACK, &len).
//
// As in the C, a nil obj produces no output and no error.
func EncodeMsgPack(obj *Object) ([]byte, error) {
	if obj == nil {
		return nil, nil
	}
	var buf bytes.Buffer
	if err := EncodeMsgPackTo(&buf, obj); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// EncodeMsgPackTo renders obj as MessagePack to w.
//
// This is the entry point a general Emit dispatcher should route EmitMsgPack
// to; the EmitText* functions in emit.go deliberately reject it, since msgpack
// shares none of the text emitters' machinery.
func EncodeMsgPackTo(w io.Writer, obj *Object) error {
	if obj == nil {
		return nil
	}
	bw := bufio.NewWriter(w)
	e := &msgpackEmitter{w: &emitWriter{w: bw}}
	e.writeElt(obj, false)
	if e.w.err != nil {
		return e.w.err
	}
	return bw.Flush()
}

// msgpackEmitter is the msgpack ucl_emitter_context. Unlike the text formats
// there is no indentation, no separators and no comment handling, so the sink
// is all the state there is.
type msgpackEmitter struct {
	w *emitWriter
}

// writeElt is ucl_emit_msgpack_elt: write one value, optionally preceded by its
// key. It recurses for containers, as the C does.
func (e *msgpackEmitter) writeElt(obj *Object, printKey bool) {
	switch obj.ObjType {
	case TypeInt:
		e.writeKey(printKey, obj)
		e.writeInt(ToInt(obj))

	case TypeFloat, TypeTime:
		// UCL_TIME has no wire representation of its own and comes back as a
		// plain float; the loss is by design, matching the C.
		e.writeKey(printKey, obj)
		e.writeDouble(ToDouble(obj))

	case TypeBoolean:
		e.writeKey(printKey, obj)
		e.writeBool(ToBoolean(obj))

	case TypeString:
		e.writeKey(printKey, obj)
		if obj.flags&FlagBinary != 0 {
			e.writeBinaryString(obj.sv)
		} else {
			e.writeString(obj.sv)
		}

	case TypeNull:
		e.writeKey(printKey, obj)
		e.writeNull()

	case TypeObject:
		e.writeKey(printKey, obj)
		e.writeObjectHeader(uint64(obj.ov.Len()))
		obj.ov.Iterate(func(cur *Object) bool {
			// The C walks the duplicate-key chain with LL_FOREACH but breaks
			// after the first link, with a comment saying so: a map's length
			// is written once in the header, so only the first value of a
			// multi-value key survives the round trip and the rest are
			// silently dropped.
			e.writeElt(cur, true)
			return true
		})

	case TypeArray:
		e.writeKey(printKey, obj)
		e.writeArrayHeader(uint64(msgpackArrayLen(obj)))
		for _, cur := range obj.av {
			if cur == nil {
				// ucl_object_iterate skips empty slots.
				continue
			}
			e.writeElt(cur, false)
		}

	case TypeUserdata:
		// The C calls the userdata's emitter callback and then throws the
		// result away, emitting the object's own string fields instead --
		// always through the plain string writer, never the binary one. That
		// makes the callback dead code for msgpack, so there is nothing to
		// call here.
		e.writeKey(printKey, obj)
		e.writeString(obj.sv)
	}
}

// msgpackArrayLen counts the elements an array will actually emit.
//
// The C writes obj->len here and iterates separately, so a tracked length that
// has drifted from the real element count (ucl_array_merge miscounts; see
// ArrayMerge) produces a header that contradicts the payload and a document
// nothing can decode. Counting what is about to be written keeps the output
// self-describing; for every array built through the object API the two agree.
func msgpackArrayLen(obj *Object) int {
	n := 0
	for _, cur := range obj.av {
		if cur != nil {
			n++
		}
	}
	return n
}

// writeKey is ucl_emitter_print_key_msgpack. Keys are always written with the
// str family, never bin, whatever flags the object carries.
func (e *msgpackEmitter) writeKey(printKey bool, obj *Object) {
	if printKey {
		e.writeString(obj.key)
	}
}

// writeInt is ucl_emitter_print_int_msgpack: the smallest encoding that holds
// val, by the C's exact thresholds.
func (e *msgpackEmitter) writeInt(val int64) {
	var buf [9]byte
	var n int

	if val >= 0 {
		switch {
		case val <= 0x7f:
			buf[0] = byte(val) & 0x7f
			n = 1
		case val <= math.MaxUint8:
			buf[0] = 0xcc
			buf[1] = byte(val)
			n = 2
		case val <= math.MaxUint16:
			buf[0] = 0xcd
			binary.BigEndian.PutUint16(buf[1:], uint16(val))
			n = 3
		case val <= math.MaxUint32:
			buf[0] = 0xce
			binary.BigEndian.PutUint32(buf[1:], uint32(val))
			n = 5
		default:
			buf[0] = 0xcf
			binary.BigEndian.PutUint64(buf[1:], uint64(val))
			n = 9
		}
	} else {
		// The C's bithack abs, ((val ^ (val >> 63)) - (val >> 63)), written in
		// unsigned arithmetic so that MinInt64 -- whose magnitude has no int64
		// -- behaves identically instead of relying on signed wraparound.
		sign := uint64(val >> 63) // all ones for a negative val
		uval := (uint64(val) ^ sign) - sign

		switch {
		case val >= -32:
			buf[0] = byte(int8(val))
			n = 1

		// DELIBERATE BUG-FOR-BUG REPRODUCTION (docs/findings.md #4).
		// These bounds compare the magnitude against INT8_MAX / INT16_MAX /
		// INT32_MAX instead of the matching _MIN magnitude, so the most
		// negative value of each width does not fit its own width and is
		// promoted one step: -128 emits as int16, -32768 as int32 and
		// -2147483648 as int64. The exact ranges that result are int8
		// [-127,-33], int16 [-32767,-128], int32 [-2147483647,-32768], int64
		// everything below that. A mathematically correct encoder would emit
		// fewer bytes and fail test_msgpack.c's byte-identity assertion, so
		// this must not be "fixed".
		case uval <= math.MaxInt8:
			buf[0] = 0xd0
			buf[1] = byte(val)
			n = 2
		case uval <= math.MaxInt16:
			buf[0] = 0xd1
			binary.BigEndian.PutUint16(buf[1:], uint16(val))
			n = 3
		case uval <= math.MaxInt32:
			buf[0] = 0xd2
			binary.BigEndian.PutUint32(buf[1:], uint32(val))
			n = 5
		default:
			buf[0] = 0xd3
			binary.BigEndian.PutUint64(buf[1:], uint64(val))
			n = 9
		}
	}

	e.w.write(buf[:n])
}

// writeDouble is ucl_emitter_print_double_msgpack: always float64, never the
// narrower float32 form, which the emitter has no path to.
func (e *msgpackEmitter) writeDouble(val float64) {
	var buf [9]byte
	buf[0] = 0xcb
	binary.BigEndian.PutUint64(buf[1:], math.Float64bits(val))
	e.w.write(buf[:])
}

// writeBool is ucl_emitter_print_bool_msgpack.
func (e *msgpackEmitter) writeBool(val bool) {
	if val {
		e.w.writeByte(0xc3)
	} else {
		e.w.writeByte(0xc2)
	}
}

// writeNull is ucl_emitter_print_null_msgpack.
func (e *msgpackEmitter) writeNull() {
	e.w.writeByte(0xc0)
}

// writeString is ucl_emitter_print_string_msgpack: the str family, which has a
// one-byte fixstr form for lengths up to 31.
//
// The length is len(s) rather than the object's tracked length, matching how
// the text emitters read string values (see docs/notes/emitter.md section 3); the C
// uses obj->len, which for msgpack is the same number for every string the
// object API can build.
func (e *msgpackEmitter) writeString(s []byte) {
	var buf [5]byte
	var n int

	switch l := uint64(len(s)); {
	case l <= 0x1f:
		buf[0] = byte(l) | 0xa0
		n = 1
	case l <= 0xff:
		buf[0] = 0xd9
		buf[1] = byte(l)
		n = 2
	case l <= 0xffff:
		buf[0] = 0xda
		binary.BigEndian.PutUint16(buf[1:], uint16(l))
		n = 3
	default:
		buf[0] = 0xdb
		binary.BigEndian.PutUint32(buf[1:], uint32(l))
		n = 5
	}

	e.w.write(buf[:n])
	e.w.write(s)
}

// writeBinaryString is ucl_emitter_print_binary_string_msgpack: the bin family,
// which has no compact form, so even a one-byte blob costs two header bytes.
func (e *msgpackEmitter) writeBinaryString(s []byte) {
	var buf [5]byte
	var n int

	switch l := uint64(len(s)); {
	case l <= 0xff:
		buf[0] = 0xc4
		buf[1] = byte(l)
		n = 2
	case l <= 0xffff:
		buf[0] = 0xc5
		binary.BigEndian.PutUint16(buf[1:], uint16(l))
		n = 3
	default:
		buf[0] = 0xc6
		binary.BigEndian.PutUint32(buf[1:], uint32(l))
		n = 5
	}

	e.w.write(buf[:n])
	e.w.write(s)
}

// writeArrayHeader is ucl_emitter_print_array_msgpack.
func (e *msgpackEmitter) writeArrayHeader(n uint64) {
	e.writeContainerHeader(n, 0x90, 0xdc, 0xdd)
}

// writeObjectHeader is ucl_emitter_print_object_msgpack. n is the distinct-key
// count: one value per key is written, so a duplicate-key chain still counts
// once.
func (e *msgpackEmitter) writeObjectHeader(n uint64) {
	e.writeContainerHeader(n, 0x80, 0xde, 0xdf)
}

// writeContainerHeader is the shape both container headers share: a four-bit
// count folded into a single byte, or a 16- or 32-bit count after a marker.
func (e *msgpackEmitter) writeContainerHeader(n uint64, fixMask, l16, l32 byte) {
	var buf [5]byte
	var blen int

	switch {
	case n <= 0xf:
		buf[0] = byte(n) | fixMask
		blen = 1
	case n <= 0xffff:
		buf[0] = l16
		binary.BigEndian.PutUint16(buf[1:], uint16(n))
		blen = 3
	default:
		buf[0] = l32
		binary.BigEndian.PutUint32(buf[1:], uint32(n))
		blen = 5
	}

	e.w.write(buf[:blen])
}
