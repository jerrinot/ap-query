package main

import (
	"fmt"
	"sort"
)

type hotEntry struct {
	name       string
	selfCount  int
	totalCount int
}

func computeHot(sf *stackFile, fqn bool) []hotEntry {
	if sf.totalSamples == 0 {
		return nil
	}

	selfCounts := make(map[string]int)
	totalCounts := make(map[string]int)

	for i := range sf.stacks {
		st := &sf.stacks[i]
		if len(st.frames) > 0 {
			selfCounts[displayName(st.frames[len(st.frames)-1], fqn)] += st.count
		}
		seen := make(map[string]bool)
		for _, fr := range st.frames {
			key := displayName(fr, fqn)
			if !seen[key] {
				totalCounts[key] += st.count
				seen[key] = true
			}
		}
	}

	var ranked []hotEntry
	for name, sc := range selfCounts {
		ranked = append(ranked, hotEntry{name, sc, totalCounts[name]})
	}
	for name, tc := range totalCounts {
		if _, hasSelf := selfCounts[name]; !hasSelf {
			ranked = append(ranked, hotEntry{name, 0, tc})
		}
	}
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].selfCount > ranked[j].selfCount })
	return ranked
}

func printHotTables(ranked []hotEntry, top, totalSamples int, showTopN bool) {
	selfRanked := ranked[:truncate(len(ranked), top)]

	if showTopN {
		fmt.Printf("=== RANK BY SELF TIME (top %d) ===\n", len(selfRanked))
	} else {
		fmt.Println("=== RANK BY SELF TIME ===")
	}
	fmt.Printf("%-50s %7s %7s %9s\n", "METHOD", "SELF%", "TOTAL%", "SAMPLES")
	for _, e := range selfRanked {
		sp := 100.0 * float64(e.selfCount) / float64(totalSamples)
		tp := 100.0 * float64(e.totalCount) / float64(totalSamples)
		fmt.Printf("%-50s %6.1f%% %6.1f%% %9d\n", e.name, sp, tp, e.selfCount)
	}

	totalRanked := make([]hotEntry, len(ranked))
	copy(totalRanked, ranked)
	sort.Slice(totalRanked, func(i, j int) bool { return totalRanked[i].totalCount > totalRanked[j].totalCount })
	totalRanked = totalRanked[:truncate(len(totalRanked), top)]

	fmt.Println()
	if showTopN {
		fmt.Printf("=== RANK BY TOTAL TIME (top %d) ===\n", len(totalRanked))
	} else {
		fmt.Println("=== RANK BY TOTAL TIME ===")
	}
	fmt.Printf("%-50s %7s %7s %9s\n", "METHOD", "SELF%", "TOTAL%", "SAMPLES")
	for _, e := range totalRanked {
		sp := 100.0 * float64(e.selfCount) / float64(totalSamples)
		tp := 100.0 * float64(e.totalCount) / float64(totalSamples)
		fmt.Printf("%-50s %6.1f%% %6.1f%% %9d\n", e.name, sp, tp, e.totalCount)
	}
}

func cmdHot(sf *stackFile, top int, fqn bool, assertBelow float64) error {
	ranked := computeHot(sf, fqn)
	if len(ranked) == 0 {
		return nil
	}

	printHotTables(ranked, top, sf.totalSamples, false)

	// assert-below stays on self-time section only
	if assertBelow > 0 && len(ranked) > 0 {
		selfPct := 100.0 * float64(ranked[0].selfCount) / float64(sf.totalSamples)
		if selfPct >= assertBelow {
			return fmt.Errorf("ASSERT FAILED: %s self=%.1f%% >= threshold %.1f%%", ranked[0].name, selfPct, assertBelow)
		}
	}
	return nil
}
