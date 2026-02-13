package main

import (
	"fmt"
	"sort"
)

func cmdEvents(path string) error {
	counts, err := discoverEvents(path)
	if err != nil {
		return err
	}
	if len(counts) == 0 {
		fmt.Println("no supported events found")
		return nil
	}

	type entry struct {
		name    string
		samples int
	}
	var ranked []entry
	for name, cnt := range counts {
		ranked = append(ranked, entry{name, cnt})
	}
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].samples > ranked[j].samples })

	fmt.Printf("%-10s %9s\n", "EVENT", "SAMPLES")
	for _, e := range ranked {
		fmt.Printf("%-10s %9d\n", e.name, e.samples)
	}
	return nil
}
