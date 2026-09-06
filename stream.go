package goucl

import (
	"fmt"
	"io"
)

// Decoder reads a single UCL document from a reader and decodes it.
//
// UCL is a whole-document format, not a framed one: there is nothing in it
// marking where one value ends and the next begins the way JSON's decoder
// can tell by bracket-matching alone, so there is no document-per-read
// streaming to offer. Decode instead reads everything the underlying reader
// has on its first call, parses it once, and decodes it. A second Decode
// finds the reader already drained and reports io.EOF, matching the shape of
// json.Decoder's Decode even though the mechanism underneath is different.
type Decoder struct {
	r    io.Reader
	opts []Option

	done      bool
	undecoded []string
}

// NewDecoder returns a Decoder that reads from r.
func NewDecoder(r io.Reader, opts ...Option) *Decoder {
	return &Decoder{r: r, opts: opts}
}

// Decode reads the whole of the underlying reader, parses it as one UCL
// document, and decodes it into v. See the Decoder doc comment: only the
// first call does any work, and every call after it returns io.EOF.
//
// An error from the reader itself is returned as-is, not folded into a
// *SyntaxError -- it is not something the document said, it is something
// that stopped the document from being read at all.
func (d *Decoder) Decode(v any) error {
	if d.done {
		return io.EOF
	}
	d.done = true

	data, err := io.ReadAll(d.r)
	if err != nil {
		return fmt.Errorf("goucl: reading document: %w", err)
	}

	n, err := Parse(data, d.opts...)
	if err != nil {
		return err
	}

	undecoded, err := decodeInto(n, v, newParseConfig(d.opts).strict)
	d.undecoded = undecoded
	return err
}

// Undecoded returns the dotted paths, in the spelling Node.Get accepts, that
// the most recent Decode found in the document but not in v -- the same
// collection DisallowUnknownFields turns into an error instead. It reflects
// only the most recent call to Decode, and is nil before the first one.
func (d *Decoder) Undecoded() []string {
	return d.undecoded
}

// Encoder writes values to an underlying writer as UCL, in the format
// selected by WithFormat (FormatConfig by default).
type Encoder struct {
	w   io.Writer
	fmt Format
}

// NewEncoder returns an Encoder that writes to w.
func NewEncoder(w io.Writer, opts ...EncodeOption) *Encoder {
	cfg := newEncodeConfig(opts)
	return &Encoder{w: w, fmt: cfg.format}
}

// Encode writes v to the underlying writer.
func (e *Encoder) Encode(v any) error {
	n, err := toNode(v)
	if err != nil {
		return err
	}
	return n.Encode(e.w, e.fmt)
}
