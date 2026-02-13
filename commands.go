package main

import (
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
)

// ---------------------------------------------------------------------------
// Name helpers
// ---------------------------------------------------------------------------

func shortName(frame string) string {
	// "com/example/App.process" → "App.process"
	// "com.example.App.process" → "App.process"
	base := strings.ReplaceAll(frame, "/", ".")
	parts := strings.Split(base, ".")
	if len(parts) >= 2 {
		return parts[len(parts)-2] + "." + parts[len(parts)-1]
	}
	return base
}

func displayName(frame string, fqn bool) string {
	if fqn {
		return strings.ReplaceAll(frame, "/", ".")
	}
	return shortName(frame)
}

func matchesMethod(frame, pattern string) bool {
	normalized := strings.ReplaceAll(frame, "/", ".")
	return strings.Contains(normalized, pattern) || strings.Contains(shortName(frame), pattern)
}

// ---------------------------------------------------------------------------
// hot
// ---------------------------------------------------------------------------

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

func cmdHot(sf *stackFile, top int, fqn bool, assertBelow float64) {
	ranked := computeHot(sf, fqn)
	if len(ranked) == 0 {
		return
	}

	// Self-time section
	selfRanked := ranked
	if top > 0 && top < len(selfRanked) {
		selfRanked = selfRanked[:top]
	}

	fmt.Println("=== RANK BY SELF TIME ===")
	fmt.Printf("%-50s %7s %7s %9s\n", "METHOD", "SELF%", "TOTAL%", "SAMPLES")
	for _, e := range selfRanked {
		sp := 100.0 * float64(e.selfCount) / float64(sf.totalSamples)
		tp := 100.0 * float64(e.totalCount) / float64(sf.totalSamples)
		fmt.Printf("%-50s %6.1f%% %6.1f%% %9d\n", e.name, sp, tp, e.selfCount)
	}

	// Total-time section
	totalRanked := make([]hotEntry, len(ranked))
	copy(totalRanked, ranked)
	sort.Slice(totalRanked, func(i, j int) bool { return totalRanked[i].totalCount > totalRanked[j].totalCount })
	if top > 0 && top < len(totalRanked) {
		totalRanked = totalRanked[:top]
	}

	fmt.Println()
	fmt.Println("=== RANK BY TOTAL TIME ===")
	fmt.Printf("%-50s %7s %7s %9s\n", "METHOD", "SELF%", "TOTAL%", "SAMPLES")
	for _, e := range totalRanked {
		sp := 100.0 * float64(e.selfCount) / float64(sf.totalSamples)
		tp := 100.0 * float64(e.totalCount) / float64(sf.totalSamples)
		fmt.Printf("%-50s %6.1f%% %6.1f%% %9d\n", e.name, sp, tp, e.totalCount)
	}

	// assert-below stays on self-time section only
	if assertBelow > 0 && len(ranked) > 0 {
		selfPct := 100.0 * float64(ranked[0].selfCount) / float64(sf.totalSamples)
		if selfPct >= assertBelow {
			fmt.Fprintf(os.Stderr, "ASSERT FAILED: %s self=%.1f%% >= threshold %.1f%%\n", ranked[0].name, selfPct, assertBelow)
			os.Exit(1)
		}
	}
}

// ---------------------------------------------------------------------------
// tree
// ---------------------------------------------------------------------------

