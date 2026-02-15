package main

import (
	"fmt"
	"strings"
)

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
