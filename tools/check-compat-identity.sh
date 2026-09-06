#!/bin/sh
# Verify what compat/libucl carries over from github.com/mitchellh/go-libucl.
#
# Three of the four files are byte-identical to f9d0e55. The fourth,
# decoder_test.go, differs only by the comment markers disabling one test that
# is broken upstream -- see the block above that test for the evidence. This
# checks both claims, so neither can rot into a comfortable assumption.
#
# Usage: tools/check-compat-identity.sh
set -e
ROOT=$(cd "$(dirname "$0")/.." && pwd)
WORK=$(cd "$ROOT/.." && pwd)
BINDING=$WORK/go-libucl
REF=f9d0e55
TMP=${TMPDIR:-/tmp}/goucl-identity.$$
mkdir -p "$TMP"
trap 'rm -rf "$TMP"' EXIT

[ -d "$BINDING/.git" ] || { echo "need the reference checkout at $BINDING" >&2; exit 1; }

status=0

for f in decoder.go object_test.go parser_test.go; do
    if git -C "$BINDING" show "$REF:$f" | cmp -s - "$ROOT/compat/libucl/$f"; then
        printf '  identical            %s\n' "$f"
    else
        printf '  DIFFERS              %s\n' "$f"; status=1
    fi
done

# decoder_test.go: strip our header block and the comment markers, then compare.
git -C "$BINDING" show "$REF:decoder_test.go" > "$TMP/orig.go"
awk '/^\/\/ DISABLED IN THIS FORK/{skip=1}
     skip && /^\/\/ TestObjectDecode_structKeys_Deterministic/{skip=0; next}
     skip{next}
     {print}' "$ROOT/compat/libucl/decoder_test.go" | sed 's|^// \?||' > "$TMP/restored.go"

if cmp -s "$TMP/orig.go" "$TMP/restored.go"; then
    printf '  identical once the disabling markers are stripped   decoder_test.go\n'
else
    printf '  DIFFERS BEYOND THE MARKERS   decoder_test.go\n'
    diff "$TMP/orig.go" "$TMP/restored.go" | head -20
    status=1
fi

exit $status
