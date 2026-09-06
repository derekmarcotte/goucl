package core

// Benchmarks for the engine, written to be directly comparable with the C.
//
// tools/bench-c.c performs the same operations on the same fixtures, and
// tools/run-bench.sh runs both and prints the comparison. The two harnesses
// are kept symmetrical on purpose:
//
//   - Parsing builds a fresh parser and a fresh tree every iteration on both
//     sides, so per-call setup is charged equally rather than amortised away
//     on whichever side happens to allow it.
//   - Emitting parses once and re-emits the same tree on both sides, and both
//     materialise a buffer, because the C entry point always returns one.
//   - The C parser is created without zero-copy, matching AddChunk, which
//     does not borrow the caller's bytes either.
//
// The fixtures are the smallest, a middling one and the largest self-contained
// document in the corpus. All three are free of includes, so nothing here
// depends on the working directory or touches the filesystem mid-parse.

import (
	"io"
	"os"
	"testing"
)

var benchFixtures = []string{
	"testdata/basic/1.in", // 187 bytes
	"testdata/basic/2.in", // 490 bytes
	"testdata/basic/4.in", // 1615 bytes
}

func benchInput(b *testing.B, path string) []byte {
	b.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		b.Fatalf("reading %s: %v", path, err)
	}
	return data
}

func benchObject(b *testing.B, path string) *Object {
	b.Helper()
	p := NewParser(ParserDefault)
	if !p.AddChunk(benchInput(b, path)) {
		b.Fatalf("parsing %s: %v", path, p.GetError())
	}
	obj := p.GetObject()
	if obj == nil {
		b.Fatalf("parsing %s produced no document", path)
	}
	return obj
}

func BenchmarkParse(b *testing.B) {
	for _, path := range benchFixtures {
		data := benchInput(b, path)
		b.Run(path, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(data)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				p := NewParser(ParserDefault)
				if !p.AddChunk(data) {
					b.Fatal(p.GetError())
				}
				if p.GetObject() == nil {
					b.Fatal("no document")
				}
			}
		})
	}
}

func benchmarkEmit(b *testing.B, format Emitter) {
	for _, path := range benchFixtures {
		obj := benchObject(b, path)
		b.Run(path, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				out, err := EmitText(obj, format)
				if err != nil {
					b.Fatal(err)
				}
				if len(out) == 0 {
					b.Fatal("empty output")
				}
			}
		})
	}
}

func BenchmarkEmitJSON(b *testing.B)        { benchmarkEmit(b, EmitJSON) }
func BenchmarkEmitJSONCompact(b *testing.B) { benchmarkEmit(b, EmitJSONCompact) }
func BenchmarkEmitConfig(b *testing.B)      { benchmarkEmit(b, EmitConfig) }

// BenchmarkEmitConfigTo writes straight to a sink instead of returning a
// buffer. It has no counterpart in the C, whose emit entry point always
// allocates one, so it is excluded from the comparison and kept only to show
// what the returned buffer costs.
func BenchmarkEmitConfigTo(b *testing.B) {
	for _, path := range benchFixtures {
		obj := benchObject(b, path)
		b.Run(path, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := EmitTextFull(io.Discard, obj, EmitConfig, nil); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
