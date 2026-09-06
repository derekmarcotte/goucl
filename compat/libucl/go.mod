// compat/libucl is a separate module so that the main goucl module ships
// without it. Four of its files come from github.com/mitchellh/go-libucl,
// which states no licence; keeping them in their own module means the main
// module carries no third-party code of uncertain provenance, and this
// package can be released, held back or dropped independently. See the NOTICE
// file at the repository root.
//
// The import path is unchanged, so migrating call sites are unaffected: this
// is still imported as github.com/derekmarcotte/goucl/compat/libucl.
module github.com/derekmarcotte/goucl/compat/libucl

go 1.23

require github.com/derekmarcotte/goucl v0.0.0

// BEFORE PUBLISHING: drop this replace and pin the require above to a real
// released version of the main module. A replace directive in a dependency is
// ignored by the consumer, so publishing with it would leave anyone importing
// this package trying to resolve a version that does not exist.
replace github.com/derekmarcotte/goucl => ../..
