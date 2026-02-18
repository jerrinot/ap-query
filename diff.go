package main

import (
	"fmt"
	"math"
	"sort"

	"github.com/spf13/cobra"
)

func newDiffCmd() *cobra.Command {
	var event string
	var thread string
	var minDelta float64
	var top int
	var fqn bool
	cmd := &cobra.Command{
		Use:   "diff <before> <after>",
		Short: "Compare two profiles: shows REGRESSION / IMPROVEMENT / NEW / GONE",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			beforePath := args[0]
			afterPath := args[1]

			eventExplicit := event != ""
			eventType := event
			if eventType == "" {
				eventType = "cpu"
			}
			switch eventType {
			case "cpu", "wall", "alloc", "lock":
			default:
				return fmt.Errorf("unknown event type %q (valid: cpu, wall, alloc, lock)", eventType)
			}

			eventsToParse := allJFREventTypes()
			if eventExplicit {
				eventsToParse = singleJFREventType(eventType)
			}

			var beforeEventCounts map[string]int
			var beforeStacksByEvent map[string]*stackFile
			if isJFRPath(beforePath) {
				parsed, err := parseJFRData(beforePath, eventsToParse, parseOpts{})
				if err != nil {
					return err
				}
				beforeEventCounts = parsed.eventCounts
				beforeStacksByEvent = parsed.stacksByEvent
			}
			var afterEventCounts map[string]int
			var afterStacksByEvent map[string]*stackFile
			if isJFRPath(afterPath) {
				parsed, err := parseJFRData(afterPath, eventsToParse, parseOpts{})
				if err != nil {
					return err
				}
				afterEventCounts = parsed.eventCounts
				afterStacksByEvent = parsed.stacksByEvent
			}
			eventType, eventReason := resolveEventTypeForDiff(eventType, eventExplicit, beforeEventCounts, afterEventCounts)

			var before *stackFile
			if beforeStacksByEvent != nil {
				before = beforeStacksByEvent[eventType]
				if before == nil {
					before = &stackFile{}
				}
			} else {
				var err error
				before, _, err = openInput(beforePath, eventType)
				if err != nil {
					return err
				}
			}

			var after *stackFile
			if afterStacksByEvent != nil {
				after = afterStacksByEvent[eventType]
				if after == nil {
					after = &stackFile{}
				}
			} else {
				var err error
				after, _, err = openInput(afterPath, eventType)
				if err != nil {
					return err
				}
			}
			if thread != "" {
				before = before.filterByThread(thread)
				after = after.filterByThread(thread)
			}
			printEventSelectionForDiff(eventType, eventReason, beforeEventCounts, afterEventCounts)
			cmdDiff(before, after, minDelta, top, fqn)
			return nil
		},
	}
	cmd.Flags().StringVarP(&event, "event", "e", "", "Event type: cpu, wall, alloc, lock (default: cpu)")
	cmd.Flags().StringVarP(&thread, "thread", "t", "", "Filter to threads matching substring")
	cmd.Flags().Float64Var(&minDelta, "min-delta", 0.5, "Hide entries below this % change")
	cmd.Flags().IntVar(&top, "top", 0, "Limit output rows (default: unlimited)")
	cmd.Flags().BoolVar(&fqn, "fqn", false, "Show fully-qualified names")
	return cmd
}

func selfPcts(sf *stackFile, fqn bool) map[string]float64 {
	counts := selfCounts(sf, fqn)
	pcts := make(map[string]float64)
	if sf.totalSamples > 0 {
		for name, c := range counts {
			pcts[name] = pctOf(c, sf.totalSamples)
		}
	}
	return pcts
}

func cmdDiff(before, after *stackFile, minDelta float64, top int, fqn bool) {
	beforePct := selfPcts(before, fqn)
	afterPct := selfPcts(after, fqn)

	allMethods := make(map[string]bool)
	for m := range beforePct {
		allMethods[m] = true
	}
	for m := range afterPct {
		allMethods[m] = true
	}

	type diffEntry struct {
		name   string
		before float64
		after  float64
		delta  float64
	}

	var regressions, improvements, newMethods, goneMethods []diffEntry

	for m := range allMethods {
		b := beforePct[m]
		a := afterPct[m]
		delta := a - b

		_, inBefore := beforePct[m]
		_, inAfter := afterPct[m]

		if inBefore && inAfter {
			if math.Abs(delta) < minDelta {
				continue
			}
			if delta > 0 {
				regressions = append(regressions, diffEntry{m, b, a, delta})
			} else {
				improvements = append(improvements, diffEntry{m, b, a, delta})
			}
		} else if !inBefore && inAfter {
			if a < minDelta {
				continue
			}
			newMethods = append(newMethods, diffEntry{m, 0, a, a})
		} else if inBefore && !inAfter {
			if b < minDelta {
				continue
			}
			goneMethods = append(goneMethods, diffEntry{m, b, 0, -b})
		}
	}

	sort.Slice(regressions, func(i, j int) bool { return regressions[i].delta > regressions[j].delta })
	sort.Slice(improvements, func(i, j int) bool { return improvements[i].delta < improvements[j].delta })
	sort.Slice(newMethods, func(i, j int) bool { return newMethods[i].after > newMethods[j].after })
	sort.Slice(goneMethods, func(i, j int) bool { return goneMethods[i].before > goneMethods[j].before })

	regressions = regressions[:truncate(len(regressions), top)]
	improvements = improvements[:truncate(len(improvements), top)]
	newMethods = newMethods[:truncate(len(newMethods), top)]
	goneMethods = goneMethods[:truncate(len(goneMethods), top)]

	anyOutput := false

	if len(regressions) > 0 {
		fmt.Println("REGRESSION")
		for _, e := range regressions {
			fmt.Printf("  %-50s %5.1f%% -> %5.1f%%  (+%.1f%%)\n", e.name, e.before, e.after, e.delta)
		}
		anyOutput = true
	}
	if len(improvements) > 0 {
		fmt.Println("IMPROVEMENT")
		for _, e := range improvements {
			fmt.Printf("  %-50s %5.1f%% -> %5.1f%%  (%.1f%%)\n", e.name, e.before, e.after, e.delta)
		}
		anyOutput = true
	}
	if len(newMethods) > 0 {
		fmt.Println("NEW")
		for _, e := range newMethods {
			fmt.Printf("  %-50s %.1f%%\n", e.name, e.after)
		}
		anyOutput = true
	}
	if len(goneMethods) > 0 {
		fmt.Println("GONE")
		for _, e := range goneMethods {
			fmt.Printf("  %-50s %.1f%%\n", e.name, e.before)
		}
		anyOutput = true
	}

	if !anyOutput {
		fmt.Println("no significant changes")
	}
}
