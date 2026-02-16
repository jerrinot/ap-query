package main

import "os"

func cmdCallers(sf *stackFile, method string, maxDepth int, minPct float64) {
	if sf.totalSamples == 0 {
		return
	}
	pt := buildCallersPT(sf, method)
	pt.fprintTree(os.Stdout, method, maxDepth, minPct, false)
}
