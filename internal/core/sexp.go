package core

import (
	"errors"
	"fmt"
	"math"
)

// This file ports src/ucl_sexp.c: the canonical S-expression (csexp) decoder,
// ucl_parse_csexp. See docs/reference/sexp.md for the grammar and the full case-by-case
// mapping to error text.
//
// ucl_parse_csexp is written as an explicit state machine over one flat byte
// cursor with a linked-list stack of open list frames (struct ucl_stack,
// shared with the msgpack decoder), because that shape suits a resumable,
// chunk-at-a-time C parser. A Go decode of a single, complete []byte has no
// such resumption requirement, so this port uses ordinary recursive descent
// instead: sexpDecoder.parseList corresponds to one push/pop cycle of the
// read_obrace/read_length/read_value/read_ebrace states, and nesting is
// bounded by an explicit depth parameter (mirroring UCL_MAX_NESTING) rather
// than left to the Go call stack -- which is what keeps a pathologically
// deep run of "(" characters from recursing without limit.
//
// Every failure the C's state machine distinguishes (bad start byte, bad
// length digit, zero length, oversized length, truncated atom, value outside
// any list, extra closing brace, trailing garbage, unclosed list, nesting too
// deep) has a corresponding error return here; see the table in docs/reference/sexp.md
// section 5. As that section notes, message text need not match verbatim,
// only the success/failure outcome and, on success, the resulting object
// tree -- several of the C's distinct error paths collapse onto the same Go
// check (e.g. "value outside of any list" and "extra closing brace" both fall
// out of this port's single trailing-data check after the top-level list),
// which changes no observable behavior since no test inspects error text.

// ErrCSexp is wrapped by every error the csexp decoder produces, mirroring
// ErrMsgPack's role for the MessagePack decoder: it lets a caller tell a
// malformed csexp document from any other kind of error.
var ErrCSexp = errors.New("goucl: csexp")

func sexpErrf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrCSexp, fmt.Sprintf(format, args...))
}

// sexpMaxNesting is UCL_MAX_NESTING from src/ucl_internal.h, the same cap the
// msgpack decoder uses (msgpackMaxNesting): the deepest list nesting
// ucl_parse_csexp accepts before failing with "csexp nesting too deep".
const sexpMaxNesting = 1024

// DecodeCSexp decodes a complete canonical S-expression document into an
// Object tree, mirroring ucl_parser_add_chunk_full(..., UCL_DUPLICATE_APPEND,
// UCL_PARSE_CSEXP) followed by ucl_parser_get_object, for a parser holding no
// previous state.
//
// The grammar (docs/reference/sexp.md section 1) is Rivest's canonical S-expression
// restricted to libucl's subset:
//
//	sexp    := "(" element* ")"
//	element := sexp | atom
//	atom    := length ":" bytes   ; bytes is exactly `length` raw octets
//	length  := [0-9]+             ; decimal, no leading structure, > 0
//
// There is no whitespace, quoting, or bare-token/symbol form anywhere, and
// the whole input must be exactly one top-level list -- a bare atom or a
// bare ")" at top level is rejected, and so is anything trailing the
// outermost list's closing ")".
//
// Every "(" becomes a TypeArray object (including the empty list "()") and
// every atom becomes a TypeString object flagged FlagBinary, holding its
// bytes verbatim; there is no numeric/boolean type inference for csexp atoms
// the way the main UCL parser infers types for bare tokens (docs/reference/sexp.md
// section 4). Divergence from UCL_PARSER_ZEROCOPY: like DecodeMsgPack, this
// port always copies atom bytes into a freshly owned slice rather than
// aliasing the input, so there is no zero-copy variant to mirror.
func DecodeCSexp(data []byte) (*Object, error) {
	return DecodeCSexpFull(data, 0)
}

// DecodeCSexpFull is DecodeCSexp with an explicit priority, matching the
// priority argument of ucl_parser_add_chunk_full: every object the decode
// creates -- every list and every atom -- is given that priority.
func DecodeCSexpFull(data []byte, priority uint) (*Object, error) {
	if len(data) == 0 {
		// Mirrors ucl_parser_add_chunk_full's len == 0 special case (also
		// reproduced by DecodeMsgPackFull): a zero-length chunk never
		// reaches the format-specific parser at all -- ucl_parse_csexp
		// asserts a non-empty chunk -- so an empty top-level object is
		// synthesized instead of applying csexp's "exactly one top-level
		// list" rule.
		return NewFull(TypeObject, priority), nil
	}

	// start_parse: the very first byte of the chunk must be '('.
	if data[0] != '(' {
		return nil, sexpErrf("bad starting character for sexp block: %x", data[0])
	}

	d := &sexpDecoder{buf: data, priority: priority}
	top, err := d.parseList(0)
	if err != nil {
		return nil, err
	}
	if d.p != len(d.buf) {
		// NEXT_STATE's trailing-garbage enforcement: nothing may follow the
		// outermost list's closing ')'. This is also what rejects the
		// issue #365 regression inputs where a value or a fresh "("
		// follows the already-closed outermost list (test_err_value_after_
		// close, test_err_open_after_close): whatever the trailing byte is,
		// it is caught here.
		return nil, sexpErrf("extra data")
	}
	return top, nil
}

