// compat/libucl is a separate module so that the main goucl module ships
// without it. Four of its files come from github.com/mitchellh/go-libucl,
// which states no licence; keeping them in their own module means the main
// module carries no third-party code of uncertain provenance, and this
// package can be released, held back or dropped independently. See the NOTICE
// file at the repository root.
//
// The import path is unchanged, so migrating call sites are unaffected: this
// is still imported as github.com/derekmarcotte/goucl/compat/libucl.
//
// Development inside this repository resolves the requirement below against
// the working tree, via the go.work at the repository root, so the pinned
// version does not have to exist yet.
//
// This module is versioned independently of the one it requires, and the two
// say different things. Its own API is frozen: it reproduces the API of the
// cgo binding, so changing it would defeat the only reason it exists. The
// main module is still v0 because the idiomatic API it offers may yet change.
// Depending on a v0 costs this module nothing, because the only thing it uses
// from it is internal/core, which no consumer of either module can see.
module github.com/derekmarcotte/goucl/compat/libucl

go 1.23

require github.com/derekmarcotte/goucl v0.1.0
