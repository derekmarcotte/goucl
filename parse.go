package goucl

import (
	"os"

	"github.com/derekmarcotte/goucl/internal/core"
)

// parserFlags is what every entry point here parses with.
//
// Comments are saved unconditionally. The engine only records them when asked,
// and a document that has lost its comments cannot get them back, so the cost
// of always asking is a little memory and the cost of not asking is that
// Node.Before, Node.After and a comment-preserving round trip silently return
// nothing. Nothing else about the parse changes.
const parserFlags = core.ParserSaveComments

// Parse reads a UCL document.
//
// Includes are resolved as libucl resolves them, which means a relative
// .include is resolved against the process working directory rather than
// against any directory belonging to the document. That is surprising, and it
// is deliberate: it is what a UCL document does under the C, so it is what a
// document does here. See docs/findings.md item 1.
//
// A failed parse returns a *SyntaxError carrying the position and an excerpt.
func Parse(data []byte) (*Node, error) {
	p := core.NewParser(parserFlags)
	p.AddChunk(data)
	return finish(p, data, "")
}

// ParseString reads a UCL document from a string.
func ParseString(s string) (*Node, error) {
	return Parse([]byte(s))
}

// ParseFile reads a UCL document from a file.
//
// The file is opened by the engine rather than read here, because opening it
// is what sets the document's file variables ($FILENAME and $CURDIR) and the
// base path an .include is reported against. Reading the bytes separately and
// parsing them as a chunk would quietly change what the document means.
func ParseFile(name string) (*Node, error) {
	p := core.NewParser(parserFlags)
	p.AddFile(name)
	// Read separately, for the error excerpt only. A failure here costs the
	// excerpt and nothing else, so it is deliberately ignored.
	src, _ := os.ReadFile(name)
	return finish(p, src, name)
}

// finish decides whether the parse succeeded.
//
// It deliberately ignores whether the engine's add returned true, and asks
// instead whether an error was recorded and a document produced. The two are
// not the same, and the C agrees: for testdata/basic/9.in,
// ucl_parser_add_file returns false while recording no error and producing a
// complete document. Verified against the compiled reference --
//
//	add_file=false error=(none) obj=yes errcode=0
//
// -- and the engine reproduces it exactly. libucl's own consumers, including
// its test harness, check the error rather than the return value, which is why
// that fixture emits its goldens perfectly well. Treating the boolean as
// authoritative would invent a failure the C does not report.
func finish(p *core.Parser, src []byte, file string) (*Node, error) {
	if p.GetError() != nil {
		return nil, newSyntaxError(p, src, file)
	}
	obj := p.GetObject()
	if obj == nil {
		// Not the empty-input case: an empty chunk still produces an empty
		// object, in the C and here. This is the defensive branch for a
		// parser that reports neither an error nor a document, which would
		// leave the caller with a nil tree and no explanation.
		return nil, &SyntaxError{
			File:    file,
			Code:    ErrInternal,
			Message: "goucl: parser reported no error but produced no document",
			src:     src,
		}
	}
	return &Node{obj: obj, comments: p.GetComments()}, nil
}
