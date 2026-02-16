package main

import "os"

func cmdTree(sf *stackFile, method string, maxDepth int, minPct float64) {
	if sf.totalSamples == 0 {
		return
	}
	pt := buildTreePT(sf, method)
	pt.fprintTree(os.Stdout, treeDisplayMethod(method), maxDepth, minPct, true)
}
