package main

import (
	"fmt"
	"strings"
)

const infoHelp = `Usage: ap-query info [flags] <file>

One-shot triage: events, threads, hot methods, and drill-down.

Flags:
  --expand N              Auto-expand top N hot methods (default: 3, 0=off).
  --top-threads N         Threads shown (default: 10, 0=all).
  --top-methods N         Hot methods shown (default: 20, 0=all).
  --event TYPE, -e TYPE   Event type (default: cpu).
  -t THREAD               Filter to threads matching substring.
  --from DURATION         Start of time window (JFR only).
  --to DURATION           End of time window (JFR only).
  --no-idle               Remove idle leaf frames.

Examples:
  ap-query info profile.jfr
  ap-query info profile.jfr --expand 0 --top-methods 10
`

type infoOpts struct {
	eventType     string
	isJFR         bool
	eventCounts   map[string]int
	expand        int
	topThreads    int
	topMethods    int
	spanNanos     int64
	stacksByEvent map[string]*stackFile
}

func cmdInfo(sf *stackFile, opts infoOpts) {
	// === Header ===
	if opts.spanNanos > 0 {
		fmt.Printf("Duration: %s  Samples: %d (%s)\n\n", formatDuration(opts.spanNanos), sf.totalSamples, opts.eventType)
	} else if opts.isJFR && len(opts.eventCounts) > 0 {
		fmt.Printf("Event: %s\n\n", opts.eventType)
	}

	// === CPU vs WALL ===
	printCrossEventSummary(opts.stacksByEvent, opts.topThreads)

	// === THREADS ===
	ranked, _, hasThread := computeThreads(sf)
	if hasThread {
		shown := ranked[:truncate(len(ranked), opts.topThreads)]
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
		printHotTables(hot, opts.topMethods, sf.totalSamples, true)
	}

	fmt.Printf("\nTotal samples: %d\n", sf.totalSamples)

	// === DRILL-DOWN ===
	if opts.expand > 0 && len(hot) > 0 {
		drillDown := hot[:truncate(len(hot), opts.expand)]
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
	if opts.isJFR && len(opts.eventCounts) > 1 {
		others := formatEventList(opts.eventCounts, opts.eventType)
		if len(others) > 0 {
			fmt.Printf("\nAlso available: %s\n", strings.Join(others, ", "))
		}
	}
}

func printCrossEventSummary(stacksByEvent map[string]*stackFile, topGroups int) {
	if stacksByEvent == nil {
		return
	}
	cpuSF := stacksByEvent["cpu"]
	wallSF := stacksByEvent["wall"]
	if cpuSF == nil || wallSF == nil || cpuSF.totalSamples == 0 || wallSF.totalSamples == 0 {
		return
	}

	cpuThreads, _, cpuHas := computeThreads(cpuSF)
	wallThreads, _, wallHas := computeThreads(wallSF)
	if !cpuHas || !wallHas {
		return
	}

	// Compute group assignments from the combined thread set so that
	// a thread pool appearing in only one event type still merges
	// consistently with the other side.
	combined := make([]threadEntry, 0, len(cpuThreads)+len(wallThreads))
	combined = append(combined, cpuThreads...)
	combined = append(combined, wallThreads...)
	assignments := assignGroups(combined)

	cpuGroups := groupThreadsWith(cpuThreads, assignments)
	wallGroups := groupThreadsWith(wallThreads, assignments)

	wallMap := make(map[string]threadGroupEntry, len(wallGroups))
	for _, g := range wallGroups {
		wallMap[g.name] = g
	}

	type row struct {
		name    string
		threads int
		cpuPct  float64
		wallPct float64
	}

	seen := make(map[string]bool, len(cpuGroups)+len(wallGroups))
	var rows []row

	// CPU groups first (sorted by CPU samples).
	for _, cg := range cpuGroups {
		seen[cg.name] = true
		wg := wallMap[cg.name]
		cp := pctOf(cg.samples, cpuSF.totalSamples)
		wp := pctOf(wg.samples, wallSF.totalSamples)
		threads := cg.threads
		if wg.threads > threads {
			threads = wg.threads
		}
		rows = append(rows, row{cg.name, threads, cp, wp})
	}
	// Wall-only groups (not seen in CPU).
	for _, wg := range wallGroups {
		if seen[wg.name] {
			continue
		}
		wp := pctOf(wg.samples, wallSF.totalSamples)
		rows = append(rows, row{wg.name, wg.threads, 0, wp})
	}

	shown := rows[:truncate(len(rows), topGroups)]
	fmt.Printf("=== CPU vs WALL ===\n")
	fmt.Printf("%-30s %7s %7s\n", "GROUP (threads)", "CPU%", "WALL%")
	for _, r := range shown {
		label := fmt.Sprintf("%s (%d)", r.name, r.threads)
		fmt.Printf("%-30s %6.1f%% %6.1f%%\n", label, r.cpuPct, r.wallPct)
	}
	fmt.Println()
}
