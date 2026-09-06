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
module github.com/derekmarcotte/goucl/compat/libucl

go 1.23

require github.com/derekmarcotte/goucl v1.0.0
