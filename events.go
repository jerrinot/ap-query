package main

import (
	"fmt"
	"sort"

	"github.com/spf13/cobra"
)

func newEventsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "events <file>",
		Short: "List event types in a JFR file (JFR only)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !isJFRPath(args[0]) {
				return fmt.Errorf("events command requires a JFR file")
			}
			return cmdEvents(args[0])
		},
	}
}

func cmdEvents(path string) error {
	parsed, err := parseJFRData(path, nil, parseOpts{})
	if err != nil {
		return err
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

	fmt.Printf("%-10s %9s\n", "EVENT", "COUNT")
	for _, e := range ranked {
		fmt.Printf("%-10s %9d\n", e.name, e.samples)
	}
	fmt.Printf("%-10s %9d\n", "total", total)
	return nil
}
