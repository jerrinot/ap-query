package main

func cmdCallers(sf *stackFile, method string, maxDepth int, minPct float64) {
	if sf.totalSamples == 0 {
		return
	}
	pt := aggregatePaths(sf, method, func(frames []string, j int) []string {
		// matched frame → root (reversed)
		path := make([]string, j+1)
		for k := 0; k <= j; k++ {
			path[j-k] = shortName(frames[k])
		}
		return path
	})
	pt.printTree(method, maxDepth, minPct, false)
}
