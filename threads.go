package main

import (
	"fmt"
	"sort"
)

type threadEntry struct {
	name    string
	samples int
}

func computeThreads(sf *stackFile) (ranked []threadEntry, noThread int, hasThread bool) {
	if sf.totalSamples == 0 {
		return nil, 0, false
	}

	threadCounts := make(map[string]int)

	for i := range sf.stacks {
		if sf.stacks[i].thread != "" {
			threadCounts[sf.stacks[i].thread] += sf.stacks[i].count
			hasThread = true
		} else {
			noThread += sf.stacks[i].count
		}
	}

	for name, cnt := range threadCounts {
		ranked = append(ranked, threadEntry{name, cnt})
	}
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].samples > ranked[j].samples })
	return
}

func cmdThreads(sf *stackFile, top int) {
	ranked, noThread, hasThread := computeThreads(sf)
	if !hasThread {
		if sf.totalSamples > 0 {
			fmt.Println("no thread info in this file")
		}
		return
	}

	ranked = ranked[:truncate(len(ranked), top)]

	fmt.Printf("%-30s %9s %7s\n", "THREAD", "SAMPLES", "PCT")
	for _, e := range ranked {
		pct := 100.0 * float64(e.samples) / float64(sf.totalSamples)
		fmt.Printf("%-30s %9d %6.1f%%\n", e.name, e.samples, pct)
	}
	if noThread > 0 {
		pct := 100.0 * float64(noThread) / float64(sf.totalSamples)
		fmt.Printf("%-30s %9d %6.1f%%\n", "(no thread info)", noThread, pct)
	}
}
