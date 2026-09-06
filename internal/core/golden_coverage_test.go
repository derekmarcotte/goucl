package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGoldenCoverage guards the guard.
//
// TestBasicGoldens skips a fixture/mode pair when neither a .golden nor a .err
// file exists for it. That skip is reasonable in itself -- a fixture added
// without running tools/gen-goldens.sh has nothing to compare against -- but it
// means deleting a golden silently reduces coverage instead of failing. Since
// the whole correctness argument for this port rests on those files, a missing
// one has to be loud.
//
// Every fixture must therefore have exactly one expectation per mode: either
// output was captured, or the parse legitimately failed and its diagnostic was.
// Never both, and never neither.
func TestGoldenCoverage(t *testing.T) {
	ins, err := filepath.Glob("testdata/basic/*.in")
	if err != nil {
		t.Fatalf("globbing fixtures: %v", err)
	}
	if len(ins) == 0 {
		t.Fatal("no fixtures in testdata/basic; the golden suite would vacuously pass")
	}

	missing, both := 0, 0
	for _, in := range ins {
		base := strings.TrimSuffix(filepath.Base(in), ".in")
		for _, mode := range basicModes {
			stem := filepath.Join("testdata/basic", base+"."+mode.name)
			_, gerr := os.Stat(stem + ".golden")
			_, eerr := os.Stat(stem + ".err")

			switch {
			case gerr != nil && eerr != nil:
				t.Errorf("%s.%s: no .golden and no .err -- TestBasicGoldens will skip this "+
					"silently; regenerate with tools/gen-goldens.sh", base, mode.name)
				missing++
			case gerr == nil && eerr == nil:
				t.Errorf("%s.%s: has both .golden and .err; the expectation is ambiguous",
					base, mode.name)
				both++
			}
		}
	}

	want := len(ins) * len(basicModes)
	if missing == 0 && both == 0 {
		t.Logf("%d fixtures x %d modes = %d expectations, all present", len(ins), len(basicModes), want)
	}
}

// The generated goldens are only meaningful if they still agree with the C
// implementation's own recorded output. Upstream ships a .res for most
// fixtures, which is the config-mode result; those must match ours exactly.
// This catches a regenerated golden that silently drifted from upstream.
func TestConfigGoldensMatchUpstreamRes(t *testing.T) {
	res, err := filepath.Glob("testdata/basic/*.res")
	if err != nil {
		t.Fatalf("globbing: %v", err)
	}
	if len(res) == 0 {
		t.Fatal("no upstream .res files vendored; the cross-check is vacuous")
	}

	for _, r := range res {
		base := strings.TrimSuffix(filepath.Base(r), ".res")
		t.Run(base, func(t *testing.T) {
			want, err := os.ReadFile(r)
			if err != nil {
				t.Fatalf("reading %s: %v", r, err)
			}
			got, err := os.ReadFile(filepath.Join("testdata/basic", base+".config.golden"))
			if err != nil {
				// Upstream ships a .res for this fixture, so a captured
				// config golden must exist for it too.
				t.Fatalf("no config golden for %s, but upstream ships a .res: %v", base, err)
			}
			if string(got) != string(want) {
				t.Errorf("config golden for %s differs from the upstream .res", base)
			}
		})
	}
}
