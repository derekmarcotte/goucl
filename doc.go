// Package goucl reads and writes UCL, the Universal Configuration Language.
//
// UCL is nginx-like configuration syntax that is a strict superset of JSON,
// with comments, includes, macros, variables, multiline strings, and suffixed
// values such as 10s or 1kb.
//
//	doc, err := goucl.ParseFile("app.conf")
//	if err != nil {
//		var se *goucl.SyntaxError
//		if errors.As(err, &se) {
//			fmt.Printf("%s\n%s", se, se.Excerpt())
//		}
//		return err
//	}
//
//	host, ok := doc.String("server.host")
//	port, ok := doc.Int("server.port")
//
//	for key, v := range doc.All() {
//		...
//	}
//
// # Fidelity
//
// A UCL document means here exactly what it means to the C library. Anything a
// document can observe -- how includes resolve, macros, variables, priorities,
// duplicate-key merging, emitted bytes -- is treated as the language's
// definition rather than as behaviour to improve, and surprising cases are
// reproduced rather than corrected. Where the C has a defect that crashes or
// returns a wrong answer, it is fixed and reported upstream; those few
// divergences are listed in docs/findings.md.
//
// The engine underneath mirrors the C API one-to-one and is verified against
// output captured from the compiled C library: 168 goldens across six emitter
// modes. This package is a Go-shaped surface over that engine, not a
// reimplementation of it.
//
// # Compatibility
//
// Callers migrating from the cgo binding github.com/mitchellh/go-libucl want
// the compat/libucl subpackage, which reproduces that API exactly and passes
// its test suite unmodified. This package is for new code and makes no attempt
// to match it.
package goucl
