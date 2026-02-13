package main

import (
	"fmt"
	"strings"
)

func cmdCollapse(sf *stackFile) {
	for i := range sf.stacks {
		st := &sf.stacks[i]
		threadPrefix := ""
		if st.thread != "" {
			threadPrefix = fmt.Sprintf("[%s];", st.thread)
		}
		fmt.Printf("%s%s %d\n", threadPrefix, strings.Join(st.frames, ";"), st.count)
	}
}
