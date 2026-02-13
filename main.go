// ap-query: analyze async-profiler profiles (JFR or collapsed text).
//
// Usage:
//
//	ap-query <command> [flags] <file>
//
// Input: .jfr/.jfr.gz files are parsed as JFR binary; all other files and
// stdin (-) are parsed as collapsed text.
//
// Commands: hot, tree, callers, threads, filter, events, collapse, diff, lines, info
package main

import (
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------------------
// Name helpers (mirror the Python ap_query helpers)
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
// Subcommand: hot
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
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].selfCount > ranked[j].selfCount })
	return ranked
}

func cmdHot(sf *stackFile, top int, fqn bool, assertBelow float64) {
	ranked := computeHot(sf, fqn)
	if len(ranked) == 0 {
		return
	}
	if top > 0 && top < len(ranked) {
		ranked = ranked[:top]
	}

	fmt.Printf("%-50s %7s %7s %9s\n", "METHOD", "SELF%", "TOTAL%", "SAMPLES")
	for _, e := range ranked {
		sp := 100.0 * float64(e.selfCount) / float64(sf.totalSamples)
		tp := 100.0 * float64(e.totalCount) / float64(sf.totalSamples)
		fmt.Printf("%-50s %6.1f%% %6.1f%% %9d\n", e.name, sp, tp, e.selfCount)
	}

	if assertBelow > 0 && len(ranked) > 0 {
		selfPct := 100.0 * float64(ranked[0].selfCount) / float64(sf.totalSamples)
		if selfPct >= assertBelow {
			fmt.Fprintf(os.Stderr, "ASSERT FAILED: %s self=%.1f%% >= threshold %.1f%%\n", ranked[0].name, selfPct, assertBelow)
			os.Exit(1)
		}
	}
}

// ---------------------------------------------------------------------------
// Subcommand: tree
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
// Subcommand: callers
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
// Subcommand: threads
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
// Subcommand: filter (outputs collapsed text for piping)
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
// Subcommand: events
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
// Subcommand: collapse
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
// Subcommand: diff
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

func cmdDiff(before, after *stackFile, minDelta float64, fqn bool) {
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
// Subcommand: lines
// ---------------------------------------------------------------------------

func cmdLines(sf *stackFile, method string, fqn bool) {
	if sf.totalSamples == 0 {
		return
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
		hasMethod := false
		for i := range sf.stacks {
			for _, fr := range sf.stacks[i].frames {
				if matchesMethod(fr, method) {
					hasMethod = true
					break
				}
			}
			if hasMethod {
				break
			}
		}
		if hasMethod {
			fmt.Fprintf(os.Stderr, "error: no line info for frames matching '%s'\n", method)
			os.Exit(1)
		}
		fmt.Printf("no frames matching '%s'\n", method)
		return
	}

	type lineEntry struct {
		name    string
		line    uint32
		samples int
	}
	var ranked []lineEntry
	for k, c := range lineCounts {
		ranked = append(ranked, lineEntry{k.name, k.line, c})
	}
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].samples > ranked[j].samples })

	fmt.Printf("%-40s %9s %7s\n", "SOURCE:LINE", "SAMPLES", "PCT")
	for _, e := range ranked {
		pct := 100.0 * float64(e.samples) / float64(sf.totalSamples)
		loc := fmt.Sprintf("%s:%d", e.name, e.line)
		fmt.Printf("%-40s %9d %6.1f%%\n", loc, e.samples, pct)
	}
}

// ---------------------------------------------------------------------------
// Subcommand: info
// ---------------------------------------------------------------------------

