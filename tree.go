package main

func cmdTree(sf *stackFile, method string, maxDepth int, minPct float64) {
	if sf.totalSamples == 0 {
		return
	}

	var pt *pathTree
	if method == "" {
		// When no method specified, build tree from root of all stacks
		pt = aggregateFromRoot(sf)
	} else {
		// When method specified, build tree descending from matched method
		pt = aggregatePaths(sf, method, func(frames []string, j int) []string {
			path := make([]string, len(frames)-j)
			for k := j; k < len(frames); k++ {
				path[k-j] = shortName(frames[k])
			}
			return path
		})
	}

	displayMethod := method
	if displayMethod == "" {
		displayMethod = "(all)"
	}
	pt.printTree(displayMethod, maxDepth, minPct, true)
}
