package main

import (
	"fmt"
	"sort"
)

type lineEntry struct {
	name    string
	line    uint32
	samples int
}

// computeLines returns the per-source-line sample counts for the given method.
// hasMethod is true when frames match but no line info is available.
func computeLines(sf *stackFile, method string, top int, fqn bool) (result []lineEntry, hasMethod bool) {
	if sf.totalSamples == 0 {
		return nil, false
	}

	type lineKey struct {
		name string
		line uint32
	}
	lineCounts := make(map[lineKey]int)
	foundAny := false

	for i := range sf.stacks {
		st := &sf.stacks[i]
		seen := make(map[lineKey]bool)
		for j, fr := range st.frames {
			if matchesMethod(fr, method) && st.lines[j] > 0 {
				key := lineKey{displayName(fr, fqn), st.lines[j]}
				if !seen[key] {
					lineCounts[key] += st.count
					seen[key] = true
				}
				foundAny = true
			}
		}
	}

	if !foundAny {
		for i := range sf.stacks {
			for _, fr := range sf.stacks[i].frames {
				if matchesMethod(fr, method) {
					return nil, true
				}
			}
		}
		return nil, false
	}

	var ranked []lineEntry
	for k, c := range lineCounts {
		ranked = append(ranked, lineEntry{k.name, k.line, c})
	}
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].samples > ranked[j].samples })
	ranked = ranked[:truncate(len(ranked), top)]
	return ranked, true
}

func cmdLines(sf *stackFile, method string, top int, fqn bool) error {
	ranked, hasMethod := computeLines(sf, method, top, fqn)
	if ranked == nil {
		if hasMethod {
			return fmt.Errorf("no line info for frames matching '%s'", method)
		}
		fmt.Printf("no frames matching '%s'\n", method)
		return nil
	}

	fmt.Printf("%-40s %9s %7s\n", "SOURCE:LINE", "SAMPLES", "PCT")
	for _, e := range ranked {
		pct := 100.0 * float64(e.samples) / float64(sf.totalSamples)
		loc := fmt.Sprintf("%s:%d", e.name, e.line)
		fmt.Printf("%-40s %9d %6.1f%%\n", loc, e.samples, pct)
	}
	return nil
}
