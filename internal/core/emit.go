package core

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
)

// This file ports src/ucl_emitter.c and the text half of
// src/ucl_emitter_utils.c: the CONFIG, JSON, compact JSON and YAML emitters.
// MessagePack emission (ucl_emit_msgpack_*) lives elsewhere.
//
// The C reifies five vtables, but for the four text formats every entry is a
// thin wrapper around one of two shared implementations that branch internally
// on ctx->id and a baked-in "compact" flag. That structure is kept here as
// methods on emitCtx rather than as literal function tables, per
// docs/reference/emitter.md section 8.

// EmitComments carries the comment text the parser saved for individual
// objects, mirroring the ucl_object_t hash that C passes as
// ucl_emitter_context.comments (keyed there by the raw bytes of the object
// pointer, which a Go map expresses directly).
//
// Each value is the head of a Next-linked chain of TypeString objects holding
// literal comment text. FlagInherited on the head selects placement: without
// it the chain is emitted immediately before the object, with it immediately
// after. Comments are honoured by the CONFIG format only, matching the C.
type EmitComments map[*Object]*Object

// ErrEmitFormat is returned when a non-text emitter is passed to one of the
// EmitText functions.
var ErrEmitFormat = errors.New("goucl: unsupported text emitter")

// EmitText renders obj in one of the four text formats, mirroring
// ucl_object_emit.
//
// As in the C, a nil obj produces no output and no error. format must be
// EmitJSON, EmitJSONCompact, EmitConfig or EmitYAML; EmitMsgPack is rejected
// here because it is not a text format.
func EmitText(obj *Object, format Emitter) ([]byte, error) {
	if obj == nil {
		return nil, nil
	}
	var buf bytes.Buffer
	if err := EmitTextFull(&buf, obj, format, nil); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// EmitTextString is EmitText returning a string, mirroring the shape of
// ucl_object_emit's NUL-terminated result.
func EmitTextString(obj *Object, format Emitter) (string, error) {
	b, err := EmitText(obj, format)
	return string(b), err
}

// EmitTextTo renders obj to w, mirroring ucl_object_emit_full with a nil
// comments argument.
func EmitTextTo(w io.Writer, obj *Object, format Emitter) error {
	return EmitTextFull(w, obj, format, nil)
}

// EmitTextFull renders obj to w, optionally interleaving parser-saved
// comments. It mirrors ucl_object_emit_full.
//
// The root object is emitted through the same path as every nested value:
// there is no special top-level handling beyond the identity comparison in
// emitCtx.wrapsTopObject, which suppresses the outermost braces for CONFIG and
// YAML. No trailing newline is appended beyond what the last entry's own
// terminator produces.
func EmitTextFull(w io.Writer, obj *Object, format Emitter, comments EmitComments) error {
	if obj == nil {
		return nil
	}
	if err := emitCheckFormat(format); err != nil {
		return err
	}

	bw := bufio.NewWriter(w)
	ctx := emitNewCtx(bw, obj, format, comments)
	ctx.writeElt(obj, true, false)
	if ctx.w.err != nil {
		return ctx.w.err
	}
	return bw.Flush()
}

// emitCheckFormat rejects emitters this file does not implement, standing in
// for ucl_emit_get_standard_context returning NULL.
func emitCheckFormat(format Emitter) error {
	switch format {
	case EmitJSON, EmitJSONCompact, EmitConfig, EmitYAML:
		return nil
	}
	return fmt.Errorf("%w: %d", ErrEmitFormat, int(format))
}

// emitCtx is ucl_emitter_context for the text formats. The C's separate
// per-format operation tables collapse into the format field plus the compact
// flag derived from it, exactly as the shared ucl_emitter_common_* functions
// use them.
type emitCtx struct {
	format   Emitter
	compact  bool
	w        *emitWriter
	indent   int
	top      *Object
	comments EmitComments
}

func emitNewCtx(w io.Writer, top *Object, format Emitter, comments EmitComments) *emitCtx {
	return &emitCtx{
		format:   format,
		compact:  format == EmitJSONCompact,
		w:        &emitWriter{w: w},
		top:      top,
		comments: comments,
	}
}

// wrapsTopObject is UCL_EMIT_IDENT_TOP_OBJ: does this object print its own
// brace pair? Yes, unless it is the root and the format is CONFIG or YAML,
// both of which write the root object's members at indent 0 without a wrapper.
//
// Only objects consult this. Arrays are always bracketed, even at the root.
func (c *emitCtx) wrapsTopObject(obj *Object) bool {
	return c.top != obj || c.format == EmitJSONCompact || c.format == EmitJSON
}

// addTabs is ucl_add_tabs, which despite the name emits spaces: four per
// nesting level, and nothing at all in compact mode.
func (c *emitCtx) addTabs() {
	if !c.compact && c.indent > 0 {
		c.w.writeSpaces(c.indent * 4)
	}
}

// leadingSeparator writes the separator that precedes a non-first sibling.
// Separators are leading, never trailing, so the last sibling of a container
// emits nothing here. CONFIG never reaches this code: it terminates each entry
// instead, in finishObject.
func (c *emitCtx) leadingSeparator() {
	switch {
	case c.compact:
		c.w.writeByte(',')
	case c.format == EmitYAML && c.indent == 0:
		// Top-level YAML pairs are separated by a bare newline, no comma.
		c.w.writeByte('\n')
	default:
		c.w.writeString(",\n")
	}
}

// printKey is ucl_emitter_print_key.
func (c *emitCtx) printKey(printKey bool, obj *Object) {
	if !printKey {
		return
	}

	switch c.format {
	case EmitConfig:
		// A bare key unless the parser marked it as needing quotes.
		if obj.flags&FlagNeedKeyEscape != 0 {
			c.writeJSONString(obj.key)
		} else {
			c.w.write(obj.key)
		}
		if obj.ObjType != TypeObject && obj.ObjType != TypeArray {
			c.w.writeString(" = ")
		} else {
			// Containers take a single space before their brace, not " = ".
			c.w.writeByte(' ')
		}

	case EmitYAML:
		switch {
		case len(obj.key) > 0 && obj.flags&FlagNeedKeyEscape != 0:
			c.writeJSONString(obj.key)
		case len(obj.key) > 0:
			c.w.write(obj.key)
		default:
			c.w.writeString("null")
		}
		c.w.writeString(": ")

	default: // EmitJSON, EmitJSONCompact
		// Strict JSON always quotes keys, so FlagNeedKeyEscape is irrelevant.
		if len(obj.key) > 0 {
			c.writeJSONString(obj.key)
		} else {
			c.w.writeString("null")
		}
		if c.compact {
			c.w.writeByte(':')
		} else {
			c.w.writeString(": ")
		}
	}
}

// finishObject is ucl_emitter_finish_object: the per-entry trailing terminator
// that CONFIG uses in place of leading separators. The root is exempt, so
// nothing is appended after the document's last entry.
//
// isArray reports whether the value was written as a bare array element rather
// than as a keyed object member.
func (c *emitCtx) finishObject(obj *Object, isArray bool) {
	if c.format != EmitConfig || obj == c.top {
		return
	}
	switch {
	case obj.ObjType == TypeObject || obj.ObjType == TypeArray:
		c.w.writeByte('\n')
	case isArray:
		// Array elements are comma-terminated, the last one included.
		c.w.writeString(",\n")
	default:
		c.w.writeString(";\n")
	}
}

// writeElt is ucl_emitter_common_elt, the workhorse: it writes one value of
// any type, with its key and its surrounding punctuation.
func (c *emitCtx) writeElt(obj *Object, first, printKey bool) {
	if c.format != EmitConfig && !first {
		c.leadingSeparator()
	}
	c.addTabs()

	// A comment left set here is an "after" comment, emitted below the value.
	var comment *Object
	if c.comments != nil && c.format == EmitConfig {
		comment = c.comments[obj]
		if comment != nil && comment.flags&FlagInherited == 0 {
			// A "before" comment. Every node is followed by indentation,
			// including the last, because the value's own key comes next.
			for cur := comment; cur != nil; cur = cur.Next {
				c.w.write(cur.sv)
				c.w.writeByte('\n')
				c.addTabs()
			}
			comment = nil
		}
	}

	switch obj.ObjType {
	case TypeInt:
		c.printKey(printKey, obj)
		c.w.writeInt(obj.iv)
		c.finishObject(obj, !printKey)

	case TypeFloat, TypeTime:
		// UCL_TIME has no duration rendering at emit time; it is a plain
		// double, formatted exactly like UCL_FLOAT.
		c.printKey(printKey, obj)
		c.w.writeString(emitDouble(obj.dv))
		c.finishObject(obj, !printKey)

	case TypeBoolean:
		c.printKey(printKey, obj)
		if obj.iv != 0 {
			c.w.writeString("true")
		} else {
			c.w.writeString("false")
		}
		c.finishObject(obj, !printKey)

	case TypeString:
		c.printKey(printKey, obj)
		c.writeStringValue(obj)
		c.finishObject(obj, !printKey)

	case TypeNull:
		c.printKey(printKey, obj)
		c.w.writeString("null")
		c.finishObject(obj, !printKey)

	case TypeObject:
		c.startObject(obj, true, printKey)
		c.endObject(obj)

	case TypeArray:
		c.startArray(obj, true, printKey)
		c.endArray(obj)

	case TypeUserdata:
		c.printKey(printKey, obj)
		out := ""
		if obj.udEmitter != nil {
			out = obj.udEmitter(obj.ud)
		}
		// Always JSON-quoted, whatever the format.
		c.writeJSONString([]byte(out))
		c.finishObject(obj, !printKey)
	}

	if comment != nil {
		// An "after" comment. Indentation goes only *between* nodes: whatever
		// follows indents itself.
		for cur := comment; cur != nil; cur = cur.Next {
			c.w.write(cur.sv)
			c.w.writeByte('\n')
			if cur.Next != nil {
				c.addTabs()
			}
		}
	}
}

// writeStringValue picks between the three string writers, mirroring the
// UCL_STRING case of ucl_emitter_common_elt. Multiline wins over single
// quoting when both would apply, and only CONFIG has the choice at all.
func (c *emitCtx) writeStringValue(obj *Object) {
	if c.format != EmitConfig {
		c.writeJSONString(obj.sv)
		return
	}
	switch {
	case emitMaybeLongString(obj):
		c.writeMultilineString(obj.sv)
	case obj.flags&FlagSquoted != 0:
		c.writeSquotedString(obj.sv)
	default:
		c.writeJSONString(obj.sv)
	}
}

// startObject is ucl_emitter_common_start_object: it opens the brace and walks
// the members.
func (c *emitCtx) startObject(obj *Object, first, printKey bool) {
	if c.format != EmitConfig && !first {
		c.leadingSeparator()
		c.addTabs()
	}

	c.printKey(printKey, obj)

	if c.wrapsTopObject(obj) {
		if c.compact {
			c.w.writeByte('{')
		} else {
			c.w.writeString("{\n")
		}
		c.indent++
	}

	firstKey := true
	obj.ov.Iterate(func(cur *Object) bool {
		switch {
		case c.format == EmitConfig:
			// Duplicate keys become repeated "key = value;" entries, each
			// self-terminating, with no separator between them.
			for elt := cur; elt != nil; elt = elt.Next {
				c.writeElt(elt, firstKey, true)
			}

		case cur.Next != nil:
			// Everywhere else a duplicate-key chain collapses into one
			// synthetic array under a single key. Note that this separator is
			// always ",\n": the YAML top-level bare-newline case in
			// leadingSeparator is deliberately not applied here, matching the
			// C.
			if !firstKey {
				if c.compact {
					c.w.writeByte(',')
				} else {
					c.w.writeString(",\n")
				}
			}
			c.addTabs()
			c.startArray(cur, true, true)
			c.endArray(cur)

		default:
			c.writeElt(cur, firstKey, true)
		}
		firstKey = false
		return true
	})
}

// endObject is ucl_emitter_common_end_object.
func (c *emitCtx) endObject(obj *Object) {
	if c.wrapsTopObject(obj) {
		c.indent--
		if c.compact {
			c.w.writeByte('}')
		} else {
			if c.format != EmitConfig {
				// CONFIG's last member already ended its own line. For an
				// empty container nothing did, which is why only the other
				// formats show a blank line inside "{}".
				c.w.writeByte('\n')
			}
			c.addTabs()
			c.w.writeByte('}')
		}
	}
	c.finishObject(obj, false)
}

// startArray is ucl_emitter_common_start_array. Arrays are always bracketed,
// even at the document root, so there is no wrapsTopObject check here.
//
// obj is either a real TypeArray, whose elements are walked, or one link of a
// duplicate-key chain, in which case the chain itself is the array.
func (c *emitCtx) startArray(obj *Object, first, printKey bool) {
	if c.format != EmitConfig && !first {
		c.leadingSeparator()
		c.addTabs()
	}

	c.printKey(printKey, obj)

	if c.compact {
		c.w.writeByte('[')
	} else {
		c.w.writeString("[\n")
	}
	c.indent++

	firstKey := true
	if obj.ObjType == TypeArray {
		// Explicit array. Nil holes are skipped without counting as elements,
		// matching ucl_object_iterate.
		for _, elt := range obj.av {
			if elt == nil {
				continue
			}
			c.writeElt(elt, firstKey, false)
			firstKey = false
		}
	} else {
		// Implicit array: the shared key was already printed above, so every
		// link is written without one.
		for cur := obj; cur != nil; cur = cur.Next {
			c.writeElt(cur, firstKey, false)
			firstKey = false
		}
	}
}

// endArray is ucl_emitter_common_end_array.
func (c *emitCtx) endArray(obj *Object) {
	c.indent--
	if c.compact {
		c.w.writeByte(']')
	} else {
		if c.format != EmitConfig {
			c.w.writeByte('\n')
		}
		c.addTabs()
		c.w.writeByte(']')
	}
	c.finishObject(obj, true)
}

// --- string writers (src/ucl_emitter_utils.c) ------------------------------

// emitReplacementEscape is the fixed text libucl emits for any byte that is
// flagged unsafe but has no named escape: the JSON escape for U+FFFD, not a
// computed escape of the offending byte. The original value is lost.
const emitReplacementEscape = `\uFFFD`

// emitJSONEscapes maps a byte to its replacement text, or "" when the byte is
// copied verbatim.
//
// This is ucl_chartable.h's UCL_CHARACTER_JSON_UNSAFE | UCL_CHARACTER_DENIED |
// UCL_CHARACTER_WHITESPACE_UNSAFE classification, flattened: the flagged bytes
// are exactly 0x00-0x20, '"', '\\' and 0x7F (confirmed by evaluating
// ucl_test_character over all 256 bytes against the reference build). Space is
// flagged yet written through unchanged, and 0x0B has no named escape in JSON
// so it gets a real six-character Unicode escape rather than the U+FFFD text.
//
// Note what is absent: 0x80-0xFF are unflagged and pass through raw. The
// emitter performs no UTF-8 validation or decoding at all, and never restores
// the \uXXXX escapes the parser decoded.
var emitJSONEscapes = func() (t [256]string) {
	for b := 0x00; b <= 0x20; b++ {
		t[b] = emitReplacementEscape
	}
	t[0x7f] = emitReplacementEscape
	t['\b'] = `\b`
	t['\t'] = `\t`
	t['\n'] = `\n`
	t['\v'] = `\u000B`
	t['\f'] = `\f`
	t['\r'] = `\r`
	t[' '] = " "
	t['"'] = `\"`
	t['\\'] = `\\`
	return t
}()

// writeJSONString is ucl_elt_string_write_json: the default writer, and the
// only one JSON, compact JSON and YAML ever use, for both keys and values.
func (c *emitCtx) writeJSONString(s []byte) {
	c.w.writeByte('"')
	start := 0
	for i, b := range s {
		esc := emitJSONEscapes[b]
		if esc == "" {
			continue
		}
		if i > start {
			c.w.write(s[start:i])
		}
		c.w.writeString(esc)
		start = i + 1
	}
	if start < len(s) {
		c.w.write(s[start:])
	}
	c.w.writeByte('"')
}

// writeSquotedString is ucl_elt_string_write_squoted, used for CONFIG values
// that were single-quoted in the source. It escapes the single quote and
// nothing else: no backslash, no control characters, no high bytes.
//
// A string already containing the two bytes \' cannot round-trip through
// single quotes, so the whole value falls back to JSON quoting.
func (c *emitCtx) writeSquotedString(s []byte) {
	for i := 0; i+1 < len(s); i++ {
		if s[i] == '\\' && s[i+1] == '\'' {
			c.writeJSONString(s)
			return
		}
	}

	c.w.writeByte('\'')
	start := 0
	for i, b := range s {
		if b != '\'' {
			continue
		}
		if i > start {
			c.w.write(s[start:i])
		}
		c.w.writeString(`\'`)
		start = i + 1
	}
	if start < len(s) {
		c.w.write(s[start:])
	}
	c.w.writeByte('\'')
}

// emitMaybeLongString is ucl_maybe_long_string: a CONFIG string is written as
// a heredoc only if it contains a newline and is either over 80 bytes or was
// itself parsed from a heredoc.
func emitMaybeLongString(obj *Object) bool {
	const longStringLimit = 80
	if len(obj.sv) > longStringLimit || obj.flags&FlagMultiline != 0 {
		return bytes.IndexByte(obj.sv, '\n') >= 0
	}
	return false
}

// writeMultilineString is ucl_elt_string_write_multiline: <<EOD, the bytes
// verbatim, then EOD, with no terminator of its own -- the CONFIG entry
// terminator follows it.
//
// A value containing "\nEOD\n", or ending in "\nEOD", would close the heredoc
// early on re-parse, so it falls back to JSON quoting.
func (c *emitCtx) writeMultilineString(s []byte) {
	if len(s) >= 4 {
		// A candidate newline can start no later than len(s)-4.
		limit := len(s) - 4
		for i := 0; i <= limit; {
			j := bytes.IndexByte(s[i:limit+1], '\n')
			if j < 0 {
				break
			}
			nl := i + j
			if string(s[nl+1:nl+4]) == "EOD" && (nl+4 >= len(s) || s[nl+4] == '\n') {
				c.writeJSONString(s)
				return
			}
			i = nl + 1
		}
	}

	c.w.writeString("<<EOD\n")
	c.w.write(s)
	c.w.writeString("\nEOD")
}

// --- number formatting -----------------------------------------------------

// emitDouble renders a float64 the way libucl's three sink implementations all
// do, byte for byte.
//
// The C is:
//
//	if (val == (double)(int) val)                   printf("%.1lf", val);
//	else if (fabs(val - (double)(int) val) < delta) printf("%.*lg", DBL_DIG, val);
//	else                                            printf("%lf", val);
//
// with delta = 1e-7 and DBL_DIG = 15. The cast is to a 32-bit int, so an
// integral double outside int32 range misses the "%.1lf" branch entirely and
// prints with six decimals instead: 1.0 emits as "1.0" but 4294967296.0 emits
// as "4294967296.000000". Casting an out-of-range double is undefined
// behaviour in C; every observed case fell through to the final branch, which
// is what is reproduced here. See docs/findings.md #5.
//
// The closeness test truncates toward zero rather than rounding, so
// 0.99999999996 does not count as close to 1 -- (int) of it is 0.
//
// Infinity and NaN are spelled as glibc's printf spells them, not as Go does.
func emitDouble(val float64) string {
	const delta = 0.0000001
	const int32Min, int32Max = -2147483648.0, 2147483647.0

	switch {
	case math.IsNaN(val):
		return "nan"
	case math.IsInf(val, 1):
		return "inf"
	case math.IsInf(val, -1):
		return "-inf"
	}

	inRange := val >= int32Min && val <= int32Max
	// NaN keeps both comparisons below false where the C's cast would be UB.
	trunc := math.NaN()
	if inRange {
		trunc = math.Trunc(val)
	}

	switch {
	case inRange && val == trunc:
		return strconv.FormatFloat(val, 'f', 1, 64) // %.1lf
	case math.Abs(val-trunc) < delta:
		return strconv.FormatFloat(val, 'g', 15, 64) // %.*lg with DBL_DIG == 15
	default:
		return strconv.FormatFloat(val, 'f', 6, 64) // %lf
	}
}

// --- sink ------------------------------------------------------------------

// emitWriter is the ucl_emitter_functions sink. Go's io.Writer already unifies
// the memory, FILE* and fd variants the C keeps apart, so only the error
// latching is worth reproducing: the C ignores write failures entirely, and
// keeping the first one here keeps the tree walk free of error plumbing.
type emitWriter struct {
	w   io.Writer
	err error
	num []byte // scratch for integer formatting
}

func (w *emitWriter) write(b []byte) {
	if w.err != nil || len(b) == 0 {
		return
	}
	_, w.err = w.w.Write(b)
}

func (w *emitWriter) writeString(s string) {
	if w.err != nil || s == "" {
		return
	}
	_, w.err = io.WriteString(w.w, s)
}

func (w *emitWriter) writeByte(c byte) {
	if w.err != nil {
		return
	}
	if bw, ok := w.w.(io.ByteWriter); ok {
		w.err = bw.WriteByte(c)
		return
	}
	_, w.err = w.w.Write([]byte{c})
}

// emitSpaceChunk bounds how much indentation one write can emit.
const emitSpaceChunk = "                                "

func (w *emitWriter) writeSpaces(n int) {
	for n > len(emitSpaceChunk) {
		w.writeString(emitSpaceChunk)
		n -= len(emitSpaceChunk)
	}
	w.writeString(emitSpaceChunk[:n])
}

// writeInt is ucl_utstring_append_int's "%jd": plain base 10, no separators,
// no leading zeros.
func (w *emitWriter) writeInt(v int64) {
	w.num = strconv.AppendInt(w.num[:0], v, 10)
	w.write(w.num)
}
