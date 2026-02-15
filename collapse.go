package main

import (
	"fmt"
	"strings"
)

func cmdCollapse(sf *stackFile) {
	for i := range sf.stacks {
		st := &sf.stacks[i]
		tp := threadPrefix(st.thread)
		fmt.Printf("%s%s %d\n", tp, strings.Join(st.frames, ";"), st.count)
	}
}
