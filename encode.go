package goucl

import (
	"bytes"
	"io"

	"github.com/derekmarcotte/goucl/internal/core"
)

// Format selects an output format.
type Format int

const (
	// FormatConfig is UCL's own configuration syntax. It is the only format
	// that emits comments, matching the C.
	FormatConfig Format = iota
	// FormatJSON is indented JSON.
	FormatJSON
	// FormatJSONCompact is JSON without insignificant whitespace.
	FormatJSONCompact
	// FormatYAML is YAML.
	FormatYAML
)

// String names the format.
func (f Format) String() string {
	switch f {
	case FormatConfig:
		return "config"
	case FormatJSON:
		return "json"
	case FormatJSONCompact:
		return "json-compact"
	case FormatYAML:
		return "yaml"
	}
	return "unknown"
}

// emitter maps to the engine's format. The two enumerations are ordered
// differently on purpose: the engine's values are a compatibility contract
// with the C, and this one is free to lead with the format people want.
func (f Format) emitter() (core.Emitter, bool) {
	switch f {
	case FormatConfig:
		return core.EmitConfig, true
	case FormatJSON:
		return core.EmitJSON, true
	case FormatJSONCompact:
		return core.EmitJSONCompact, true
	case FormatYAML:
		return core.EmitYAML, true
	}
	return 0, false
}

// ErrFormat is returned for a Format that is not one of the four constants.
var ErrFormat = core.ErrEmitFormat

// Encode writes the node in the given format.
//
// Comments survive only through FormatConfig, because that is the only format
// the C emits them in. Encoding a document parsed with comments as JSON drops
// them, exactly as the C does.
func (n *Node) Encode(w io.Writer, f Format) error {
	if n == nil || n.obj == nil {
		return ErrInvalidNode
	}
	em, ok := f.emitter()
	if !ok {
		return ErrFormat
	}
	return core.EmitTextFull(w, n.obj, em, n.comments)
}

// Bytes renders the node in the given format.
func (n *Node) Bytes(f Format) ([]byte, error) {
	var buf bytes.Buffer
	if err := n.Encode(&buf, f); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
