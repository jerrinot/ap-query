package main

import (
	"fmt"
	"math"
	"sort"
)

func selfPcts(sf *stackFile, fqn bool) map[string]float64 {
	counts := make(map[string]int)
	for i := range sf.stacks {
		st := &sf.stacks[i]
		if len(st.frames) > 0 {
			counts[displayName(st.frames[len(st.frames)-1], fqn)] += st.count
		}
	}
	pcts := make(map[string]float64)
	if sf.totalSamples > 0 {
		for name, c := range counts {
			pcts[name] = 100.0 * float64(c) / float64(sf.totalSamples)
		}
	}
	return pcts
}

func cmdDiff(before, after *stackFile, minDelta float64, top int, fqn bool) {
	beforePct := selfPcts(before, fqn)
	afterPct := selfPcts(after, fqn)

	allMethods := make(map[string]bool)
	for m := range beforePct {
		allMethods[m] = true
	}
	for m := range afterPct {
		allMethods[m] = true
	}

	type diffEntry struct {
		name   string
		before float64
		after  float64
		delta  float64
	}

	var regressions, improvements, newMethods, goneMethods []diffEntry

	for m := range allMethods {
		b := beforePct[m]
		a := afterPct[m]
		delta := a - b

		_, inBefore := beforePct[m]
		_, inAfter := afterPct[m]

		if inBefore && inAfter {
			if math.Abs(delta) < minDelta {
				continue
			}
			if delta > 0 {
				regressions = append(regressions, diffEntry{m, b, a, delta})
			} else {
				improvements = append(improvements, diffEntry{m, b, a, delta})
			}
		} else if !inBefore && inAfter {
			if a < minDelta {
				continue
			}
			newMethods = append(newMethods, diffEntry{m, 0, a, a})
		} else if inBefore && !inAfter {
			if b < minDelta {
				continue
			}
			goneMethods = append(goneMethods, diffEntry{m, b, 0, -b})
		}
	}

	sort.Slice(regressions, func(i, j int) bool { return regressions[i].delta > regressions[j].delta })
	sort.Slice(improvements, func(i, j int) bool { return improvements[i].delta < improvements[j].delta })
	sort.Slice(newMethods, func(i, j int) bool { return newMethods[i].after > newMethods[j].after })
	sort.Slice(goneMethods, func(i, j int) bool { return goneMethods[i].before > goneMethods[j].before })

	regressions = regressions[:truncate(len(regressions), top)]
	improvements = improvements[:truncate(len(improvements), top)]
	newMethods = newMethods[:truncate(len(newMethods), top)]
	goneMethods = goneMethods[:truncate(len(goneMethods), top)]

	anyOutput := false

	if len(regressions) > 0 {
		fmt.Println("REGRESSION")
		for _, e := range regressions {
			fmt.Printf("  %-50s %5.1f%% -> %5.1f%%  (+%.1f%%)\n", e.name, e.before, e.after, e.delta)
		}
		anyOutput = true
	}
	if len(improvements) > 0 {
		fmt.Println("IMPROVEMENT")
		for _, e := range improvements {
			fmt.Printf("  %-50s %5.1f%% -> %5.1f%%  (%.1f%%)\n", e.name, e.before, e.after, e.delta)
		}
		anyOutput = true
	}
	if len(newMethods) > 0 {
		fmt.Println("NEW")
		for _, e := range newMethods {
			fmt.Printf("  %-50s %.1f%%\n", e.name, e.after)
		}
		anyOutput = true
	}
	if len(goneMethods) > 0 {
		fmt.Println("GONE")
		for _, e := range goneMethods {
			fmt.Printf("  %-50s %.1f%%\n", e.name, e.before)
		}
		anyOutput = true
	}

	if !anyOutput {
		fmt.Println("no significant changes")
	}
}
