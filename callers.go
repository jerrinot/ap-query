package main

import (
	"fmt"
	"os"
	"regexp"

	"github.com/spf13/cobra"
)

func newCallersCmd() *cobra.Command {
	var shared sharedFlags
	var method string
	var depth int
	var minPct float64
	var hide string
	cmd := &cobra.Command{
		Use:   "callers <file>",
		Short: "Callers ascending to a method (-m required)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if method == "" {
				return fmt.Errorf("-m/--method required")
			}
			pctx, err := preprocessProfile(shared.toOpts(args[0], "callers"))
			if err != nil {
				return err
			}
			sf := pctx.sf
			if hide != "" {
				re, err := regexp.Compile(hide)
				if err != nil {
					return fmt.Errorf("invalid --hide regex: %v", err)
				}
				sf = sf.hideFrames(re)
			}
			cmdCallers(sf, method, depth, minPct)
			return nil
		},
	}
	shared.register(cmd)
	cmd.Flags().StringVarP(&method, "method", "m", "", "Substring match on method name (required)")
	cmd.Flags().IntVar(&depth, "depth", 4, "Max depth")
	cmd.Flags().Float64Var(&minPct, "min-pct", 1.0, "Hide nodes below this %")
	cmd.Flags().StringVar(&hide, "hide", "", "Remove matching frames before analysis (regex)")
	return cmd
}

func cmdCallers(sf *stackFile, method string, maxDepth int, minPct float64) {
	if sf.totalSamples == 0 {
		fmt.Fprintln(os.Stdout, "no samples (empty profile or all filtered out)")
		return
	}
	pt := buildCallersPT(sf, method)
	pt.fprintTree(os.Stdout, sf, method, maxDepth, minPct, false)
}
