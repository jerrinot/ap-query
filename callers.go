package main

import "os"

const callersHelp = `Usage: ap-query callers [flags] <file>

Callers ascending to a method (-m required).

Flags:
  -m METHOD, --method METHOD   Substring match on method name (required).
  --depth N                    Max depth (default: 4).
  --min-pct F                  Hide nodes below this % (default: 1.0).
  --hide REGEX                 Remove matching frames before analysis.
  --event TYPE, -e TYPE        Event type (default: cpu).
  -t THREAD                    Filter to threads matching substring.
  --from DURATION              Start of time window (JFR only).
  --to DURATION                End of time window (JFR only).
  --no-idle                    Remove idle leaf frames.

Examples:
  ap-query callers profile.jfr -m HashMap.resize
  ap-query callers profile.jfr -m HashMap.resize --depth 6
`

func cmdCallers(sf *stackFile, method string, maxDepth int, minPct float64) {
	if sf.totalSamples == 0 {
		return
	}
	pt := buildCallersPT(sf, method)
	pt.fprintTree(os.Stdout, method, maxDepth, minPct, false)
}
