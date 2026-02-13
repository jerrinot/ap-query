package main

func cmdTree(sf *stackFile, method string, maxDepth int, minPct float64) {
	if sf.totalSamples == 0 {
		return
	}
	pt := aggregatePaths(sf, method, func(frames []string, j int) []string {
		path := make([]string, len(frames)-j)
		for k := j; k < len(frames); k++ {
			path[k-j] = shortName(frames[k])
		}
		return path
	})
	pt.printTree(method, maxDepth, minPct, true)
}
