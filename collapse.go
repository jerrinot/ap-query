package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

func newCollapseCmd() *cobra.Command {
	var shared sharedFlags
	cmd := &cobra.Command{
		Use:   "collapse <file>",
		Short: "Emit collapsed-stack text (useful for piping JFR output)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			pctx, err := preprocessProfile(shared.toOpts(args[0], "collapse"))
			if err != nil {
				return err
			}
			cmdCollapse(pctx.sf)
			return nil
		},
	}
	shared.register(cmd)
	return cmd
}

func cmdCollapse(sf *stackFile) {
	for i := range sf.stacks {
		st := &sf.stacks[i]
		tp := threadPrefix(st.thread)
		fmt.Printf("%s%s %d\n", tp, strings.Join(st.frames, ";"), st.count)
	}
}
