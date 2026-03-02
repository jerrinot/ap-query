package main

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

type singleAssignStringValue struct {
	name   string
	value  *string
	wasSet bool
}

func (s *singleAssignStringValue) String() string {
	if s.value == nil {
		return ""
	}
	return *s.value
}

func (s *singleAssignStringValue) Set(v string) error {
	if s.wasSet {
		return fmt.Errorf("%s must not be repeated", s.name)
	}
	s.wasSet = true
	if s.value != nil {
		*s.value = v
	}
	return nil
}

func (s *singleAssignStringValue) Type() string {
	return "string"
}

func newDiffCmd() *cobra.Command {
	var event string
	var thread string
	var minDelta float64
	var top int
	var fqn bool
	var fromStr string
	var toStr string
	var vsFromStr string
	var vsToStr string
	cmd := &cobra.Command{
		Use:   "diff <before> <after> | diff <file> --from DURATION [--to DURATION] --vs-from DURATION [--vs-to DURATION]",
		Short: "Compare two profiles: shows REGRESSION / IMPROVEMENT / NEW / GONE",
		Example: strings.Join([]string{
			"  ap-query diff before.jfr after.jfr --min-delta 0.5",
			"  ap-query diff profile.jfr --from 55s --to 1m05s --vs-from 2m45s --vs-to 3m10s",
		}, "\n"),
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			windowMode := fromStr != "" || toStr != "" || vsFromStr != "" || vsToStr != ""
			if len(args) == 1 {
				path := args[0]
				if !windowMode {
					return fmt.Errorf("single-file diff requires two time windows: --from/--to and --vs-from/--vs-to")
				}
				if detectFormat(path) != formatJFR {
					return fmt.Errorf("single-file window diff requires a JFR file")
				}
				beforeWindow, err := parseDurationWindow("--from", fromStr, "--to", toStr)
				if err != nil {
					return err
				}
				if !beforeWindow.specified {
					return fmt.Errorf("single-file diff requires at least one of --from or --to")
				}
				afterWindow, err := parseDurationWindow("--vs-from", vsFromStr, "--vs-to", vsToStr)
				if err != nil {
					return err
				}
				if !afterWindow.specified {
					return fmt.Errorf("single-file diff requires at least one of --vs-from or --vs-to")
				}
				return runSingleFileWindowDiff(path, beforeWindow, afterWindow, event, thread, minDelta, top, fqn)
			}
			if windowMode {
				return fmt.Errorf("--from/--to/--vs-from/--vs-to can only be used with single-file diff mode")
			}

			beforePath := args[0]
			afterPath := args[1]

			eventExplicit := event != ""
			eventType := event
			if eventType == "" {
				eventType = "cpu"
			}
			if !isKnownEventType(eventType) {
				// For collapsed text files, reject unknown events immediately
				// (no event metadata to discover dynamically).
				beforeFmt := detectFormat(beforePath)
				afterFmt := detectFormat(afterPath)
				if (beforeFmt == formatCollapsed && beforePath != "-") &&
					(afterFmt == formatCollapsed && afterPath != "-") {
					return fmt.Errorf("unknown event type %q (valid: %s)", eventType, validEventTypesString())
				}
			}

			eventsToParse := allEventTypes()
			if eventExplicit {
				eventsToParse = singleEventType(eventType)
			}

			// Parse each side for metadata. Structured formats (JFR/pprof)
			// yield a parsedProfile with eventCounts for event resolution.
			// Stdin is parsed eagerly (can only be read once).
			type diffSide struct {
				parsed      *parsedProfile // non-nil for JFR/pprof
				collapsedSF *stackFile     // non-nil for collapsed stdin
			}
			parseSide := func(path string) (diffSide, error) {
				if path == "-" {
					res, err := parseStdin(eventsToParse)
					if err != nil {
						return diffSide{}, err
					}
					if res.parsed != nil {
						return diffSide{parsed: res.parsed}, nil
					}
					return diffSide{collapsedSF: res.sf}, nil
				}
				p, err := parseStructuredProfile(path, eventsToParse)
				if err != nil {
					return diffSide{}, err
				}
				if p != nil {
					return diffSide{parsed: p}, nil
				}
				return diffSide{}, nil
			}

			beforeSide, err := parseSide(beforePath)
			if err != nil {
				return err
			}
			afterSide, err := parseSide(afterPath)
			if err != nil {
				return err
			}

			var beforeEventCounts, afterEventCounts map[string]int
			if beforeSide.parsed != nil {
				beforeEventCounts = beforeSide.parsed.eventCounts
			}
			if afterSide.parsed != nil {
				afterEventCounts = afterSide.parsed.eventCounts
			}
			eventType, eventReason := resolveEventTypeForDiff(eventType, eventExplicit, beforeEventCounts, afterEventCounts)

			// Post-parse validation: reject explicitly-requested unknown events
			// that were not found in either side with metadata.
			if eventExplicit && !isKnownEventType(eventType) {
				hasBefore := beforeEventCounts != nil && beforeEventCounts[eventType] > 0
				hasAfter := afterEventCounts != nil && afterEventCounts[eventType] > 0
				if !hasBefore && !hasAfter {
					// Collect available events from whichever side has metadata.
					available := make(map[string]struct{})
					for e := range beforeEventCounts {
						available[e] = struct{}{}
					}
					for e := range afterEventCounts {
						available[e] = struct{}{}
					}
					names := make([]string, 0, len(available))
					for e := range available {
						names = append(names, e)
					}
					sort.Strings(names)
					if len(names) == 0 {
						return fmt.Errorf("event %q not found (no events in files)", eventType)
					}
					return fmt.Errorf("event %q not found (available: %s)", eventType, strings.Join(names, ", "))
				}
			}

			var before *stackFile
			switch {
			case beforeSide.parsed != nil:
				before = beforeSide.parsed.stacksByEvent[eventType]
				if before == nil {
					before = &stackFile{}
				}
			case beforeSide.collapsedSF != nil:
				before = beforeSide.collapsedSF
			default:
				before, _, err = openInput(beforePath, eventType)
				if err != nil {
					return err
				}
			}

			var after *stackFile
			switch {
			case afterSide.parsed != nil:
				after = afterSide.parsed.stacksByEvent[eventType]
				if after == nil {
					after = &stackFile{}
				}
			case afterSide.collapsedSF != nil:
				after = afterSide.collapsedSF
			default:
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
	cmd.Flags().StringVarP(&event, "event", "e", "", "Event type: cpu, wall, alloc, lock, or hardware counter name (default: cpu)")
	cmd.Flags().StringVarP(&thread, "thread", "t", "", "Filter to threads matching substring")
	cmd.Flags().Float64Var(&minDelta, "min-delta", 0.5, "Hide entries below this % change")
	cmd.Flags().IntVar(&top, "top", 0, "Limit output rows (default: unlimited)")
	cmd.Flags().BoolVar(&fqn, "fqn", false, "Show fully-qualified names")
	cmd.Flags().Var(&singleAssignStringValue{name: "--from", value: &fromStr}, "from", "Start of first time window (single-file JFR diff only)")
	cmd.Flags().Var(&singleAssignStringValue{name: "--to", value: &toStr}, "to", "End of first time window (single-file JFR diff only)")
	cmd.Flags().Var(&singleAssignStringValue{name: "--vs-from", value: &vsFromStr}, "vs-from", "Start of second time window (single-file JFR diff only)")
	cmd.Flags().Var(&singleAssignStringValue{name: "--vs-to", value: &vsToStr}, "vs-to", "End of second time window (single-file JFR diff only)")
	return cmd
}

func runSingleFileWindowDiff(path string, beforeWindow, afterWindow durationWindow, event, thread string, minDelta float64, top int, fqn bool) error {
	eventExplicit := event != ""
	eventType := event
	if eventType == "" {
		eventType = "cpu"
	}

	eventsToParse := allEventTypes()
	if eventExplicit {
		eventsToParse = singleEventType(eventType)
	}

	parseWindow := func(w durationWindow) (*parsedProfile, map[string]int, error) {
		parsed, err := parseJFRData(path, eventsToParse, parseOpts{
			collectTimestamps: true,
			fromNanos:         w.fromNanos,
			toNanos:           w.toNanos,
		})
		if err != nil {
			return nil, nil, err
		}
		return parsed, eventCountsFromTimedEvents(parsed.timedEvents), nil
	}

	beforeParsed, beforeEventCounts, err := parseWindow(beforeWindow)
	if err != nil {
		return err
	}
	afterParsed, afterEventCounts, err := parseWindow(afterWindow)
	if err != nil {
		return err
	}

	eventType, eventReason := resolveEventTypeForDiff(eventType, eventExplicit, beforeEventCounts, afterEventCounts)

	if eventExplicit && !isKnownEventType(eventType) {
		hasBefore := beforeParsed.eventCounts != nil && beforeParsed.eventCounts[eventType] > 0
		hasAfter := afterParsed.eventCounts != nil && afterParsed.eventCounts[eventType] > 0
		if !hasBefore && !hasAfter {
			available := make(map[string]struct{})
			for e := range beforeParsed.eventCounts {
				available[e] = struct{}{}
			}
			for e := range afterParsed.eventCounts {
				available[e] = struct{}{}
			}
			names := make([]string, 0, len(available))
			for e := range available {
				names = append(names, e)
			}
			sort.Strings(names)
			if len(names) == 0 {
				return fmt.Errorf("event %q not found (no events in file)", eventType)
			}
			return fmt.Errorf("event %q not found (available: %s)", eventType, strings.Join(names, ", "))
		}
	}

	before := beforeParsed.stacksByEvent[eventType]
	if before == nil {
		before = &stackFile{}
	}
	after := afterParsed.stacksByEvent[eventType]
	if after == nil {
		after = &stackFile{}
	}

	if thread != "" {
		before = before.filterByThread(thread)
		after = after.filterByThread(thread)
	}

	printEventSelectionForDiff(eventType, eventReason, beforeEventCounts, afterEventCounts)
	cmdDiff(before, after, minDelta, top, fqn)
	return nil
}

func eventCountsFromTimedEvents(timedByEvent map[string][]timedEvent) map[string]int {
	counts := make(map[string]int)
	for eventType, events := range timedByEvent {
		total := 0
		for i := range events {
			total += events[i].weight
		}
		if total > 0 {
			counts[eventType] = total
		}
	}
	return counts
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
