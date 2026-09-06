#!/bin/sh
# Compare this port's engine against the C reference on the same operations
# and the same fixtures.
#
# The C side needs a reference build:
#   cmake -B ../libucl/build ../libucl && cmake --build ../libucl/build
#
# Usage: tools/run-bench.sh [iterations]   (default 200000)
#
# Both harnesses build a fresh parser per parse iteration, and re-emit a
# once-parsed tree for the emit measurements, so the two are comparable. See
# the comment at the top of internal/core/bench_test.go.
set -e
ROOT=$(cd "$(dirname "$0")/.." && pwd)
WORK=$(cd "$ROOT/.." && pwd)
LIB=$WORK/libucl
ITERS=${1:-200000}
OUT=${TMPDIR:-/tmp}/goucl-bench.$$
mkdir -p "$OUT"
trap 'rm -rf "$OUT"' EXIT

[ -f "$LIB/build/libucl.a" ] || {
    echo "build the C reference first:" >&2
    echo "  cmake -B $LIB/build $LIB && cmake --build $LIB/build" >&2
    exit 1
}

echo "Reference: $(git -C "$LIB" describe --always --dirty 2>/dev/null || echo unknown)" \
     "on branch $(git -C "$LIB" rev-parse --abbrev-ref HEAD 2>/dev/null || echo unknown)"
echo

cc -O2 -I "$LIB/include" -o "$OUT/bench-c" "$ROOT/tools/bench-c.c" "$LIB/build/libucl.a" -lm

cd "$ROOT/internal/core"
for f in testdata/basic/1.in testdata/basic/2.in testdata/basic/4.in; do
    "$OUT/bench-c" "$f" "$ITERS"
done > "$OUT/c.tsv"

go test . -run '^$' -bench 'BenchmarkParse|BenchmarkEmitJSON$|BenchmarkEmitJSONCompact|BenchmarkEmitConfig$' \
    -benchmem -count="${COUNT:-5}" > "$OUT/go.txt"

# Reduce Go's repeated samples to a median per benchmark, then join with C.
awk '
/^Benchmark/ {
    name = $1
    sub(/-[0-9]+$/, "", name)
    n = split(name, part, "/")
    op = part[1]
    sub(/^Benchmark/, "", op)
    fixture = part[n-1] "/" part[n]
    sub(/^testdata\//, "", fixture)
    key = tolower(op) "\t" fixture
    ns[key] = ns[key] " " $3
    allocs[key] = $(NF-1)
    bytes[key] = $(NF-3)
}
END { for (k in ns) print k "\t" ns[k] "\t" bytes[k] "\t" allocs[k] }
' "$OUT/go.txt" | sort > "$OUT/go.tsv"

echo "operation            fixture      C ns/op    Go ns/op   ratio   Go B/op  allocs"
echo "-------------------- ------------ ---------- ---------- ------- -------- ------"
awk -F'\t' '
NR==FNR {
    split($0, f, "\t")
    key = f[1] "\t" f[2]
    n = split(f[3], samples, " ")
    # median of the Go samples
    for (i = 1; i <= n; i++) v[i] = samples[i] + 0
    for (i = 1; i < n; i++) for (j = i+1; j <= n; j++) if (v[j] < v[i]) { t=v[i]; v[i]=v[j]; v[j]=t }
    gons[key] = (n % 2) ? v[(n+1)/2] : (v[n/2] + v[n/2+1]) / 2
    gob[key] = f[4]; goa[key] = f[5]
    next
}
{
    op = $1; fix = $2; cns = $4 + 0
    gsub(/_/, "", op)   # C writes emit_json, Go writes EmitJSON
    sub(/^testdata\//, "", fix)
    key = op "\t" fix
    g = gons[key]
    if (g == "") next
    printf "%-20s %-12s %10.0f %10.0f  %5.2fx %8s %6s\n", op, fix, cns, g, g/cns, gob[key], goa[key]
}
' "$OUT/go.tsv" "$OUT/c.tsv"
