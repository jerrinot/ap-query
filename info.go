package main

import (
	"fmt"
	"strings"
)

func cmdInfo(sf *stackFile, eventType string, isJFR bool, eventCounts map[string]int, expand, topThreads, topMethods int, spanNanos int64) {
	// === Header ===
	if spanNanos > 0 {
		fmt.Printf("Duration: %s  Samples: %d (%s)\n\n", formatDuration(spanNanos), sf.totalSamples, eventType)
	} else if isJFR && len(eventCounts) > 0 {
		fmt.Printf("Event: %s\n\n", eventType)
	}

	// === THREADS ===
	ranked, _, hasThread := computeThreads(sf)
	if hasThread {
		shown := ranked[:truncate(len(ranked), topThreads)]
		fmt.Printf("=== THREADS (top %d) ===\n", len(shown))
		for _, e := range shown {
			pct := pctOf(e.samples, sf.totalSamples)
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
			sp := pctOf(h.selfCount, sf.totalSamples)
			fmt.Printf("\n=== DRILL-DOWN: %s (self=%.1f%%) ===\n", h.name, sp)

			fmt.Println("--- tree (callees) ---")
			cmdTree(sf, h.name, 3, 1.0)

			fmt.Println("--- callers ---")
			cmdCallers(sf, h.name, 3, 1.0)

			lines, _ := computeLines(sf, h.name, 5, false)
			if len(lines) > 0 {
				fmt.Println("--- lines ---")
				for _, le := range lines {
					pct := pctOf(le.samples, sf.totalSamples)
					fmt.Printf("%s:%-8d %8d %6.1f%%\n", le.name, le.line, le.samples, pct)
				}
			}
		}
	}

	// === Other available events ===
	if isJFR && len(eventCounts) > 1 {
		others := formatEventList(eventCounts, eventType)
		if len(others) > 0 {
			fmt.Printf("\nAlso available: %s\n", strings.Join(others, ", "))
		}
	}
}
