package core

import "testing"

// The csexp decoder is exercised thoroughly in sexp_test.go. These tests cover
// only the wiring: that ParseCSexp reaches it through the parser's chunk
// dispatch, and that a decode failure surfaces as a parser error rather than
// being swallowed. Without this, DecodeCSexp could be perfectly correct while
// ucl_parser_add_chunk_full's csexp arm stayed a stub.
func TestParserDispatchesCSexp(t *testing.T) {
	p := NewParser(ParserDefault)
	if !p.AddChunkFull([]byte("(3:foo3:bar)"), 0, DuplicateAppend, ParseCSexp) {
		t.Fatalf("AddChunkFull(ParseCSexp) failed: %v", p.GetError())
	}
	obj := p.GetObject()
	if obj == nil {
		t.Fatal("GetObject returned nil")
	}
	if obj.Type() != TypeArray {
		t.Fatalf("top-level type = %v, want %v", obj.Type(), TypeArray)
	}
	if n := ArraySize(obj); n != 2 {
		t.Fatalf("array size = %d, want 2", n)
	}
	// csexp atoms are always flagged binary, and ToString rejects binary
	// strings by returning "" -- a C wart this port reproduces. ToLString is
	// the accessor that reads them.
	head := ArrayHead(obj)
	if head.Flags()&FlagBinary == 0 {
		t.Error("csexp atom is not flagged binary")
	}
	b, n := ToLString(head)
	if got := string(b[:n]); got != "foo" {
		t.Errorf("first atom = %q, want %q", got, "foo")
	}
}

// A malformed document must set the parser's error, not merely return false.
func TestParserCSexpErrorSurfaces(t *testing.T) {
	p := NewParser(ParserDefault)
	if p.AddChunkFull([]byte("(3:foo"), 0, DuplicateAppend, ParseCSexp) {
		t.Fatal("an unclosed list was accepted")
	}
	if p.GetError() == nil {
		t.Error("parser recorded no error for a malformed document")
	}
	if p.ErrorCode() != ErrSyntax {
		t.Errorf("error code = %v, want %v", p.ErrorCode(), ErrSyntax)
	}
}
