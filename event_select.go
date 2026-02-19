package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

var validEventTypes = []string{"cpu", "wall", "alloc", "lock"}

var eventOrder map[string]int

func init() {
	eventOrder = make(map[string]int, len(validEventTypes))
	for i, e := range validEventTypes {
		eventOrder[e] = i
	}
}

func isKnownEventType(s string) bool {
	_, ok := eventOrder[s]
	return ok
}

func validEventTypesString() string {
	return strings.Join(validEventTypes, ", ")
}

type eventSelectionReason string

const (
	eventReasonUnknown              eventSelectionReason = "unknown"
	eventReasonExplicit             eventSelectionReason = "explicit"
	eventReasonSingleAvailable      eventSelectionReason = "single_available"
	eventReasonDefaultPresent       eventSelectionReason = "default_present"
	eventReasonFallbackDominant     eventSelectionReason = "fallback_dominant"
	eventReasonDiffCommon           eventSelectionReason = "diff_common"
	eventReasonDiffNoCommonFallback eventSelectionReason = "diff_no_common_fallback"
	eventReasonDiffOneSidedMetadata eventSelectionReason = "diff_one_sided_metadata"
)

func resolveEventType(requested string, explicit bool, counts map[string]int) (selected string, reason eventSelectionReason) {
	if explicit {
		return requested, eventReasonExplicit
	}
	if len(counts) == 0 {
		return requested, eventReasonUnknown
	}
	if len(counts) == 1 {
		best := dominantEvent(counts)
		if best == "" {
			return requested, eventReasonUnknown
		}
		return best, eventReasonSingleAvailable
	}
	if counts[requested] > 0 {
		return requested, eventReasonDefaultPresent
	}
	best := dominantEvent(counts)
	if best == "" {
		return requested, eventReasonUnknown
	}
	return best, eventReasonFallbackDominant
}

func resolveEventTypeForDiff(requested string, explicit bool, beforeCounts, afterCounts map[string]int) (selected string, reason eventSelectionReason) {
	if explicit {
		return requested, eventReasonExplicit
	}
	beforeKnown := beforeCounts != nil
	afterKnown := afterCounts != nil
	if beforeKnown && afterKnown {
		common := commonEventCounts(beforeCounts, afterCounts)
		if len(common) > 0 {
			selected, _ := resolveEventType(requested, false, common)
			return selected, eventReasonDiffCommon
		}
		combined := combineEventCounts(beforeCounts, afterCounts)
		selected, _ := resolveEventType(requested, false, combined)
		return selected, eventReasonDiffNoCommonFallback
	}
	if beforeKnown || afterKnown {
		// Mixed-format diff (JFR + collapsed): keep requested/default event.
		// We cannot verify event compatibility from collapsed input metadata.
		return requested, eventReasonDiffOneSidedMetadata
	}
	return requested, eventReasonUnknown
}

func printEventSelectionForSingle(eventType string, reason eventSelectionReason, counts map[string]int) {
	if len(counts) <= 1 {
		return
	}
	fmt.Fprintf(os.Stderr, "Event: %s (%s)\n", eventType, selectionModeLabel(reason))
	others := formatEventList(counts, eventType)
	if len(others) > 0 {
		fmt.Fprintf(os.Stderr, "Also available: %s\n", strings.Join(others, ", "))
	}
}

func printEventSelectionForDiff(eventType string, reason eventSelectionReason, beforeCounts, afterCounts map[string]int) {
	beforeKnown := beforeCounts != nil
	afterKnown := afterCounts != nil
	oneSided := beforeKnown != afterKnown
	if len(beforeCounts) <= 1 && len(afterCounts) <= 1 && !oneSided && reason != eventReasonDiffNoCommonFallback {
		return
	}
	fmt.Fprintf(os.Stderr, "Event: %s (%s)\n", eventType, selectionModeLabel(reason))
	if oneSided {
		side := "before"
		if afterKnown {
			side = "after"
		}
		fmt.Fprintf(os.Stderr, "Warning: only %s input exposes JFR event metadata. Comparing event %q against untyped collapsed input; event compatibility could not be verified.\n", side, eventType)
		fmt.Fprintf(os.Stderr, "Tip: if the collapsed input was derived from JFR, regenerate it with `ap-query collapse --event %s` to match.\n", eventType)
	}
	if reason == eventReasonDiffNoCommonFallback {
		fmt.Fprintln(os.Stderr, "Warning: no common event type across both recordings; selected event may be missing in one file.")
	}
	if len(beforeCounts) > 1 {
		others := formatEventList(beforeCounts, eventType)
		if len(others) > 0 {
			fmt.Fprintf(os.Stderr, "Before also available: %s\n", strings.Join(others, ", "))
		}
	}
	if len(afterCounts) > 1 {
		others := formatEventList(afterCounts, eventType)
		if len(others) > 0 {
			fmt.Fprintf(os.Stderr, "After also available: %s\n", strings.Join(others, ", "))
		}
	}
}

func selectionModeLabel(reason eventSelectionReason) string {
	switch reason {
	case eventReasonExplicit:
		return "from --event"
	case eventReasonSingleAvailable:
		return "only available"
	case eventReasonDefaultPresent:
		return "default present"
	case eventReasonFallbackDominant:
		return "dominant fallback"
	case eventReasonDiffCommon:
		return "common across files"
	case eventReasonDiffNoCommonFallback:
		return "no common event; dominant fallback"
	case eventReasonDiffOneSidedMetadata:
		return "single-sided metadata; kept requested"
	default:
		return "selected"
	}
}

func dominantEvent(counts map[string]int) string {
	ranked := sortEventCounts(counts)
	if len(ranked) == 0 {
		return ""
	}
	return ranked[0].name
}

func formatEventList(counts map[string]int, skip string) []string {
	ranked := sortEventCounts(counts)
	out := make([]string, 0, len(ranked))
	for _, e := range ranked {
		if e.name == skip {
			continue
		}
		out = append(out, fmt.Sprintf("%s (%d samples)", e.name, e.samples))
	}
	return out
}

type eventCount struct {
	name    string
	samples int
}

func sortEventCounts(counts map[string]int) []eventCount {
	ranked := make([]eventCount, 0, len(counts))
	for name, samples := range counts {
		ranked = append(ranked, eventCount{name: name, samples: samples})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].samples != ranked[j].samples {
			return ranked[i].samples > ranked[j].samples
		}
		ri, rj := eventRank(ranked[i].name), eventRank(ranked[j].name)
		if ri != rj {
			return ri < rj
		}
		return ranked[i].name < ranked[j].name
	})
	return ranked
}

func eventRank(name string) int {
	if rank, ok := eventOrder[name]; ok {
		return rank
	}
	return 99
}

func combineEventCounts(a, b map[string]int) map[string]int {
	out := make(map[string]int)
	for name, n := range a {
		out[name] += n
	}
	for name, n := range b {
		out[name] += n
	}
	return out
}

func commonEventCounts(a, b map[string]int) map[string]int {
	out := make(map[string]int)
	for name, n := range a {
		if n > 0 && b[name] > 0 {
			out[name] = n + b[name]
		}
	}
	return out
}
