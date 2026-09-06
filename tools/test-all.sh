#!/bin/sh
# Run the tests of every module in the repository.
#
# compat/libucl is a separate module (see its go.mod for why), which means
# `go test ./...` at the root no longer reaches it. This does.
#
# Usage: tools/test-all.sh [go test flags...]
set -e
ROOT=$(cd "$(dirname "$0")/.." && pwd)

for mod in . compat/libucl; do
    printf '=== %s\n' "$mod"
    (cd "$ROOT/$mod" && go test "$@" ./...)
done
