package goucl

import (
	"fmt"
	"sync"
	"testing"
)

// The pattern cache is keyed by pattern strings taken from the schema, so it
// must not grow without bound when those come from untrusted input.
// Compilation must keep working past the limit; only memoisation stops.
func TestPatternCacheIsBounded(t *testing.T) {
	before := patternCacheLen.Load()

	const n = patternCacheMax + 500
	for i := 0; i < n; i++ {
		re, err := compilePattern(fmt.Sprintf("^unique-pattern-%d$", i))
		if err != nil {
			t.Fatalf("pattern %d failed to compile: %v", i, err)
		}
		if re == nil {
			t.Fatalf("pattern %d compiled to nil", i)
		}
	}

	if got := patternCacheLen.Load(); got > patternCacheMax {
		t.Errorf("cache grew to %d entries, limit is %d", got, patternCacheMax)
	}
	if patternCacheLen.Load() <= before {
		t.Error("cache did not grow at all; the memoisation is dead")
	}

	// Past the limit, matching must still be correct rather than degraded.
	re, err := compilePattern("^past-the-limit$")
	if err != nil {
		t.Fatalf("compiling past the limit failed: %v", err)
	}
	if !re.MatchString("past-the-limit") || re.MatchString("nope") {
		t.Error("a pattern compiled past the cache limit does not match correctly")
	}
}

// compilePattern is reached concurrently when several validations run at once.
func TestPatternCacheConcurrent(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(base int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				if _, err := compilePattern(fmt.Sprintf("^conc-%d$", (base*200+j)%64)); err != nil {
					t.Errorf("compile failed: %v", err)
					return
				}
			}
		}(i)
	}
	wg.Wait()
}