func cmdInfo(path string, sf *stackFile, eventType string, isJFR bool) {
	// === EVENTS === (JFR only)
	if isJFR {
		counts, err := discoverEvents(path)
		if err == nil && len(counts) > 0 {
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
		fmt.Println("=== THREADS (top 5) ===")
		top5 := ranked
		if len(top5) > 5 {
			top5 = top5[:5]
		}
		for _, e := range top5 {
			pct := 100.0 * float64(e.samples) / float64(sf.totalSamples)
			fmt.Printf("%-30s %9d %6.1f%%\n", e.name, e.samples, pct)
		}
		fmt.Println()
	}

	// === HOT METHODS ===
	hot := computeHot(sf, false)
	if len(hot) > 0 {
		fmt.Println("=== HOT METHODS (top 10) ===")
		top10 := hot
		if len(top10) > 10 {
			top10 = top10[:10]
		}
		fmt.Printf("%-50s %7s %7s %9s\n", "METHOD", "SELF%", "TOTAL%", "SAMPLES")
		for _, e := range top10 {
			sp := 100.0 * float64(e.selfCount) / float64(sf.totalSamples)
			tp := 100.0 * float64(e.totalCount) / float64(sf.totalSamples)
			fmt.Printf("%-50s %6.1f%% %6.1f%% %9d\n", e.name, sp, tp, e.selfCount)
		}
	}

	fmt.Printf("\nTotal samples: %d\n", sf.totalSamples)
}

// ---------------------------------------------------------------------------
// CLI
// ---------------------------------------------------------------------------

func usage() {
	fmt.Fprintf(os.Stderr, `ap-query: analyze async-profiler profiles (JFR or collapsed text)

Usage:
  ap-query <command> [flags] <file>

Input auto-detection:
  .jfr / .jfr.gz  →  JFR binary (supports --event selection)
  everything else  →  collapsed-stack text (one "frames count" per line)
  -                →  collapsed text from stdin

Commands:
  info      One-shot triage: events (JFR only), top threads, top hot methods.
  hot       Rank methods by self-time (leaf cost).
  tree      Call tree descending from a method (-m required).
  callers   Callers ascending to a method (-m required).
  lines     Source-line breakdown inside a method (-m required).
  threads   Thread sample distribution.
  diff      Compare two profiles: shows REGRESSION / IMPROVEMENT / NEW / GONE.
  collapse  Emit collapsed-stack text (useful for piping JFR output).
  filter    Output stacks passing through a method (-m required).
  events    List event types in a JFR file (JFR only).

Global flags:
  --event TYPE, -e TYPE   Event type: cpu (default), wall, alloc, lock. JFR only.
  -t THREAD               Filter stacks to threads matching substring.

Command-specific flags:
  -m METHOD, --method METHOD   Substring match against method names.
  --top N                      Limit output rows (default: 10 for hot, unlimited for threads).
  --depth N                    Max tree/callers depth (default: 4).
  --min-pct F                  Hide tree/callers nodes below this %% (default: 1.0).
  --min-delta F                Hide diff entries below this %% change (default: 0.5).
  --fqn                        Show fully-qualified names instead of Class.method.
  --assert-below F             Exit 1 if top method self%% >= F (for CI gates).
  --include-callers            Include caller frames in filter output.

Examples:
  ap-query info profile.jfr
  ap-query hot profile.jfr --event cpu --top 20
  ap-query tree profile.jfr -m HashMap.resize --depth 6
  ap-query callers profile.jfr -m HashMap.resize
  ap-query lines profile.jfr -m HashMap.resize
  ap-query hot profile.jfr -t "http-nio" --assert-below 15.0
  ap-query diff before.jfr after.jfr --min-delta 0.5
  ap-query collapse profile.jfr --event wall | ap-query hot -
  echo "A;B;C 10" | ap-query hot -
`)
	os.Exit(2)
}

// Simple flag parser that works with subcommands.
type flags struct {
	args  []string // positional
	vals  map[string]string
	bools map[string]bool
}

func parseFlags(args []string) flags {
	f := flags{vals: make(map[string]string), bools: make(map[string]bool)}
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "--" {
			f.args = append(f.args, args[i+1:]...)
			break
		}
		if strings.HasPrefix(a, "--") || (strings.HasPrefix(a, "-") && len(a) == 2) {
			key := strings.TrimLeft(a, "-")
			// Known boolean flags
			switch key {
			case "fqn", "include-callers":
				f.bools[key] = true
				i++
				continue
			}
			// Value flags
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				f.vals[key] = args[i+1]
				i += 2
				continue
			}
			// Flag with no value — treat as boolean
			f.bools[key] = true
			i++
			continue
		}
		f.args = append(f.args, a)
		i++
	}
	return f
}

func (f *flags) str(keys ...string) string {
	for _, k := range keys {
		if v, ok := f.vals[k]; ok {
			return v
		}
	}
	return ""
}

