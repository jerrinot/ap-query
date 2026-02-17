package main

import (
	"fmt"
	"strings"
)

const filterHelp = `Usage: ap-query filter [flags] <file>

Output stacks passing through a method (-m required).

Flags:
  -m METHOD, --method METHOD   Substring match on method name (required).
  --include-callers            Include caller frames in output.
  --event TYPE, -e TYPE        Event type (default: cpu).
  -t THREAD                    Filter to threads matching substring.
  --from DURATION              Start of time window (JFR only).
  --to DURATION                End of time window (JFR only).
  --no-idle                    Remove idle leaf frames.

Examples:
  ap-query filter profile.jfr -m HashMap.resize
  ap-query filter profile.jfr -m HashMap.resize --include-callers
`

func cmdFilter(sf *stackFile, method string, includeCallers bool) {
	for i := range sf.stacks {
		st := &sf.stacks[i]
		for j, fr := range st.frames {
			if matchesMethod(fr, method) {
				var outFrames []string
				if includeCallers {
					outFrames = st.frames
				} else {
					outFrames = st.frames[j:]
				}
				tp := threadPrefix(st.thread)
				fmt.Printf("%s%s %d\n", tp, strings.Join(outFrames, ";"), st.count)
				break
			}
		}
	}
}
