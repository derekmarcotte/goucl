#!/bin/sh
# Regenerate expected-output goldens from the reference C implementation.
#
# Upstream's tests/basic.test diffs only the CONFIG emitter against a .res
# golden; it runs the JSON, compact-JSON, YAML, save-comments and
# disable-macro modes with output redirected to /dev/null, and checks the
# exit status of only the FIRST invocation. So five of the six modes are
# effectively untested upstream. We capture all six from the C oracle.
#
# CWD MATTERS: `.include` with a relative path (e.g. basic/14.in's
# `.include(try=true) "./1.in"`) resolves against the process working
# directory, not the including file's directory. Upstream ctest runs the
# wrappers from the build directory, where "./1.in" does not exist, so the
# golden records the try=true not-found path. We reproduce that cwd exactly.
#
# Output per fixture and mode:
#   <base>.<mode>.golden   emitted bytes, when the parse succeeded
#   <base>.<mode>.err      normalised stderr, when the parse failed
# A failure is a legitimate expectation, not a skip: basic/14.in genuinely
# fails to parse under -M because a disabled `.include` macro is then read as
# an ordinary key and rejected. The Go port must fail the same way.
#
# Usage: tools/gen-goldens.sh   (from anywhere; needs the C reference built)
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
err=$(mktemp)
trap 'rm -f "$err"' EXIT
nglobal=0 nerr=0
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
        if "$PROG" "$@" "$tin" "$DST/$base.$mode.golden" 2>"$err"; then
            nglobal=$((nglobal + 1))
        else
            rm -f "$DST/$base.$mode.golden"
            # Absolute paths would not survive a checkout elsewhere.
            sed -e "s|$SRC/|testdata/basic/|g" "$err" > "$DST/$base.$mode.err"
            nerr=$((nerr + 1))
        fi
    done
done
echo "$nglobal output goldens, $nerr expected-error goldens -> $DST"
