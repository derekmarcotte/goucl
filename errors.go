package goucl

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"go.marcottesoftware.ca/goucl/internal/core"
)

// ErrInvalidNode is returned when an operation is attempted on a nil Node or
// the zero Node. It is not a *SyntaxError: nothing failed to parse, so there
// is no position to report and no source to quote.
var ErrInvalidNode = errors.New("goucl: operation on an invalid node")

// ErrorCode classifies a parse failure. The values mirror the engine's, which
// mirror the C's, so a code compares equal across all three layers.
type ErrorCode int

const (
	ErrOK       = ErrorCode(core.ErrOK)       // no error
	ErrSyntax   = ErrorCode(core.ErrSyntax)   // syntax error
	ErrIO       = ErrorCode(core.ErrIO)       // I/O error
	ErrState    = ErrorCode(core.ErrState)    // invalid parser state
	ErrNested   = ErrorCode(core.ErrNested)   // nesting limit exceeded
	ErrUnpaired = ErrorCode(core.ErrUnpaired) // unpaired brace
	ErrMacro    = ErrorCode(core.ErrMacro)    // macro error
	ErrInternal = ErrorCode(core.ErrInternal) // internal error
	ErrSSL      = ErrorCode(core.ErrSSL)      // signature/SSL error
	ErrMerge    = ErrorCode(core.ErrMerge)    // merge conflict
)

// String names the code, for diagnostics.
func (c ErrorCode) String() string {
	switch c {
	case ErrOK:
		return "ok"
	case ErrSyntax:
		return "syntax"
	case ErrIO:
		return "io"
	case ErrState:
		return "state"
	case ErrNested:
		return "nested"
	case ErrUnpaired:
		return "unpaired"
	case ErrMacro:
		return "macro"
	case ErrInternal:
		return "internal"
	case ErrSSL:
		return "ssl"
	case ErrMerge:
		return "merge"
	}
	return "unknown(" + strconv.Itoa(int(c)) + ")"
}

// SyntaxError reports a document that could not be parsed. Every parse failure
// returns one, so errors.As against it always succeeds on a Parse error:
//
//	var se *goucl.SyntaxError
//	if errors.As(err, &se) {
//		fmt.Println(se.Line, se.Column)
//		fmt.Print(se.Excerpt())
//	}
type SyntaxError struct {
	// File is the file the error is in, or "" when the input was not a file.
	//
	// It is not necessarily the file passed to ParseFile. An .include moves
	// the parser into another file, and an error raised there names that
	// file, not the one the caller opened. Line and Column refer to File,
	// and Excerpt quotes it.
	File string

	// Line is the 1-based line number.
	Line int

	// Column is a 0-based byte offset within Line.
	//
	// 0-based is what the engine reports and what Message already says, and
	// the two agreeing matters more than matching the 1-based convention of
	// Go's own tools. Excerpt places the caret correctly either way.
	Column int

	// Code classifies the failure.
	//
	// It is the engine's own classification, and the engine does not classify
	// everything: a failure raised while processing a macro -- an .include
	// naming a file that does not exist, a glob matching nothing, a document
	// including itself -- carries no code, and Code is then ErrOK on an error
	// that is real. Treat ErrOK as "unclassified" and read Message; do not
	// write a switch that assumes one of the other codes must apply.
	Code ErrorCode

	// Message is the engine's text, which is byte-identical to the C's. It
	// already names the file, line and column, so Error returns it unchanged
	// rather than prefixing a second, differently formatted position.
	Message string

	// src is the input, retained only so Excerpt can quote it.
	src []byte
}

// Error returns the engine's message verbatim.
func (e *SyntaxError) Error() string { return e.Message }

// Excerpt renders the offending line with a caret under the column:
//
//	2 | b = ;
//	  |     ^
//
// It returns "" when the source is unavailable or the position does not fall
// within it. Tabs in the source are preserved in the caret line so the caret
// stays aligned under any tab width.
func (e *SyntaxError) Excerpt() string {
	if e == nil || e.Line <= 0 || len(e.src) == 0 {
		return ""
	}
	lines := strings.Split(string(e.src), "\n")
	if e.Line > len(lines) {
		return ""
	}
	line := strings.TrimSuffix(lines[e.Line-1], "\r")

	col := e.Column
	if col < 0 {
		col = 0
	}
	if col > len(line) {
		col = len(line)
	}

	// Indent the caret with the source's own leading bytes, turning
	// everything but a tab into a space, so tabs align.
	pad := []byte(line[:col])
	for i, b := range pad {
		if b != '\t' {
			pad[i] = ' '
		}
	}

	num := strconv.Itoa(e.Line)
	gutter := strings.Repeat(" ", len(num))

	var b strings.Builder
	fmt.Fprintf(&b, "%s | %s\n", num, line)
	fmt.Fprintf(&b, "%s | %s^\n", gutter, pad)
	return b.String()
}

// newSyntaxError captures the parser's failure state. Line and column are read
// from the parser rather than parsed back out of the message text: the parser
// still holds the failing chunk, and the numbers it reports are the ones the
// message was built from.
//
// src is the caller's input, which is not always the input the position refers
// to: an .include moves the parser into another file, and the line and column
// then belong to that file. Quoting the caller's bytes at that position would
// put a caret under an unrelated line of the wrong file, which is worse than
// showing nothing, so the included file is re-read and the excerpt dropped if
// it cannot be.
func newSyntaxError(p *core.Parser, src []byte, file string) *SyntaxError {
	if cur := p.CurFile(); cur != "" {
		if cur != file {
			if b, err := os.ReadFile(cur); err == nil {
				src = b
			} else {
				src = nil
			}
		}
		file = cur
	}

	msg := p.ErrorString()
	if msg == "" {
		msg = "goucl: parse failed without a recorded error"
	}
	return &SyntaxError{
		File:    file,
		Line:    p.LineNum(),
		Column:  p.Column(),
		Code:    ErrorCode(p.ErrorCode()),
		Message: msg,
		src:     src,
	}
}
