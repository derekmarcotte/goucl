#!/bin/sh
# Regenerate expected-output goldens from the reference C implementation.
#
# Upstream's tests/basic.test diffs only the CONFIG emitter against a .res
# golden; it runs the JSON, compact-JSON, YAML, save-comments and
# disable-macro modes with output redirected to /dev/null, and checks the
# exit status of only the FIRST invocation. So five of the six modes are
# effectively untested upstream. We capture all six from the C oracle.
#
# WHAT THE GOLDENS ACTUALLY CONTAIN (tests/test_basic.c). To reproduce these
# bytes, a Go test must replicate the harness exactly, not just parse+emit:
#
#   1. Parser flags are always UCL_PARSER_KEY_LOWERCASE, plus SAVE_COMMENTS
#      for -C and DISABLE_MACRO for -M.
#   2. The variable ABI is registered as "unknown", and file variables are set
#      from the input filename.
#   3. The input is parsed, emitted, THEN REPARSED AND EMITTED AGAIN. The
#      golden is the output of the second emit -- emit(parse(emit(parse(in)))).
#      It therefore pins round-trip idempotence, not a single emit.
#   4. The harness prints one unconditional trailing "\n" after the emitted
#      bytes. Every golden ends with a newline the emitter did not produce.
#
# CWD MATTERS: `.include` with a relative path (e.g. basic/14.in's
# `.include(try=true) "./1.in"`) resolves against the process working
# directory, not the including file's directory. Upstream ctest runs the
# wrappers from the build directory, where "./1.in" does not exist, so the
# golden records the try=true not-found path. We reproduce that cwd exactly.
#
# Output per fixture and mode:
#   <base>.<mode>.golden   emitted bytes, when the parse succeeded
#   <base>.<mode>.err      normalised error text, when the parse failed
#
# NOTE test_basic.c writes parse errors to the OUTPUT file, not to stderr
# ("Error occurred (phase 1): ..."), so error text is recovered from the
# output file rather than from a stderr redirect.
#
# A failure is a legitimate expectation rather than a skip, so it is captured
# as a golden of its own. As of the fixes carried on the libucl branches
# fix-array-leading-comment, fix-disable-macro and fix-truncated-unicode-escape
# no fixture fails any more, and the reference build these goldens come from is
# expected to carry them (see docs/findings.md items 2, 3 and 9).
#
# Usage: tools/gen-goldens.sh   (from anywhere; needs the C reference built)
set -e
ROOT=$(cd "$(dirname "$0")/.." && pwd)
WORK=$(cd "$ROOT/.." && pwd)
PROG=$WORK/libucl/build/tests/test_basic
SRC=$WORK/libucl/tests/basic
DST=$ROOT/internal/core/testdata/basic
RUNDIR=$WORK/libucl/build

[ -x "$PROG" ] || { echo "build the C reference first: cmake -B ../libucl/build ../libucl && cmake --build ../libucl/build" >&2; exit 1; }

rm -rf "$DST"
mkdir -p "$DST"
# Verbatim copy of upstream fixtures (.in, .res, .inc, include_dir/, ...).
cp -a "$SRC"/. "$DST"/

cd "$RUNDIR"
nok=0
nerr=0
for tin in "$SRC"/*.in; do
    base=$(basename "$tin" .in)
    for mode in config json compact yaml comments nomacro; do
        case $mode in
            config)   set -- ;;
            json)     set -- -j ;;
            compact)  set -- -c -j ;;
            yaml)     set -- -y ;;
            comments) set -- -C ;;
            nomacro)  set -- -M ;;
        esac
        # Two-arg form writes emitted bytes straight to the file, so trailing
        # newlines survive (command substitution would strip them).
        if "$PROG" "$@" "$tin" "$DST/$base.$mode.golden" 2>/dev/null; then
            nok=$((nok + 1))
        else
            sed -e "s|$SRC/|testdata/basic/|g" "$DST/$base.$mode.golden" \
                > "$DST/$base.$mode.err"
            rm -f "$DST/$base.$mode.golden"
            nerr=$((nerr + 1))
        fi
    done
done
echo "$nok output goldens, $nerr expected-error goldens -> $DST"
