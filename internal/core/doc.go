// Package core is a Go translation of libucl, the C reference implementation
// of the Universal Configuration Language.
//
// # Provenance
//
// This package is derived work, not original work. It is a translation of the
// C sources of libucl 0.9.4 (https://github.com/vstakhov/libucl), copyright
// (c) 2013-2015 Vsevolod Stakhov <vsevolod@highsecure.ru>, with contributions
// within src/ucl_util.c copyright (c) 2015 Allan Jude <allanjude@freebsd.org>,
// distributed under the BSD 2-clause licence. goucl carries the same licence;
// see the LICENSE and NOTICE files at the module root, which also record the
// per-file mapping from C source to Go source.
//
// list.go additionally translates the doubly-linked-list macros of utlist.h,
// copyright (c) 2007-2022 Troy D. Hanson, which libucl bundles and uses to
// thread the implicit arrays formed by duplicate keys.
//
// The Go was produced by automated translation of the C by a large language
// model, function by function, rather than written from a specification or by
// hand. The translation is deliberately literal: each Go function carries the
// name of the C function it came from, control flow follows the original, and
// docs/reference/ records the C behaviour each file was translated against.
// Correctness rests not on the translation but on differential testing against
// the compiled C library — 168 golden outputs across six emitter modes,
// libucl's own corpus, and targeted probes built against libucl.a wherever the
// Go and the C appeared to disagree. Known departures are in docs/findings.md.
//
// # Scope
//
// The package mirrors the C API one-to-one, including behaviour that looks
// wrong, because behaviour a UCL document can observe is the language rather
// than a defect to design around. It is internal: callers want the root
// package github.com/derekmarcotte/goucl, or compat/libucl when migrating
// from the cgo binding.
//
// The build configuration reproduced here is libucl without OpenSSL and
// without libfetch or libcurl, so signature checking and URL includes report
// the same errors that build does.
package core