func cmdTree(sf *stackFile, method string, maxDepth int, minPct float64) {
	if sf.totalSamples == 0 {
		return
	}

	subtreeSamples := make(map[string]int)
	selfSamples := make(map[string]int)
	matchedNames := make(map[string]bool)

	for i := range sf.stacks {
		st := &sf.stacks[i]
		for j, fr := range st.frames {
			if matchesMethod(fr, method) {
				matchedNames[shortName(fr)] = true
				suffix := make([]string, len(st.frames)-j)
				for k := j; k < len(st.frames); k++ {
					suffix[k-j] = shortName(st.frames[k])
				}
				for depth := 1; depth <= len(suffix); depth++ {
					key := strings.Join(suffix[:depth], ";")
					subtreeSamples[key] += st.count
				}
				leafKey := strings.Join(suffix, ";")
				selfSamples[leafKey] += st.count
				break
			}
		}
	}

	if len(subtreeSamples) == 0 {
		fmt.Printf("no frames matching '%s'\n", method)
		return
	}

	roots := make(map[string]bool)
	for key := range subtreeSamples {
		roots[strings.SplitN(key, ";", 2)[0]] = true
	}
	if len(matchedNames) > 1 {
		names := make([]string, 0, len(matchedNames))
		for n := range matchedNames {
			names = append(names, n)
		}
		sort.Strings(names)
		fmt.Printf("# matched %d methods: %s\n", len(matchedNames), strings.Join(names, ", "))
	}

	var printTree func(prefix string, depth, indent int)
	printTree = func(prefix string, depth, indent int) {
		samples := subtreeSamples[prefix]
		pct := 100.0 * float64(samples) / float64(sf.totalSamples)
		if pct < minPct {
			return
		}
		parts := strings.Split(prefix, ";")
		name := parts[len(parts)-1]
		selfCt := selfSamples[prefix]
		selfPct := 0.0
		if selfCt > 0 {
			selfPct = 100.0 * float64(selfCt) / float64(sf.totalSamples)
		}
		pad := strings.Repeat("  ", indent)
		selfSuffix := ""
		if selfPct >= minPct {
			selfSuffix = fmt.Sprintf("  ← self=%.1f%%", selfPct)
		}
		fmt.Printf("%s[%.1f%%] %s%s\n", pad, pct, name, selfSuffix)
		if depth >= maxDepth {
			return
		}
		type child struct {
			key     string
			samples int
		}
		var children []child
		pfx := prefix + ";"
		for key, cnt := range subtreeSamples {
			if strings.HasPrefix(key, pfx) && strings.Count(key, ";") == strings.Count(prefix, ";")+1 {
				children = append(children, child{key, cnt})
			}
		}
		sort.Slice(children, func(i, j int) bool { return children[i].samples > children[j].samples })
		for _, c := range children {
			printTree(c.key, depth+1, indent+1)
		}
	}

	sortedRoots := make([]string, 0, len(roots))
	for r := range roots {
		sortedRoots = append(sortedRoots, r)
	}
	sort.Strings(sortedRoots)
	for _, root := range sortedRoots {
		printTree(root, 1, 0)
	}
}

// ---------------------------------------------------------------------------
// callers
// ---------------------------------------------------------------------------

func cmdCallers(sf *stackFile, method string, maxDepth int, minPct float64) {
	if sf.totalSamples == 0 {
		return
	}

	callerSamples := make(map[string]int)
	matchedNames := make(map[string]bool)

	for i := range sf.stacks {
		st := &sf.stacks[i]
		for j, fr := range st.frames {
			if matchesMethod(fr, method) {
				matchedNames[shortName(fr)] = true
				// prefix from matched frame upward (reversed)
				prefix := make([]string, j+1)
				for k := 0; k <= j; k++ {
					prefix[j-k] = shortName(st.frames[k])
				}
				for depth := 1; depth <= len(prefix); depth++ {
					key := strings.Join(prefix[:depth], ";")
					callerSamples[key] += st.count
				}
				break
			}
		}
	}

	if len(callerSamples) == 0 {
		fmt.Printf("no frames matching '%s'\n", method)
		return
	}

	if len(matchedNames) > 1 {
		names := make([]string, 0, len(matchedNames))
		for n := range matchedNames {
			names = append(names, n)
		}
		sort.Strings(names)
		fmt.Printf("# matched %d methods: %s\n", len(matchedNames), strings.Join(names, ", "))
	}

	roots := make(map[string]bool)
	for key := range callerSamples {
		roots[strings.SplitN(key, ";", 2)[0]] = true
	}

	var printCallers func(prefix string, depth, indent int)
	printCallers = func(prefix string, depth, indent int) {
		samples := callerSamples[prefix]
		pct := 100.0 * float64(samples) / float64(sf.totalSamples)
		if pct < minPct {
			return
		}
		parts := strings.Split(prefix, ";")
		name := parts[len(parts)-1]
		pad := strings.Repeat("  ", indent)
		fmt.Printf("%s[%.1f%%] %s\n", pad, pct, name)
		if depth >= maxDepth {
			return
		}
		type child struct {
			key     string
			samples int
		}
		var children []child
		pfx := prefix + ";"
		for key, cnt := range callerSamples {
			if strings.HasPrefix(key, pfx) && strings.Count(key, ";") == strings.Count(prefix, ";")+1 {
				children = append(children, child{key, cnt})
			}
		}
		sort.Slice(children, func(i, j int) bool { return children[i].samples > children[j].samples })
		for _, c := range children {
			printCallers(c.key, depth+1, indent+1)
		}
	}

	sortedRoots := make([]string, 0, len(roots))
	for r := range roots {
		sortedRoots = append(sortedRoots, r)
	}
	sort.Strings(sortedRoots)
	for _, root := range sortedRoots {
		printCallers(root, 1, 0)
	}
}

// ---------------------------------------------------------------------------
// threads
// ---------------------------------------------------------------------------

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

	if top > 0 && top < len(ranked) {
		ranked = ranked[:top]
	}

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

// ---------------------------------------------------------------------------
// filter
// ---------------------------------------------------------------------------

