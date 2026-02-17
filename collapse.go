package main

import (
	"fmt"
	"strings"
)

const collapseHelp = `Usage: ap-query collapse [flags] <file>

Emit collapsed-stack text (useful for piping JFR output).

Flags:
  --event TYPE, -e TYPE   Event type (default: cpu).
  -t THREAD               Filter to threads matching substring.
  --from DURATION         Start of time window (JFR only).
  --to DURATION           End of time window (JFR only).
  --no-idle               Remove idle leaf frames.

Examples:
  ap-query collapse profile.jfr --event wall | ap-query hot -
`

func cmdCollapse(sf *stackFile) {
	for i := range sf.stacks {
		st := &sf.stacks[i]
		tp := threadPrefix(st.thread)
		fmt.Printf("%s%s %d\n", tp, strings.Join(st.frames, ";"), st.count)
	}
}
