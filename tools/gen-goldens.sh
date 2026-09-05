#!/bin/sh
# Regenerate emitter goldens from the reference C implementation.
#
# The upstream C suite only diffs CONFIG-emitter output against a golden and
# treats JSON/YAML/compact as smoke tests. We capture all four from the C
# oracle so the Go port is pinned on every emitter.
#
# CWD MATTERS: `.include` with a relative path (e.g. basic/14.in's
# `.include(try=true) "./1.in"`) resolves against the process working
# directory, not the including file's directory. Upstream ctest runs the
# wrappers from the build directory, where "./1.in" does not exist, so the
# golden records the try=true not-found path. We reproduce that cwd exactly.
#
# Usage: tools/gen-goldens.sh   (from anywhere; needs libucl/build built)
set -e
ROOT=$(cd "$(dirname "$0")/.." && pwd)
WORK=$(cd "$ROOT/.." && pwd)
PROG=$WORK/libucl/build/tests/test_basic
SRC=$WORK/libucl/tests/basic
DST=$ROOT/testdata/basic
RUNDIR=$WORK/libucl/build

[ -x "$PROG" ] || { echo "build the C reference first: cmake -B ../libucl/build ../libucl && cmake --build ../libucl/build" >&2; exit 1; }

rm -rf "$DST"
mkdir -p "$DST"
# Verbatim copy of upstream fixtures (.in, .res, .inc, include_dir/, ...).
cp -a "$SRC"/. "$DST"/

cd "$RUNDIR"
for tin in "$SRC"/*.in; do
    base=$(basename "$tin" .in)
    for mode in config json compact yaml; do
        case $mode in
            config)  set -- ;;
            json)    set -- -j ;;
            compact) set -- -c -j ;;
            yaml)    set -- -y ;;
        esac
        # Two-arg form writes the emitted bytes straight to the file, so
        # trailing newlines survive (command substitution would strip them).
        if ! "$PROG" "$@" "$tin" "$DST/$base.$mode.golden" 2>/dev/null; then
            rm -f "$DST/$base.$mode.golden"
            echo "  (skip: $base $mode did not parse)" >&2
        fi
    done
done
echo "goldens written to $DST"
