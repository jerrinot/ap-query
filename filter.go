package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

func newFilterCmd() *cobra.Command {
	var shared sharedFlags
	var method string
	var inclCallers bool
	cmd := &cobra.Command{
		Use:   "filter <file>",
		Short: "Output stacks passing through a method (-m required)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if method == "" {
				return fmt.Errorf("-m/--method required")
			}
			pctx, err := preprocessProfile(shared.toOpts(args[0], "filter"))
			if err != nil {
				return err
			}
			cmdFilter(pctx.sf, method, inclCallers)
			return nil
		},
	}
	shared.register(cmd)
	cmd.Flags().StringVarP(&method, "method", "m", "", "Substring match on method name (required)")
	cmd.Flags().BoolVar(&inclCallers, "include-callers", false, "Include caller frames in output")
	return cmd
}

func cmdFilter(sf *stackFile, method string, includeCallers bool) {
	if sf.totalSamples == 0 {
		fmt.Fprintln(os.Stdout, "no samples (empty profile or all filtered out)")
		return
	}
	matched := 0
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
				matched++
				break
			}
		}
	}
	if matched == 0 {
		noMatchMessage(os.Stdout, sf, method)
	}
}
