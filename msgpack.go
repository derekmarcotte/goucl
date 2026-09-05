package goucl

import (
	"errors"
	"fmt"
)

// This file and its two companions port src/ucl_msgpack.c: the MessagePack
// decoder (msgpack_decode.go, ucl_parse_msgpack and friends) and the
// msgpack-specific emitter primitives (msgpack_encode.go, the
// ucl_emitter_print_*_msgpack family plus ucl_emit_msgpack_elt from
// src/ucl_emitter.c).
//
// The wire format is big-endian throughout. The C swaps bytes by hand with
// TO_BE16/32/64 and FROM_BE16/32/64 because it memcpy's native-order structs;
// here encoding/binary.BigEndian does the same work directly.
//
// See docs/reference/msgpack.md for the byte-level format table and the behaviours this
// port reproduces deliberately.

// ErrMsgPack is wrapped by every error the msgpack decoder produces, so a
// caller can tell a malformed document from an I/O failure. The wrapped text
// is the message ucl_create_err would have stored in parser->err.
var ErrMsgPack = errors.New("goucl: msgpack")

func msgpackErrf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrMsgPack, fmt.Sprintf(format, args...))
}

// msgpackMaxNesting is UCL_MAX_NESTING from src/ucl_internal.h: the deepest
// container stack ucl_msgpack_get_container will build before erroring out.
const msgpackMaxNesting = 1024

// msgpackFormat is enum ucl_msgpack_format. The numeric values matter only
// because the C's error messages print them ("bad type for key: %u"), and this
// port keeps the same text; the declaration order is therefore the C's.
type msgpackFormat uint8

const (
	msgpackPositiveFixint msgpackFormat = iota
	msgpackFixmap
	msgpackFixarray
	msgpackFixstr
	msgpackNil
	msgpackFalse
	msgpackTrue
	msgpackBin8
	msgpackBin16
	msgpackBin32
	msgpackExt8
	msgpackExt16
	msgpackExt32
	msgpackFloat32
	msgpackFloat64
	msgpackUint8
	msgpackUint16
	msgpackUint32
	msgpackUint64
	msgpackInt8
	msgpackInt16
	msgpackInt32
	msgpackInt64
	msgpackFixext1
	msgpackFixext2
	msgpackFixext4
	msgpackFixext8
	msgpackFixext16
	msgpackStr8
	msgpackStr16
	msgpackStr32
	msgpackArray16
	msgpackArray32
	msgpackMap16
	msgpackMap32
	msgpackNegativeFixint
	msgpackInvalid
)

// Format flags, mirroring the MSGPACK_FLAG_* defines.
const (
	msgpackFlagFixed     = 1 << iota // length is embedded or fixed, not a separate field
	msgpackFlagContainer             // map or array: opens a new stack frame
	msgpackFlagTypeValue             // the type byte *is* the value (fixint, nil, bool)
	msgpackFlagExt                   // ext/fixext: unsupported, see msgpackParseIgnore
	msgpackFlagAssoc                 // container is a map rather than an array
	msgpackFlagKey                   // may appear as a map key (str and bin only)
)

// msgpackParserEntry is one row of the C's parsers[] search table.
//
// length carries three different meanings, selected by the flags, exactly as
// in the C:
//
//   - msgpackFlagFixed with length == 0: the length is embedded in the low
//     bits of the type byte (fixstr, fixmap, fixarray).
//   - msgpackFlagFixed with length != 0: length is the fixed payload width
//     (the scalar types), or for fixext the ext data width.
//   - without msgpackFlagFixed: length is the width in bytes of the
//     big-endian length field that follows the type byte.
type msgpackParserEntry struct {
	prefix    byte // prefix bit pattern
	prefixLen uint8
	format    msgpackFormat
	length    uint8
	flags     uint8
}

