package main

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

const traceHelp = `Usage: ap-query trace [flags] <file>

Hottest path from a method to leaf (-m required).

Flags:
  -m METHOD, --method METHOD   Substring match on method name (required).
  --min-pct F                  Hide nodes below this % (default: 0.5).
  --fqn                        Show fully-qualified names.
  --hide REGEX                 Remove matching frames before analysis.
  --event TYPE, -e TYPE        Event type (default: cpu).
  -t THREAD                    Filter to threads matching substring.
  --from DURATION              Start of time window (JFR only).
  --to DURATION                End of time window (JFR only).
  --no-idle                    Remove idle leaf frames.

Examples:
  ap-query trace profile.jfr -m HashMap.resize
  ap-query trace profile.jfr -m HashMap.resize --min-pct 0.5
`

func cmdTrace(sf *stackFile, method string, minPct float64, fqn bool) {
	writeTrace(os.Stdout, sf, method, minPct, fqn)
}

func writeTrace(w io.Writer, sf *stackFile, method string, minPct float64, fqn bool) {
	if sf.totalSamples == 0 {
		return
	}

	pt := aggregatePaths(sf, method, func(frames []string, j int) []string {
		path := make([]string, len(frames)-j)
		for k := j; k < len(frames); k++ {
			if fqn {
				path[k-j] = displayName(frames[k], true)
			} else {
				path[k-j] = shortName(frames[k])
			}
		}
		return path
	})

	if len(pt.samples) == 0 {
		fmt.Fprintf(w, "no frames matching '%s'\n", method)
		return
	}

	if len(pt.matchedNames) > 1 {
		names := make([]string, 0, len(pt.matchedNames))
		for n := range pt.matchedNames {
			names = append(names, n)
		}
		sort.Strings(names)
		fmt.Fprintf(w, "# matched %d methods: %s\n", len(pt.matchedNames), strings.Join(names, ", "))
	}

	// Collect roots sorted by sample count descending, then name for stability.
	type rootEntry struct {
		name    string
		samples int
	}
	roots := make(map[string]bool)
	for key := range pt.samples {
		roots[strings.SplitN(key, ";", 2)[0]] = true
	}
	var sortedRoots []rootEntry
	for r := range roots {
		sortedRoots = append(sortedRoots, rootEntry{r, pt.samples[r]})
	}
	sort.Slice(sortedRoots, func(i, j int) bool {
		if sortedRoots[i].samples != sortedRoots[j].samples {
			return sortedRoots[i].samples > sortedRoots[j].samples
		}
		return sortedRoots[i].name < sortedRoots[j].name
	})

	for _, root := range sortedRoots {
		ftraceHottestPath(w, pt, root.name, minPct)
	}
}

type traceChild struct {
	key     string
	name    string
	samples int
}

// childrenAboveMinPct returns direct children of prefix sorted by samples
// descending, with ties broken by name ascending.
func childrenAboveMinPct(pt *pathTree, prefix string, minPct float64) []traceChild {
	var children []traceChild
	pfx := prefix + ";"
	depth := strings.Count(prefix, ";") + 1
	for key, cnt := range pt.samples {
		if strings.HasPrefix(key, pfx) && strings.Count(key, ";") == depth {
			childPct := pctOf(cnt, pt.totalSamples)
			if childPct >= minPct {
				parts := strings.Split(key, ";")
				children = append(children, traceChild{key, parts[len(parts)-1], cnt})
			}
		}
	}
	sort.Slice(children, func(i, j int) bool {
		if children[i].samples != children[j].samples {
			return children[i].samples > children[j].samples
		}
		return children[i].name < children[j].name
	})
	return children
}

// ftraceHottestPath walks from root following the hottest child at each level.
func ftraceHottestPath(w io.Writer, pt *pathTree, rootKey string, minPct float64) {
	prefix := rootKey
	indent := 0
	// siblingAnnotation is computed when we pick a child, then printed
	// on that child's line (the next iteration).
	siblingAnnotation := ""

	for {
		samples := pt.samples[prefix]
		pct := pctOf(samples, pt.totalSamples)
		if pct < minPct {
			break
		}

		parts := strings.Split(prefix, ";")
		name := parts[len(parts)-1]
		pad := strings.Repeat("  ", indent)

		children := childrenAboveMinPct(pt, prefix, minPct)
		isLeaf := len(children) == 0

		// Build line.
		line := fmt.Sprintf("%s[%.1f%%] %s", pad, pct, name)

		// Append sibling annotation (carried from previous iteration).
		line += siblingAnnotation

		// Leaf: append self-time annotation.
		if isLeaf {
			selfCt := pt.selfSamples[prefix]
			selfPct := pctOf(selfCt, pt.totalSamples)
			if selfCt > 0 && selfPct >= minPct {
				line += fmt.Sprintf("  ← self=%.1f%%", selfPct)
			}
			fmt.Fprintln(w, line)
			fmt.Fprintf(w, "Hottest leaf: %s (self=%.1f%%)\n", name, selfPct)
			break
		}

		fmt.Fprintln(w, line)

		// Pick hottest child, compute sibling annotation for next iteration.
		hottest := children[0]
		siblingAnnotation = ""
		if len(children) > 1 {
			next := children[1]
			nextPct := pctOf(next.samples, pt.totalSamples)
			n := len(children) - 1
			word := "siblings"
			if n == 1 {
				word = "sibling"
			}
			siblingAnnotation = fmt.Sprintf("  (+%d %s, next: %.1f%% %s)", n, word, nextPct, next.name)
		}

		prefix = hottest.key
		indent++
	}
}

// computeTraceString returns the hottest-path trace for the given method as a string.
func computeTraceString(sf *stackFile, method string, minPct float64, fqn bool) string {
	var buf strings.Builder
	writeTrace(&buf, sf, method, minPct, fqn)
	return strings.TrimRight(buf.String(), "\n")
}
