package goucl

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSyntaxErrorPosition(t *testing.T) {
	_, err := ParseString("a = 1;\nb = ;\n")
	if err == nil {
		t.Fatal("expected a parse error")
	}

	var se *SyntaxError
	if !errors.As(err, &se) {
		t.Fatalf("errors.As failed on %T", err)
	}
	if se.Line != 2 {
		t.Errorf("Line = %d, want 2", se.Line)
	}
	if se.Column != 4 {
		t.Errorf("Column = %d, want 4 (0-based, as the message says)", se.Column)
	}
	if se.Code != ErrSyntax {
		t.Errorf("Code = %v, want syntax", se.Code)
	}
	// The message is the engine's, which is the C's, byte for byte.
	if !strings.Contains(se.Message, "line: 2, column: 4") {
		t.Errorf("Message lost the position: %q", se.Message)
	}
	if se.Error() != se.Message {
		t.Error("Error() should return Message unchanged")
	}
}

func TestSyntaxErrorExcerpt(t *testing.T) {
	_, err := ParseString("a = 1;\nb = ;\n")
	var se *SyntaxError
	if !errors.As(err, &se) {
		t.Fatalf("errors.As failed")
	}

	want := "2 | b = ;\n  |     ^\n"
	if got := se.Excerpt(); got != want {
		t.Errorf("Excerpt =\n%q\nwant\n%q", got, want)
	}
}

// Tabs must be preserved in the caret line, or the caret drifts under any tab
// width but the one the author guessed.
func TestExcerptPreservesTabs(t *testing.T) {
	se := &SyntaxError{Line: 1, Column: 3, src: []byte("\t\t\tX")}
	want := "1 | \t\t\tX\n  | \t\t\t^\n"
	if got := se.Excerpt(); got != want {
		t.Errorf("Excerpt = %q, want %q", got, want)
	}
}

func TestExcerptOutOfRange(t *testing.T) {
	for _, se := range []*SyntaxError{
		{Line: 0, src: []byte("a")},
		{Line: 99, src: []byte("a")},
		{Line: 1, src: nil},
		nil,
	} {
		if got := se.Excerpt(); got != "" {
			t.Errorf("Excerpt = %q, want empty", got)
		}
	}
	// A column past the end of the line clamps rather than panicking.
	se := &SyntaxError{Line: 1, Column: 99, src: []byte("ab")}
	if got := se.Excerpt(); !strings.Contains(got, "^") {
		t.Errorf("Excerpt = %q, want a caret", got)
	}
}

func TestParseFileError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.conf")
	if err := writeFile(path, "key = {\n"); err != nil {
		t.Fatal(err)
	}
	_, err := ParseFile(path)
	if err == nil {
		t.Fatal("expected a parse error")
	}
	var se *SyntaxError
	if !errors.As(err, &se) {
		t.Fatalf("errors.As failed on %T", err)
	}
	if se.File == "" {
		t.Error("File should name the file")
	}
}

func TestParseFileMissing(t *testing.T) {
	_, err := ParseFile(filepath.Join(t.TempDir(), "nope.conf"))
	if err == nil {
		t.Fatal("expected an error for a missing file")
	}
	var se *SyntaxError
	if !errors.As(err, &se) {
		t.Fatalf("errors.As failed on %T", err)
	}
}

func TestEmptyInput(t *testing.T) {
	// An empty document is an empty object, not an error and not a nil tree.
	// Verified against the compiled C reference --
	//
	//	add_string=true error=(none) obj=yes type=object
	//
	// -- which is also what makes ".include()" with empty parens yield an
	// empty arguments object.
	doc, err := ParseString("")
	if err != nil {
		t.Fatalf("empty input: %v", err)
	}
	if got := doc.Kind(); got != KindObject {
		t.Errorf("Kind = %v, want object", got)
	}
	if got := doc.Len(); got != 0 {
		t.Errorf("Len = %d, want 0", got)
	}
}

func TestErrorCodeNames(t *testing.T) {
	for _, tc := range []struct {
		code ErrorCode
		want string
	}{
		{ErrOK, "ok"},
		{ErrSyntax, "syntax"},
		{ErrIO, "io"},
		{ErrMerge, "merge"},
		{ErrorCode(99), "unknown(99)"},
	} {
		if got := tc.code.String(); got != tc.want {
			t.Errorf("%d: String = %q, want %q", int(tc.code), got, tc.want)
		}
	}
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}
