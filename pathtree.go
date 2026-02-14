package main

import (
	"fmt"
	"sort"
	"strings"
)

// pathTree holds path-aggregated sample counts for tree/callers display.
type pathTree struct {
	samples      map[string]int
	selfSamples  map[string]int
	matchedNames map[string]bool
	totalSamples int
}

// aggregateFromRoot builds a path tree starting from the root of all stacks.
// Used when no specific method is specified for the tree command.
func aggregateFromRoot(sf *stackFile) *pathTree {
	pt := &pathTree{
		samples:      make(map[string]int),
		selfSamples:  make(map[string]int),
		matchedNames: make(map[string]bool),
		totalSamples: sf.totalSamples,
	}

	for i := range sf.stacks {
		st := &sf.stacks[i]
		// Build path from root (index 0) to leaf
		for depth := 1; depth <= len(st.frames); depth++ {
			path := make([]string, depth)
			for j := 0; j < depth; j++ {
				path[j] = shortName(st.frames[j])
			}
			key := strings.Join(path, ";")
			pt.samples[key] += st.count
		}
		// Mark leaf as having self samples
		if len(st.frames) > 0 {
			fullPath := make([]string, len(st.frames))
			for j := 0; j < len(st.frames); j++ {
				fullPath[j] = shortName(st.frames[j])
			}
			leafKey := strings.Join(fullPath, ";")
			pt.selfSamples[leafKey] += st.count
		}
	}
	return pt
}

// aggregatePaths walks every stack in sf, finds frames matching method,
// and calls extract to get the path to aggregate. extract receives the
// stack's frames slice and the index of the matched frame.
func aggregatePaths(sf *stackFile, method string, extract func(frames []string, matchIdx int) []string) *pathTree {
	pt := &pathTree{
		samples:      make(map[string]int),
		selfSamples:  make(map[string]int),
		matchedNames: make(map[string]bool),
		totalSamples: sf.totalSamples,
	}

	for i := range sf.stacks {
		st := &sf.stacks[i]
		for j, fr := range st.frames {
			if matchesMethod(fr, method) {
				pt.matchedNames[shortName(fr)] = true
				path := extract(st.frames, j)
				for depth := 1; depth <= len(path); depth++ {
					key := strings.Join(path[:depth], ";")
					pt.samples[key] += st.count
				}
				leafKey := strings.Join(path, ";")
				pt.selfSamples[leafKey] += st.count
				break
			}
		}
	}
	return pt
}

// printTree prints the aggregated path tree. If showSelf is true,
// leaf nodes annotate their self-time percentage.
func (pt *pathTree) printTree(method string, maxDepth int, minPct float64, showSelf bool) {
	if len(pt.samples) == 0 {
		fmt.Printf("no frames matching '%s'\n", method)
		return
	}

	if len(pt.matchedNames) > 1 {
		names := make([]string, 0, len(pt.matchedNames))
		for n := range pt.matchedNames {
			names = append(names, n)
		}
		sort.Strings(names)
		fmt.Printf("# matched %d methods: %s\n", len(pt.matchedNames), strings.Join(names, ", "))
	}

	roots := make(map[string]bool)
	for key := range pt.samples {
		roots[strings.SplitN(key, ";", 2)[0]] = true
	}

	var walk func(prefix string, depth, indent int)
	walk = func(prefix string, depth, indent int) {
		samples := pt.samples[prefix]
		pct := 100.0 * float64(samples) / float64(pt.totalSamples)
		if pct < minPct {
			return
		}
		parts := strings.Split(prefix, ";")
		name := parts[len(parts)-1]
		pad := strings.Repeat("  ", indent)
		selfSuffix := ""
		if showSelf {
			if selfCt := pt.selfSamples[prefix]; selfCt > 0 {
				selfPct := 100.0 * float64(selfCt) / float64(pt.totalSamples)
				if selfPct >= minPct {
					selfSuffix = fmt.Sprintf("  ← self=%.1f%%", selfPct)
				}
			}
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
		for key, cnt := range pt.samples {
			if strings.HasPrefix(key, pfx) && strings.Count(key, ";") == strings.Count(prefix, ";")+1 {
				children = append(children, child{key, cnt})
			}
		}
		sort.Slice(children, func(i, j int) bool { return children[i].samples > children[j].samples })
		for _, c := range children {
			walk(c.key, depth+1, indent+1)
		}
	}

	sortedRoots := make([]string, 0, len(roots))
	for r := range roots {
		sortedRoots = append(sortedRoots, r)
	}
	sort.Strings(sortedRoots)
	for _, root := range sortedRoots {
		walk(root, 1, 0)
	}
}
