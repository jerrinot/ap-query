package main

import (
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
)

func shortName(frame string) string {
	base := strings.ReplaceAll(frame, "/", ".")

	// Native frames from shared libraries: "libc.so.6.__sched_yield" → "__sched_yield"
	if soIdx := strings.Index(base, ".so."); soIdx >= 0 {
		after := base[soIdx+4:]
		// Skip version components (all-digit parts like "6" in libc.so.6)
		for {
			dot := strings.IndexByte(after, '.')
			if dot < 0 {
				break
			}
			allDigits := true
			for _, c := range after[:dot] {
				if c < '0' || c > '9' {
					allDigits = false
					break
				}
			}
			if !allDigits {
				break
			}
			after = after[dot+1:]
		}
		if after != "" {
			return after
		}
	}

	// Java frames: "com/example/App.process" → "App.process"
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
	pattern = strings.ReplaceAll(pattern, "/", ".")
	return strings.Contains(normalized, pattern) || strings.Contains(shortName(frame), pattern)
}

func matchesHide(frame string, re *regexp.Regexp) bool {
	normalized := strings.ReplaceAll(frame, "/", ".")
	return re.MatchString(normalized) || re.MatchString(shortName(frame))
}

// idleJavaPatterns match Java idle frames by exact class name and method
// prefix on the short name (e.g. "Object.wait", "Object.wait0").
var idleJavaPatterns = []struct{ class, method string }{
	{"Thread", "sleep"},
	{"Object", "wait"},
	{"LockSupport", "park"},
	{"Unsafe", "park"},
}

// idleNativePatterns match native idle frames by prefix on the short name.
var idleNativePatterns = []string{
	"__futex",
	"__sched_yield",
	"epoll_wait",
	"pthread_cond_wait",
	"pthread_cond_timedwait",
}

func isIdleLeaf(frame string) bool {
	short := shortName(frame)

	// Java frames: exact class + method prefix (avoids matching
	// e.g. TransactionObject.waitFor against Object.wait).
	if dot := strings.LastIndexByte(short, '.'); dot >= 0 {
		class := short[:dot]
		method := short[dot+1:]
		for _, p := range idleJavaPatterns {
			if class == p.class && strings.HasPrefix(method, p.method) {
				return true
			}
		}
	}

	// Native frames (no dot): prefix match.
	for _, p := range idleNativePatterns {
		if strings.HasPrefix(short, p) {
			return true
		}
	}
	return false
}

func truncate(n, top int) int {
	if top > 0 && top < n {
		return top
	}
	return n
}

func pctOf(n, total int) float64 {
	return 100.0 * float64(n) / float64(total)
}

func threadPrefix(thread string) string {
	if thread == "" {
		return ""
	}
	return fmt.Sprintf("[%s];", thread)
}

// uniqueFrames returns deduplicated raw frames and whether any contains '$'.
func uniqueFrames(sf *stackFile) (frames []string, hasDollar bool) {
	seen := make(map[string]bool)
	for i := range sf.stacks {
		for _, fr := range sf.stacks[i].frames {
			if seen[fr] {
				continue
			}
			seen[fr] = true
			frames = append(frames, fr)
			if !hasDollar && strings.ContainsRune(fr, '$') {
				hasDollar = true
			}
		}
	}
	return
}

// levenshtein returns the edit distance between two strings.
func levenshtein(a, b string) int {
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}
	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min(curr[j-1]+1, min(prev[j]+1, prev[j-1]+cost))
		}
		prev, curr = curr, prev
	}
	return prev[len(b)]
}

// fuzzyScore returns how well lowered pattern matches name: 0 for
// case-insensitive substring, positive edit distance for segment-level typo
// matches, or -1 if nothing is close enough. pattern must already be
// lowercased and dot-normalized. Segments are split on '.' and '$'.
func fuzzyScore(name, pattern string, threshold int) int {
	ln := strings.ToLower(name)
	if strings.Contains(ln, pattern) {
		return 0
	}
	best := threshold + 1

	// Full-name comparison: catches FQN patterns with typos
	// (e.g. "com.example.appp.process" vs "com.example.app.process").
	// The length-diff gate keeps this cheap for short patterns.
	if diff := abs(len(pattern) - len(ln)); diff <= threshold {
		if d := levenshtein(pattern, ln); d < best {
			best = d
		}
	}

	// Segment-level comparison: catches short patterns with typos
	// (e.g. "rezise" vs segment "resize").
	for _, seg := range strings.FieldsFunc(ln, func(r rune) bool { return r == '.' || r == '$' }) {
		if diff := abs(len(pattern) - len(seg)); diff > threshold {
			continue
		}
		if d := levenshtein(pattern, seg); d < best {
			best = d
		}
	}
	if best <= threshold {
		return best
	}
	return -1
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// suggestMethods returns up to 5 method names from sf that fuzzy-match pattern,
// sorted by match quality then alphabetically. When multiple distinct FQN
// methods share the same short name, FQN forms are shown to disambiguate.
// hasDollar reports whether any frame in sf contains '$'.
func suggestMethods(sf *stackFile, pattern string) ([]string, bool) {
	frames, hasDollar := uniqueFrames(sf)
	lp := strings.ToLower(strings.ReplaceAll(pattern, "/", "."))
	threshold := max(1, min(len(pattern)/3, 3))

	type match struct {
		fqn   string
		short string
		score int
	}
	// Key by FQN to preserve distinct methods for disambiguation.
	fqnScores := make(map[string]match)
	for _, fr := range frames {
		short := shortName(fr)
		fqn := strings.ReplaceAll(fr, "/", ".")

		// Take the better score of short name and FQN form.
		score := fuzzyScore(short, lp, threshold)
		if s := fuzzyScore(fqn, lp, threshold); s >= 0 && (score < 0 || s < score) {
			score = s
		}
		if score < 0 {
			continue
		}
		if prev, ok := fqnScores[fqn]; ok && prev.score <= score {
			continue
		}
		fqnScores[fqn] = match{fqn, short, score}
	}

	// Detect short-name collisions: count distinct FQNs per short name.
	shortCount := make(map[string]int)
	for _, m := range fqnScores {
		shortCount[m.short]++
	}

	type entry struct {
		name  string
		score int
	}
	ranked := make([]entry, 0, len(fqnScores))
	for _, m := range fqnScores {
		name := m.short
		if shortCount[m.short] > 1 {
			name = m.fqn
		}
		ranked = append(ranked, entry{name, m.score})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score < ranked[j].score
		}
		return ranked[i].name < ranked[j].name
	})
	if len(ranked) > 5 {
		ranked = ranked[:5]
	}
	result := make([]string, len(ranked))
	for i, e := range ranked {
		result[i] = e.name
	}
	return result, hasDollar
}

// noMatchMessage prints a "no stacks matching" message to w, with optional
// fuzzy suggestions and shell $-expansion hints. sf may be nil.
func noMatchMessage(w io.Writer, sf *stackFile, method string) {
	fmt.Fprintf(w, "no stacks matching '%s'\n", method)
	if sf == nil {
		return
	}
	suggestions, hasDollar := suggestMethods(sf, method)
	if len(suggestions) > 0 {
		fmt.Fprintf(w, "  similar: %s\n", strings.Join(suggestions, ", "))
	}
	if strings.ContainsRune(method, '$') {
		fmt.Fprintln(w, "  hint: use single quotes to prevent shell $-expansion (e.g. -m 'Class$Inner.method')")
	} else if hasDollar {
		fmt.Fprintln(w, "  hint: profile has inner classes ($) — if your pattern lost a $, use single quotes")
	}
}
