package main

import (
	"fmt"
	"sort"

	"github.com/spf13/cobra"
)

func newHotCmd() *cobra.Command {
	var shared sharedFlags
	var top int
	var fqn bool
	var assertBelow float64
	cmd := &cobra.Command{
		Use:   "hot <file>",
		Short: "Rank methods by self-time and total-time",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if assertBelow < 0 {
				return fmt.Errorf("--assert-below must not be negative (got %g)", assertBelow)
			}
			pctx, err := preprocessProfile(shared.toOpts(args[0], "hot"))
			if err != nil {
				return err
			}
			return cmdHot(pctx.sf, top, fqn, assertBelow)
		},
	}
	shared.register(cmd)
	cmd.Flags().IntVar(&top, "top", 10, "Limit output rows")
	cmd.Flags().BoolVar(&fqn, "fqn", false, "Show fully-qualified names")
	cmd.Flags().Float64Var(&assertBelow, "assert-below", 0, "Exit 1 if top method self% >= F (for CI gates)")
	return cmd
}

type hotEntry struct {
	name       string
	selfCount  int
	totalCount int
}

func selfCounts(sf *stackFile, fqn bool) map[string]int {
	counts := make(map[string]int)
	for i := range sf.stacks {
		st := &sf.stacks[i]
		if len(st.frames) > 0 {
			counts[displayName(st.frames[len(st.frames)-1], fqn)] += st.count
		}
	}
	return counts
}

func computeHot(sf *stackFile, fqn bool) []hotEntry {
	if sf.totalSamples == 0 {
		return nil
	}

	sc := selfCounts(sf, fqn)
	totalCounts := make(map[string]int)

	for i := range sf.stacks {
		st := &sf.stacks[i]
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
	for name, s := range sc {
		ranked = append(ranked, hotEntry{name, s, totalCounts[name]})
	}
	for name, tc := range totalCounts {
		if _, hasSelf := sc[name]; !hasSelf {
			ranked = append(ranked, hotEntry{name, 0, tc})
		}
	}
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].selfCount > ranked[j].selfCount })
	return ranked
}

// computeHotSorted returns hot entries sorted by the given key ("self" or "total").
// An empty sortBy defaults to "self". Returns an error for unknown values.
func computeHotSorted(sf *stackFile, fqn bool, sortBy string) ([]hotEntry, error) {
	if sortBy == "" {
		sortBy = "self"
	}
	if sortBy != "self" && sortBy != "total" {
		return nil, fmt.Errorf("hot: sort must be \"self\" or \"total\", got %q", sortBy)
	}
	ranked := computeHot(sf, fqn)
	if sortBy == "total" {
		sort.Slice(ranked, func(i, j int) bool { return ranked[i].totalCount > ranked[j].totalCount })
	}
	return ranked, nil
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
		sp := pctOf(e.selfCount, totalSamples)
		tp := pctOf(e.totalCount, totalSamples)
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
		sp := pctOf(e.selfCount, totalSamples)
		tp := pctOf(e.totalCount, totalSamples)
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
		selfPct := pctOf(ranked[0].selfCount, sf.totalSamples)
		if selfPct >= assertBelow {
			return fmt.Errorf("ASSERT FAILED: %s self=%.1f%% >= threshold %.1f%%", ranked[0].name, selfPct, assertBelow)
		}
	}
	return nil
}
