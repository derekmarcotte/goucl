# Documentation

Three kinds of document, kept apart because they have different authority and
different lifespans. When two disagree, the order below is the tiebreaker —
and the disagreement is a bug worth fixing in the loser.

## 1. Decisions — authoritative

| | |
|---|---|
| [`findings.md`](findings.md) | Every place this port knowingly differs from libucl 0.9.4, with the evidence and the reasoning. |
| [`review.md`](review.md) | The review pass: what was checked, what it found. |

`findings.md` is the one file to read before changing behaviour. Each entry
records a defect in the C and an explicit *replicate* or *diverge* decision.
The rule the entries follow: reproduce upstream bugs bug-for-bug, because
callers and goldens depend on them, **except** where a bug produces a wrong
answer or a crash. Four have been fixed here and reported upstream.

## 2. Implementation notes — how this port is built

[`notes/`](notes/) — one file per component, written while porting it:
naming decisions, quirks reproduced on purpose, things that surprised the
author, and what was left out.

`compat.md`, `emitter.md`, `msgpack.md`, `parser.md`, `schema.md`, `sexp.md`,
plus [`notes/compat-plan.md`](notes/compat-plan.md), the design of the
drop-in shim and why it is built from defined types rather than aliases or
wrappers.

These describe *our* code, so they go stale when it changes. They defer to
`findings.md` on divergences rather than restating them, precisely so the two
cannot drift apart.

## 3. Reference — how the C behaves

[`reference/`](reference/) — behaviour extracted from the libucl sources
before implementation, so the port could be written and reviewed without
re-reading C each time:

`object-model.md`, `parser.md`, `emitter.md`, `schema.md`, `msgpack.md`,
`sexp.md`, `tests.md`, and `compat.md` (the mitchellh/go-libucl API this
package must reproduce).

**These have known errata.** They were written from reading, and reading is
fallible: `reference/emitter.md` §2.2 missed a YAML separator case,
`reference/parser.md` §7.1 got macro shadowing backwards, and its §12.2 listed
three out-of-bounds reads where only one is real. Each was caught by testing
against the compiled library and is recorded in `findings.md`.

So: **the oracle wins.** Where a spec and the C disagree, the C is right and
the spec gets corrected. Where the C and a golden disagree, the golden is
right — it came from running the C.
