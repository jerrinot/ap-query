package main

import (
	"fmt"
	"sort"
)

const threadsHelp = `Usage: ap-query threads [flags] <file>

Thread sample distribution.

Flags:
  --top N                 Limit output rows (default: unlimited).
  --group                 Group threads by normalized name.
  --event TYPE, -e TYPE   Event type (default: cpu).
  -t THREAD               Filter to threads matching substring.
  --from DURATION         Start of time window (JFR only).
  --to DURATION           End of time window (JFR only).
  --no-idle               Remove idle leaf frames.

Examples:
  ap-query threads profile.jfr
  ap-query threads profile.jfr --group --top 10
`

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

// threadGroupName normalises a thread name for grouping by splitting on
// separators (-, _, #), dropping purely-numeric segments and trimming
// trailing digits from the remaining segments.
//
//	"pool-1-thread-2"       → "pool-thread"
//	"http-nio-8080-exec-1"  → "http-nio-exec"
//	"GC Thread#54"          → "GC Thread"
//	"CompilerThread0"       → "CompilerThread"
func threadGroupName(name string) string {
	type segment struct {
		sep  byte // separator preceding this segment (0 for first)
		text string
	}

	var segs []segment
	start := 0
	for i := 0; i <= len(name); i++ {
		if i == len(name) || name[i] == '-' || name[i] == '_' || name[i] == '#' {
			var sep byte
			if start > 0 {
				sep = name[start-1]
			}
			segs = append(segs, segment{sep, name[start:i]})
			start = i + 1
		}
	}

	// Drop all-digit and empty segments, trim trailing digits from the rest.
	var buf []byte
	any := false
	for _, s := range segs {
		if s.text == "" || isAllDigits(s.text) {
			continue
		}
		trimmed := trimTrailingDigits(s.text)
		if any && s.sep != 0 {
			buf = append(buf, s.sep)
		}
		buf = append(buf, trimmed...)
		any = true
	}

	if len(buf) == 0 {
		return name
	}
	return string(buf)
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// trimTrailingDigits removes trailing digits from s if the remaining prefix
// is at least 2 characters and ends with a letter.
func trimTrailingDigits(s string) string {
	i := len(s)
	for i > 0 && s[i-1] >= '0' && s[i-1] <= '9' {
		i--
	}
	if i == len(s) {
		return s // no trailing digits
	}
	if i >= 2 && isLetter(s[i-1]) {
		return s[:i]
	}
	return s // remaining part too short or doesn't end with letter
}

func isLetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

type threadGroupEntry struct {
	name    string
	threads int
	samples int
}

// assignGroups maps each original thread name to a group name.
// Normalisation is only applied when 2+ distinct thread names share the
// same normalised form; singletons keep their original name.
func assignGroups(entries []threadEntry) map[string]string {
	norm := make(map[string]string) // original → normalised
	count := make(map[string]int)   // normalised → distinct originals
	for _, e := range entries {
		if _, ok := norm[e.name]; ok {
			continue
		}
		g := threadGroupName(e.name)
		norm[e.name] = g
		count[g]++
	}
	result := make(map[string]string, len(norm))
	for orig, g := range norm {
		if count[g] >= 2 {
			result[orig] = g
		} else {
			result[orig] = orig
		}
	}
	return result
}

func groupThreadsWith(entries []threadEntry, assignments map[string]string) []threadGroupEntry {
	type acc struct {
		threads int
		samples int
	}
	m := make(map[string]*acc)
	for _, e := range entries {
		g := assignments[e.name]
		a := m[g]
		if a == nil {
			a = &acc{}
			m[g] = a
		}
		a.threads++
		a.samples += e.samples
	}
	groups := make([]threadGroupEntry, 0, len(m))
	for name, a := range m {
		groups = append(groups, threadGroupEntry{name, a.threads, a.samples})
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].samples > groups[j].samples })
	return groups
}

func groupThreads(entries []threadEntry) []threadGroupEntry {
	return groupThreadsWith(entries, assignGroups(entries))
}

func cmdThreads(sf *stackFile, top int, group bool) {
	ranked, noThread, hasThread := computeThreads(sf)
	if !hasThread {
		if sf.totalSamples > 0 {
			fmt.Println("no thread info in this file")
		}
		return
	}

	if group {
		groups := groupThreads(ranked)
		groups = groups[:truncate(len(groups), top)]
		fmt.Printf("%-30s %9s %7s\n", "GROUP", "SAMPLES", "PCT")
		for _, g := range groups {
			label := g.name
			if g.threads > 1 {
				label = fmt.Sprintf("%s (%d threads)", g.name, g.threads)
			}
			pct := pctOf(g.samples, sf.totalSamples)
			fmt.Printf("%-30s %9d %6.1f%%\n", label, g.samples, pct)
		}
		if noThread > 0 {
			pct := pctOf(noThread, sf.totalSamples)
			fmt.Printf("%-30s %9d %6.1f%%\n", "(no thread info)", noThread, pct)
		}
		return
	}

	ranked = ranked[:truncate(len(ranked), top)]

	fmt.Printf("%-30s %9s %7s\n", "THREAD", "SAMPLES", "PCT")
	for _, e := range ranked {
		pct := pctOf(e.samples, sf.totalSamples)
		fmt.Printf("%-30s %9d %6.1f%%\n", e.name, e.samples, pct)
	}
	if noThread > 0 {
		pct := pctOf(noThread, sf.totalSamples)
		fmt.Printf("%-30s %9d %6.1f%%\n", "(no thread info)", noThread, pct)
	}
}
