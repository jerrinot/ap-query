package main

import (
	"fmt"
	"os"
	"sort"
)

func cmdInfo(path string, sf *stackFile, eventType string, isJFR bool, expand, topThreads, topMethods int) {
	// === EVENTS === (JFR only)
	if isJFR {
		counts, err := discoverEvents(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not read events: %v\n", err)
		} else if len(counts) > 0 {
			fmt.Println("=== EVENTS ===")
			type evEntry struct {
				name    string
				samples int
			}
			var evs []evEntry
			for n, c := range counts {
				evs = append(evs, evEntry{n, c})
			}
			sort.Slice(evs, func(i, j int) bool { return evs[i].samples > evs[j].samples })
			for _, e := range evs {
				fmt.Printf("%-10s %9d\n", e.name, e.samples)
			}
			fmt.Println()
		}
	}

	// === THREADS ===
	ranked, _, hasThread := computeThreads(sf)
	if hasThread {
		shown := ranked[:truncate(len(ranked), topThreads)]
		fmt.Printf("=== THREADS (top %d) ===\n", len(shown))
		for _, e := range shown {
			pct := 100.0 * float64(e.samples) / float64(sf.totalSamples)
			fmt.Printf("%-30s %9d %6.1f%%\n", e.name, e.samples, pct)
		}
		fmt.Println()
	}

	// === HOT METHODS ===
	hot := computeHot(sf, false)
	if len(hot) > 0 {
		printHotTables(hot, topMethods, sf.totalSamples, true)
	}

	fmt.Printf("\nTotal samples: %d\n", sf.totalSamples)

	// === DRILL-DOWN ===
	if expand > 0 && len(hot) > 0 {
		drillDown := hot[:truncate(len(hot), expand)]
		for _, h := range drillDown {
			sp := 100.0 * float64(h.selfCount) / float64(sf.totalSamples)
			fmt.Printf("\n=== DRILL-DOWN: %s (self=%.1f%%) ===\n", h.name, sp)

			fmt.Println("--- tree (callees) ---")
			cmdTree(sf, h.name, 3, 1.0)

			fmt.Println("--- callers ---")
			cmdCallers(sf, h.name, 3, 1.0)

			lines, _ := computeLines(sf, h.name, 5, false)
			if len(lines) > 0 {
				fmt.Println("--- lines ---")
				for _, le := range lines {
					pct := 100.0 * float64(le.samples) / float64(sf.totalSamples)
					fmt.Printf("%s:%-8d %8d %6.1f%%\n", le.name, le.line, le.samples, pct)
				}
			}
		}
	}
}
