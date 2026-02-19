package main

import (
	"fmt"
	"sort"

	"github.com/spf13/cobra"
)

func newEventsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "events <file>",
		Short: "List event types in a JFR or pprof file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := args[0]
			if path != "-" && detectFormat(path) == formatCollapsed {
				return fmt.Errorf("events command requires a JFR or pprof file")
			}
			return cmdEvents(path)
		},
	}
}

func cmdEvents(path string) error {
	var parsed *parsedProfile
	var err error
	if path == "-" {
		res, err := parseStdin(nil)
		if err != nil {
			return err
		}
		if res.parsed == nil {
			return fmt.Errorf("events command requires a JFR or pprof file (stdin appears to be collapsed text)")
		}
		parsed = res.parsed
	} else {
		parsed, err = parseStructuredProfile(path, nil)
		if err != nil {
			return err
		}
	}
	if parsed == nil {
		return fmt.Errorf("events command requires a JFR or pprof file")
	}
	counts := parsed.eventCounts
	if len(counts) == 0 {
		fmt.Println("no supported events found")
		return nil
	}

	type entry struct {
		name    string
		samples int
	}
	var ranked []entry
	var total int
	for name, cnt := range counts {
		ranked = append(ranked, entry{name, cnt})
		total += cnt
	}
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].samples > ranked[j].samples })

	fmt.Printf("%-10s %9s\n", "EVENT", "SAMPLES")
	for _, e := range ranked {
		fmt.Printf("%-10s %9d\n", e.name, e.samples)
	}
	fmt.Printf("%-10s %9d\n", "total", total)
	return nil
}
