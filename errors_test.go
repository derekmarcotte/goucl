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

// An .include moves the parser into another file. An error raised there must
// name that file and quote that file: pointing a caret at the same line number
// in the file the caller opened is worse than showing nothing, because it
// looks authoritative and is not.
func TestIncludeErrorNamesTheIncludedFile(t *testing.T) {
	dir := t.TempDir()
	inc := filepath.Join(dir, "bad.conf")
	if err := os.WriteFile(inc, []byte("x = 1;\ny = ;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The outer file's line 2 is a comment, so a wrong excerpt is obvious.
	outer := filepath.Join(dir, "main.conf")
	body := "# comment line one\n# another line two\n.include \"" + inc + "\"\n"
	if err := os.WriteFile(outer, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := ParseFile(outer)
	if err == nil {
		t.Fatal("expected a parse error")
	}
	var se *SyntaxError
	if !errors.As(err, &se) {
		t.Fatalf("errors.As failed on %T", err)
	}

	if se.File != inc {
		t.Errorf("File = %q, want the included file %q", se.File, inc)
	}
	if se.Line != 2 {
		t.Errorf("Line = %d, want 2", se.Line)
	}
	excerpt := se.Excerpt()
	if !strings.Contains(excerpt, "y = ;") {
		t.Errorf("Excerpt quotes the wrong file:\n%s", excerpt)
	}
	if strings.Contains(excerpt, "another line two") {
		t.Errorf("Excerpt quotes the outer file:\n%s", excerpt)
	}
}

// If the included file cannot be re-read, the excerpt is dropped rather than
// filled in from the wrong source.
func TestIncludeErrorWithoutReadableSourceHasNoExcerpt(t *testing.T) {
	dir := t.TempDir()
	inc := filepath.Join(dir, "vanishing.conf")
	if err := os.WriteFile(inc, []byte("y = ;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	outer := filepath.Join(dir, "main.conf")
	body := "# a comment\n# another comment\n.include \"" + inc + "\"\n"
	if err := os.WriteFile(outer, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ParseFile(outer)
	var se *SyntaxError
	if !errors.As(err, &se) {
		t.Fatalf("errors.As failed")
	}
	// Simulate the file becoming unreadable by clearing the retained source
	// the way newSyntaxError does when the re-read fails.
	se.src = nil
	if got := se.Excerpt(); got != "" {
		t.Errorf("Excerpt = %q, want empty when the source is unavailable", got)
	}
}

// A failure the engine does not classify still must not report ErrOK, which
// would read as success to any caller switching on the code.
func TestMissingFileIsClassifiedAsIO(t *testing.T) {
	_, err := ParseFile(filepath.Join(t.TempDir(), "nope.conf"))
	if err == nil {
		t.Fatal("expected an error")
	}
	var se *SyntaxError
	if !errors.As(err, &se) {
		t.Fatalf("errors.As failed on %T", err)
	}
	if se.Code == ErrOK {
		t.Error("Code is ErrOK on a real error")
	}
	if se.Code != ErrIO {
		t.Errorf("Code = %v, want io", se.Code)
	}
}

// Encoding an invalid node is a caller mistake, not a parse failure, so it
// must not arrive as a *SyntaxError promising a position it does not have.
func TestEncodeInvalidNodeIsNotASyntaxError(t *testing.T) {
	var n *Node
	_, err := n.Bytes(FormatJSON)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errors.Is(err, ErrInvalidNode) {
		t.Errorf("err = %v, want ErrInvalidNode", err)
	}
	var se *SyntaxError
	if errors.As(err, &se) {
		t.Error("an encode error should not be a *SyntaxError")
	}
}

// --- the encoder and decoder error types ----------------------------------
//
// These assert the rendered text, not just that an error came back. A message
// nothing reads is a message nothing catches when it breaks, and these are
// the strings a caller sees when a program hands them a bad value.

func TestUnsupportedTypeErrorMessage(t *testing.T) {
	_, err := Marshal(struct{ C chan int }{})
	var ute *UnsupportedTypeError
	if !errors.As(err, &ute) {
		t.Fatalf("errors.As failed on %T", err)
	}
	if ute.Path != "C" {
		t.Errorf("Path = %q, want %q", ute.Path, "C")
	}
	want := "goucl: unsupported type chan int at C"
	if got := ute.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestCycleErrorMessage(t *testing.T) {
	type ring struct {
		Next *ring
	}
	r := &ring{}
	r.Next = r

	_, err := Marshal(r)
	var ce *CycleError
	if !errors.As(err, &ce) {
		t.Fatalf("errors.As failed on %T", err)
	}
	if got := ce.Error(); !strings.HasPrefix(got, "goucl: cycle at ") {
		t.Errorf("Error() = %q, want a cycle message", got)
	}
}

// A root failure has no path to name, so the message must not render a stray
// separator for one.
func TestDecodeErrorMessageAtRoot(t *testing.T) {
	e := &DecodeError{Message: "no"}
	if got, want := e.Error(), "goucl: no"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestDecodeErrorMessageWithPath(t *testing.T) {
	var got struct{ Port int }
	err := Unmarshal([]byte(`port = "http";`), &got)
	var de *DecodeError
	if !errors.As(err, &de) {
		t.Fatalf("errors.As failed on %T", err)
	}
	if de.Path != "port" {
		t.Errorf("Path = %q, want %q", de.Path, "port")
	}
	if msg := de.Error(); !strings.HasPrefix(msg, "goucl: port: ") {
		t.Errorf("Error() = %q, want it to name the path", msg)
	}
}

// Unwrap is what lets errors.Is reach a cause a TextUnmarshaler returned.
func TestDecodeErrorUnwrap(t *testing.T) {
	cause := errors.New("the cause")
	e := &DecodeError{Path: "a", Message: "wrapped", Err: cause}
	if !errors.Is(e, cause) {
		t.Error("errors.Is did not reach the cause")
	}
	if errors.Unwrap(e) != cause {
		t.Error("Unwrap did not return the cause")
	}
	if errors.Unwrap(&DecodeError{Message: "none"}) != nil {
		t.Error("Unwrap on a causeless error should be nil")
	}
}