// sexpDecoder holds the byte cursor and priority that parseList and
// parseAtom share; it is the recursive-descent analogue of ucl_parse_csexp's
// locals (p, end) plus parser->stack's chain of open list frames -- one
// sexpDecoder.parseList call per stack frame.
type sexpDecoder struct {
	buf      []byte
	p        int
	priority uint
}

// parseList parses one "(" element* ")" form starting at d.buf[d.p], which
// must be '(', mirroring one push/allocate/pop cycle of ucl_parse_csexp's
// read_obrace/read_ebrace states plus everything dispatched in between.
// depth is the nesting level of the '(' this call is about to consume (0 for
// the outermost list); it is checked against sexpMaxNesting exactly where
// read_obrace checks it, before the frame is allocated, and is what stops a
// long run of unmatched "(" bytes from recursing without bound.
func (d *sexpDecoder) parseList(depth int) (*Object, error) {
	if depth >= sexpMaxNesting {
		return nil, sexpErrf("csexp nesting too deep (over %d)", sexpMaxNesting)
	}
	d.p++ // consume '('

	arr := NewFull(TypeArray, d.priority)

	for {
		if d.p >= len(d.buf) {
			// The C's end-of-function check ("invalid finishing state"):
			// input ran out before this list's matching ')'.
			return nil, sexpErrf("invalid finishing state: unclosed list")
		}

		switch d.buf[d.p] {
		case ')':
			// read_ebrace: pop this frame and append it into the parent
			// (done by the caller, via ArrayAppend on the returned object).
			d.p++
			return arr, nil

		case '(':
			// read_obrace for a nested list.
			child, err := d.parseList(depth + 1)
			if err != nil {
				return nil, err
			}
			ArrayAppend(arr, child)

		default:
			// Anything else starts an atom (read_length); parseAtom itself
			// rejects a non-digit first byte ("bad length character"),
			// which is how e.g. "(a:abc)" is caught.
			atom, err := d.parseAtom()
			if err != nil {
				return nil, err
			}
			ArrayAppend(arr, atom)
		}
	}
}

// parseAtom parses one length-prefixed atom "N:bytes" starting at d.buf[d.p],
// mirroring the read_length and read_value states together. It always
// returns a TypeString object flagged FlagBinary, per the csexp atom mapping
// (docs/reference/sexp.md section 4): csexp has no other atom shape, so there is
// nothing to type-infer.
func (d *sexpDecoder) parseAtom() (*Object, error) {
	var length uint64
	for {
		if d.p >= len(d.buf) {
			// Ran out of input while still reading the length field.
			return nil, sexpErrf("invalid finishing state: truncated length")
		}
		c := d.buf[d.p]
		if c == ':' {
			break
		}
		if c < '0' || c > '9' {
			return nil, sexpErrf("bad length character: %x", c)
		}
		// Accumulate decimal digits, exactly as read_length's
		// `len = len*10 + digit`. length is checked against the
		// UINT32_MAX soft cap after every digit, so it can never climb far
		// enough to risk overflowing the uint64 accumulator itself.
		length = length*10 + uint64(c-'0')
		if length > math.MaxUint32 {
			// "too big length of an element": a soft cap bounding memory
			// allocation, not the true (arbitrary-precision) csexp spec.
			return nil, sexpErrf("too big length of an element")
		}
		d.p++
	}
	if length == 0 {
		// Reached ':' with no digits consumed, or with an accumulated
		// length of exactly 0 -- both are "zero length element" in the C,
		// since read_length starts len at 0 and a bare ":" never advances
		// it.
		return nil, sexpErrf("zero length element")
	}
	d.p++ // consume ':'

	// read_value's bounds check: `(end - p) < len` (defensively re-checked
	// against len == 0, which cannot happen here since length > 0 already).
	// By construction, once this passes, length fits in an int: it is at
	// most len(d.buf)-d.p, and slice lengths are always int-representable.
	if uint64(len(d.buf)-d.p) < length {
		return nil, sexpErrf("invalid length: %d, %d remain", length, len(d.buf)-d.p)
	}

	n := int(length)
	val := d.buf[d.p : d.p+n]
	d.p += n

	// FromLString copies val into a freshly owned slice (see DecodeCSexp's
	// doc comment on the zerocopy divergence), so the returned object never
	// aliases the caller's input.
	obj := FromLString(val)
	obj.flags |= FlagBinary
	obj.SetPriority(d.priority)
	return obj, nil
}