func cmdFilter(sf *stackFile, method string, includeCallers bool) {
	for i := range sf.stacks {
		st := &sf.stacks[i]
		for j, fr := range st.frames {
			if matchesMethod(fr, method) {
				var outFrames []string
				if includeCallers {
					outFrames = st.frames
				} else {
					outFrames = st.frames[j:]
				}
				threadPrefix := ""
				if st.thread != "" {
					threadPrefix = fmt.Sprintf("[%s];", st.thread)
				}
				fmt.Printf("%s%s %d\n", threadPrefix, strings.Join(outFrames, ";"), st.count)
				break
			}
		}
	}
}

// ---------------------------------------------------------------------------
// events
// ---------------------------------------------------------------------------

func cmdEvents(path string) {
	counts, err := discoverEvents(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if len(counts) == 0 {
		fmt.Println("no supported events found")
		return
	}

	type entry struct {
		name    string
		samples int
	}
	var ranked []entry
	for name, cnt := range counts {
		ranked = append(ranked, entry{name, cnt})
	}
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].samples > ranked[j].samples })

	fmt.Printf("%-10s %9s\n", "EVENT", "SAMPLES")
	for _, e := range ranked {
		fmt.Printf("%-10s %9d\n", e.name, e.samples)
	}
}

// ---------------------------------------------------------------------------
// collapse
// ---------------------------------------------------------------------------

func cmdCollapse(sf *stackFile) {
	for i := range sf.stacks {
		st := &sf.stacks[i]
		threadPrefix := ""
		if st.thread != "" {
			threadPrefix = fmt.Sprintf("[%s];", st.thread)
		}
		fmt.Printf("%s%s %d\n", threadPrefix, strings.Join(st.frames, ";"), st.count)
	}
}

// ---------------------------------------------------------------------------
// diff
// ---------------------------------------------------------------------------

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

func truncate(n, top int) int {
	if top > 0 && top < n {
		return top
	}
	return n
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

// ---------------------------------------------------------------------------
// lines
// ---------------------------------------------------------------------------

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

func cmdLines(sf *stackFile, method string, top int, fqn bool) {
	ranked, hasMethod := computeLines(sf, method, top, fqn)
	if ranked == nil {
		if hasMethod {
			fmt.Fprintf(os.Stderr, "error: no line info for frames matching '%s'\n", method)
			os.Exit(1)
		}
		fmt.Printf("no frames matching '%s'\n", method)
		return
	}

	fmt.Printf("%-40s %9s %7s\n", "SOURCE:LINE", "SAMPLES", "PCT")
	for _, e := range ranked {
		pct := 100.0 * float64(e.samples) / float64(sf.totalSamples)
		loc := fmt.Sprintf("%s:%d", e.name, e.line)
		fmt.Printf("%-40s %9d %6.1f%%\n", loc, e.samples, pct)
	}
}

// ---------------------------------------------------------------------------
// info
// ---------------------------------------------------------------------------

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
		shown := ranked
		if topThreads > 0 && topThreads < len(shown) {
			shown = shown[:topThreads]
		}
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
		// Self-time section
		selfShown := hot
		if topMethods > 0 && topMethods < len(selfShown) {
			selfShown = selfShown[:topMethods]
		}
		fmt.Printf("=== RANK BY SELF TIME (top %d) ===\n", len(selfShown))
		fmt.Printf("%-50s %7s %7s %9s\n", "METHOD", "SELF%", "TOTAL%", "SAMPLES")
		for _, e := range selfShown {
			sp := 100.0 * float64(e.selfCount) / float64(sf.totalSamples)
			tp := 100.0 * float64(e.totalCount) / float64(sf.totalSamples)
			fmt.Printf("%-50s %6.1f%% %6.1f%% %9d\n", e.name, sp, tp, e.selfCount)
		}

		// Total-time section
		totalShown := make([]hotEntry, len(hot))
		copy(totalShown, hot)
		sort.Slice(totalShown, func(i, j int) bool { return totalShown[i].totalCount > totalShown[j].totalCount })
		if topMethods > 0 && topMethods < len(totalShown) {
			totalShown = totalShown[:topMethods]
		}
		fmt.Println()
		fmt.Printf("=== RANK BY TOTAL TIME (top %d) ===\n", len(totalShown))
		fmt.Printf("%-50s %7s %7s %9s\n", "METHOD", "SELF%", "TOTAL%", "SAMPLES")
		for _, e := range totalShown {
			sp := 100.0 * float64(e.selfCount) / float64(sf.totalSamples)
			tp := 100.0 * float64(e.totalCount) / float64(sf.totalSamples)
			fmt.Printf("%-50s %6.1f%% %6.1f%% %9d\n", e.name, sp, tp, e.totalCount)
		}
	}

	fmt.Printf("\nTotal samples: %d\n", sf.totalSamples)

	// === DRILL-DOWN ===
	if expand > 0 && len(hot) > 0 {
		drillDown := hot
		if expand < len(drillDown) {
			drillDown = drillDown[:expand]
		}
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
