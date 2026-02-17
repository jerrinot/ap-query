package main

import (
	"fmt"
	"sort"
)

const eventsHelp = `Usage: ap-query events <file>

List event types and their counts in a JFR file.

Examples:
  ap-query events profile.jfr
`

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