// msgpackParsers is the C's parsers[] table, in its original order. The C
// scans it linearly for every type byte; msgpackDispatch below precomputes the
// same answer, which is legitimate because every entry's bit pattern is
// disjoint from every other's.
var msgpackParsers = [...]msgpackParserEntry{
	{0xa0, 3, msgpackFixstr, 0, msgpackFlagFixed | msgpackFlagKey},
	{0x00, 1, msgpackPositiveFixint, 0, msgpackFlagFixed | msgpackFlagTypeValue},
	{0xe0, 3, msgpackNegativeFixint, 0, msgpackFlagFixed | msgpackFlagTypeValue},
	{0x80, 4, msgpackFixmap, 0, msgpackFlagFixed | msgpackFlagContainer | msgpackFlagAssoc},
	{0x90, 4, msgpackFixarray, 0, msgpackFlagFixed | msgpackFlagContainer},
	{0xd9, 8, msgpackStr8, 1, msgpackFlagKey},
	{0xc4, 8, msgpackBin8, 1, msgpackFlagKey},
	{0xcf, 8, msgpackUint64, 8, msgpackFlagFixed},
	{0xd3, 8, msgpackInt64, 8, msgpackFlagFixed},
	{0xce, 8, msgpackUint32, 4, msgpackFlagFixed},
	{0xd2, 8, msgpackInt32, 4, msgpackFlagFixed},
	{0xcb, 8, msgpackFloat64, 8, msgpackFlagFixed},
	{0xca, 8, msgpackFloat32, 4, msgpackFlagFixed},
	{0xc2, 8, msgpackFalse, 1, msgpackFlagFixed | msgpackFlagTypeValue},
	{0xc3, 8, msgpackTrue, 1, msgpackFlagFixed | msgpackFlagTypeValue},
	{0xcc, 8, msgpackUint8, 1, msgpackFlagFixed},
	{0xcd, 8, msgpackUint16, 2, msgpackFlagFixed},
	{0xd0, 8, msgpackInt8, 1, msgpackFlagFixed},
	{0xd1, 8, msgpackInt16, 2, msgpackFlagFixed},
	// nil's length really is 0 in the C table, not 1: with msgpackFlagFixed
	// that selects the embedded-length branch, which computes 0xc0 &^ 0xc0 ==
	// 0, and msgpackFlagTypeValue then zeroes the length anyway.
	{0xc0, 8, msgpackNil, 0, msgpackFlagFixed | msgpackFlagTypeValue},
	{0xda, 8, msgpackStr16, 2, msgpackFlagKey},
	{0xdb, 8, msgpackStr32, 4, msgpackFlagKey},
	{0xc5, 8, msgpackBin16, 2, msgpackFlagKey},
	{0xc6, 8, msgpackBin32, 4, msgpackFlagKey},
	{0xdc, 8, msgpackArray16, 2, msgpackFlagContainer},
	{0xdd, 8, msgpackArray32, 4, msgpackFlagContainer},
	{0xde, 8, msgpackMap16, 2, msgpackFlagContainer | msgpackFlagAssoc},
	{0xdf, 8, msgpackMap32, 4, msgpackFlagContainer | msgpackFlagAssoc},
	{0xc7, 8, msgpackExt8, 1, msgpackFlagExt},
	{0xc8, 8, msgpackExt16, 2, msgpackFlagExt},
	{0xc9, 8, msgpackExt32, 4, msgpackFlagExt},
	{0xd4, 8, msgpackFixext1, 1, msgpackFlagFixed | msgpackFlagExt},
	{0xd5, 8, msgpackFixext2, 2, msgpackFlagFixed | msgpackFlagExt},
	{0xd6, 8, msgpackFixext4, 4, msgpackFlagFixed | msgpackFlagExt},
	{0xd7, 8, msgpackFixext8, 8, msgpackFlagFixed | msgpackFlagExt},
	{0xd8, 8, msgpackFixext16, 16, msgpackFlagFixed | msgpackFlagExt},
}

// msgpackDispatch maps a type byte to its table row, or nil when the byte is
// not in the table at all. Only 0xc1 -- msgpack's permanently reserved byte --
// is unmapped, and it is what makes ucl_msgpack_get_parser_from_type return
// NULL ("unknown msgpack format").
var msgpackDispatch [256]*msgpackParserEntry

func init() {
	// This is ucl_msgpack_get_parser_from_type, evaluated once per byte value
	// instead of once per input byte: an entry matches t when the top
	// prefixLen bits of t equal those of prefix.
	for t := 0; t < 256; t++ {
		for i := range msgpackParsers {
			e := &msgpackParsers[i]
			shift := 8 - e.prefixLen
			if uint(e.prefix)>>shift == uint(t)>>shift {
				msgpackDispatch[t] = e
				break
			}
		}
	}
}
