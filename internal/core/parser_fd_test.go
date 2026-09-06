package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// AddFd must read the whole file. It sizes a buffer from Stat and used to fill
// it with a single Read, which is not guaranteed to return everything: the
// remainder of the document was dropped without an error. The C mmaps the file
// and cannot truncate this way.
func TestAddFdReadsWholeFile(t *testing.T) {
	const keys = 200000

	var b strings.Builder
	for i := 0; i < keys; i++ {
		fmt.Fprintf(&b, "k%d = %d;\n", i, i)
	}
	path := filepath.Join(t.TempDir(), "big.conf")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	p := NewParser(0)
	if !p.AddFd(f.Fd()) {
		t.Fatalf("AddFd failed: %v", p.GetError())
	}
	obj := p.GetObject()
	if obj == nil {
		t.Fatal("AddFd produced no object")
	}

	// Every key must be present, and the last one is the one a truncated read
	// would lose first.
	if got := int((*CompatObject)(obj).Len()); got != keys {
		t.Errorf("parsed %d keys, want %d", got, keys)
	}
	last := obj.Lookup(fmt.Sprintf("k%d", keys-1))
	if last == nil {
		t.Fatalf("last key k%d is missing: the file was read short", keys-1)
	}
	if got := ToInt(last); got != keys-1 {
		t.Errorf("last key = %d, want %d", got, keys-1)
	}
}

// A pipe reports a size of zero, so AddFd succeeds without parsing anything.
// That is what the C does too -- ucl_parser_add_fd_full returns true on
// st_size == 0 before it reaches mmap -- so it is reproduced rather than
// fixed. This test exists to document it: it is surprising, and someone will
// otherwise "fix" it into a divergence.
func TestAddFdOnPipeMatchesC(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	go func() {
		w.WriteString("a = 1;\n")
		w.Close()
	}()

	p := NewParser(0)
	if !p.AddFd(r.Fd()) {
		t.Fatalf("AddFd on a pipe reported failure; the C returns true: %v", p.GetError())
	}
	if err := p.GetError(); err != nil {
		t.Errorf("AddFd on a pipe set an error; the C sets none: %v", err)
	}
	if obj := p.GetObject(); obj != nil {
		t.Errorf("AddFd on a pipe parsed %v; the C parses nothing", obj.Type())
	}
}