func (f *flags) intVal(keys []string, def int) int {
	for _, k := range keys {
		if v, ok := f.vals[k]; ok {
			n, err := strconv.Atoi(v)
			if err != nil {
				fmt.Fprintf(os.Stderr, "error: invalid integer value for --%s: %q\n", k, v)
				os.Exit(2)
			}
			return n
		}
	}
	return def
}

func (f *flags) floatVal(keys []string, def float64) float64 {
	for _, k := range keys {
		if v, ok := f.vals[k]; ok {
			n, err := strconv.ParseFloat(v, 64)
			if err != nil {
				fmt.Fprintf(os.Stderr, "error: invalid numeric value for --%s: %q\n", k, v)
				os.Exit(2)
			}
			return n
		}
	}
	return def
}

func (f *flags) boolean(keys ...string) bool {
	for _, k := range keys {
		if f.bools[k] {
			return true
		}
	}
	return false
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}

	cmd := os.Args[1]
	if cmd == "-h" || cmd == "--help" || cmd == "help" {
		usage()
	}
	f := parseFlags(os.Args[2:])

	if cmd == "events" {
		if len(f.args) < 1 {
			usage()
		}
		if !isJFRPath(f.args[0]) {
			fmt.Fprintln(os.Stderr, "error: events command requires a JFR file")
			os.Exit(2)
		}
		cmdEvents(f.args[0])
		return
	}

	eventType := f.str("event", "e")
	if eventType == "" {
		eventType = "cpu"
	}
	switch eventType {
	case "cpu", "wall", "alloc", "lock":
	default:
		fmt.Fprintf(os.Stderr, "error: unknown event type %q (valid: cpu, wall, alloc, lock)\n", eventType)
		os.Exit(2)
	}

	// diff requires two positional args
	if cmd == "diff" {
		if len(f.args) < 2 {
			fmt.Fprintln(os.Stderr, "error: diff requires two files")
			os.Exit(2)
		}
		thread := f.str("t", "thread")
		fqn := f.boolean("fqn")
		minDelta := f.floatVal([]string{"min-delta"}, 0.5)

		before, _, err := openInput(f.args[0], eventType)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		after, _, err := openInput(f.args[1], eventType)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		if thread != "" {
			before = before.filterByThread(thread)
			after = after.filterByThread(thread)
		}
		cmdDiff(before, after, minDelta, fqn)
		return
	}

	if len(f.args) < 1 {
		usage()
	}
	path := f.args[0]
	thread := f.str("t", "thread")

	sf, isJFR, err := openInput(path, eventType)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if thread != "" {
		sf = sf.filterByThread(thread)
	}

	switch cmd {
	case "hot":
		top := f.intVal([]string{"top"}, 10)
		fqn := f.boolean("fqn")
		assertBelow := f.floatVal([]string{"assert-below"}, 0)
		cmdHot(sf, top, fqn, assertBelow)

	case "tree":
		method := f.str("m", "method")
		if method == "" {
			fmt.Fprintln(os.Stderr, "error: -m/--method required")
			os.Exit(2)
		}
		depth := f.intVal([]string{"depth"}, 4)
		minPct := f.floatVal([]string{"min-pct"}, 1.0)
		cmdTree(sf, method, depth, minPct)

	case "callers":
		method := f.str("m", "method")
		if method == "" {
			fmt.Fprintln(os.Stderr, "error: -m/--method required")
			os.Exit(2)
		}
		depth := f.intVal([]string{"depth"}, 4)
		minPct := f.floatVal([]string{"min-pct"}, 1.0)
		cmdCallers(sf, method, depth, minPct)

	case "threads":
		top := f.intVal([]string{"top"}, 0)
		cmdThreads(sf, top)

	case "filter":
		method := f.str("m", "method")
		if method == "" {
			fmt.Fprintln(os.Stderr, "error: -m/--method required")
			os.Exit(2)
		}
		inclCallers := f.boolean("include-callers")
		cmdFilter(sf, method, inclCallers)

	case "collapse":
		cmdCollapse(sf)

	case "lines":
		method := f.str("m", "method")
		if method == "" {
			fmt.Fprintln(os.Stderr, "error: -m/--method required")
			os.Exit(2)
		}
		fqn := f.boolean("fqn")
		cmdLines(sf, method, fqn)

	case "info":
		cmdInfo(path, sf, eventType, isJFR)

	default:
		usage()
	}
}
