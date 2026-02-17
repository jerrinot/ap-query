package main

import "os"

const treeHelp = `Usage: ap-query tree [flags] <file>

Call tree descending from a method (shows all roots if -m omitted).

Flags:
  -m METHOD, --method METHOD   Substring match on method name.
  --depth N                    Max depth (default: 4).
  --min-pct F                  Hide nodes below this % (default: 1.0).
  --hide REGEX                 Remove matching frames before analysis.
  --event TYPE, -e TYPE        Event type (default: cpu).
  -t THREAD                    Filter to threads matching substring.
  --from DURATION              Start of time window (JFR only).
  --to DURATION                End of time window (JFR only).
  --no-idle                    Remove idle leaf frames.

Examples:
  ap-query tree profile.jfr -m HashMap.resize --depth 6
  ap-query tree profile.jfr --hide "Thread\.(run|start)" --depth 6
`

func cmdTree(sf *stackFile, method string, maxDepth int, minPct float64) {
	if sf.totalSamples == 0 {
		return
	}
	pt := buildTreePT(sf, method)
	pt.fprintTree(os.Stdout, treeDisplayMethod(method), maxDepth, minPct, true)
}
