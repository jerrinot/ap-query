package main

import (
	"os"
	"strings"
	"testing"
)

func captureStderr(f func()) string {
	return captureStream(&os.Stderr, f)
}

func TestResolveEventTypeSingleAutoSelect(t *testing.T) {
	got, reason := resolveEventType("cpu", false, map[string]int{"wall": 11})
	if got != "wall" {
		t.Errorf("resolveEventType(cpu, auto, wall-only) = %q, want wall", got)
	}
	if reason != eventReasonSingleAvailable {
		t.Errorf("resolveEventType reason = %q, want %q", reason, eventReasonSingleAvailable)
	}
}

func TestResolveEventTypeKeepsRequestedWhenPresent(t *testing.T) {
	got, reason := resolveEventType("cpu", false, map[string]int{"cpu": 1, "wall": 100})
	if got != "cpu" {
		t.Errorf("resolveEventType(cpu, auto, cpu+wall) = %q, want cpu", got)
	}
	if reason != eventReasonDefaultPresent {
		t.Errorf("resolveEventType reason = %q, want %q", reason, eventReasonDefaultPresent)
	}
}

func TestResolveEventTypeFallsBackToDominant(t *testing.T) {
	got, reason := resolveEventType("cpu", false, map[string]int{"alloc": 20, "wall": 10})
	if got != "alloc" {
		t.Errorf("resolveEventType(cpu, auto, alloc+wall) = %q, want alloc", got)
	}
	if reason != eventReasonFallbackDominant {
		t.Errorf("resolveEventType reason = %q, want %q", reason, eventReasonFallbackDominant)
	}
}

func TestResolveEventTypeExplicitWins(t *testing.T) {
	got, reason := resolveEventType("cpu", true, map[string]int{"wall": 99})
	if got != "cpu" {
		t.Errorf("resolveEventType(cpu, explicit, wall-only) = %q, want cpu", got)
	}
	if reason != eventReasonExplicit {
		t.Errorf("resolveEventType reason = %q, want %q", reason, eventReasonExplicit)
	}
}

func TestResolveEventTypeForDiffUsesCommonEvents(t *testing.T) {
	got, reason := resolveEventTypeForDiff(
		"cpu",
		false,
		map[string]int{"cpu": 10, "wall": 30},
		map[string]int{"cpu": 20, "alloc": 100},
	)
	if got != "cpu" {
		t.Errorf("resolveEventTypeForDiff = %q, want cpu", got)
	}
	if reason != eventReasonDiffCommon {
		t.Errorf("resolveEventTypeForDiff reason = %q, want %q", reason, eventReasonDiffCommon)
	}
}

func TestResolveEventTypeForDiffNoCommon(t *testing.T) {
	got, reason := resolveEventTypeForDiff(
		"cpu",
		false,
		map[string]int{"wall": 30},
		map[string]int{"alloc": 40},
	)
	if got != "alloc" {
		t.Errorf("resolveEventTypeForDiff = %q, want alloc", got)
	}
	if reason != eventReasonDiffNoCommonFallback {
		t.Errorf("resolveEventTypeForDiff reason = %q, want %q", reason, eventReasonDiffNoCommonFallback)
	}
}

func TestResolveEventTypeForDiffOneSidedMetadataKeepsRequested(t *testing.T) {
	got, reason := resolveEventTypeForDiff(
		"cpu",
		false,
		map[string]int{"wall": 30},
		nil,
	)
	if got != "cpu" {
		t.Errorf("resolveEventTypeForDiff = %q, want cpu", got)
	}
	if reason != eventReasonDiffOneSidedMetadata {
		t.Errorf("resolveEventTypeForDiff reason = %q, want %q", reason, eventReasonDiffOneSidedMetadata)
	}
}

func TestPrintEventSelectionForSingle(t *testing.T) {
	out := captureStderr(func() {
		printEventSelectionForSingle("cpu", eventReasonDefaultPresent, map[string]int{"cpu": 200, "wall": 50, "alloc": 20})
	})

	if !strings.Contains(out, "Event: cpu (default present)") {
		t.Fatalf("missing event line: %q", out)
	}
	if !strings.Contains(out, "Also available: wall (50 samples), alloc (20 samples)") {
		t.Fatalf("missing also-available line: %q", out)
	}
}

func TestPrintEventSelectionForDiff(t *testing.T) {
	out := captureStderr(func() {
		printEventSelectionForDiff(
			"cpu",
			eventReasonDiffNoCommonFallback,
			map[string]int{"wall": 30, "alloc": 10},
			map[string]int{"cpu": 20, "lock": 7},
		)
	})

	if !strings.Contains(out, "Event: cpu (no common event; dominant fallback)") {
		t.Fatalf("missing event line: %q", out)
	}
	if !strings.Contains(out, "Warning: no common event type across both recordings") {
		t.Fatalf("missing warning line: %q", out)
	}
	if !strings.Contains(out, "Before also available: wall (30 samples), alloc (10 samples)") {
		t.Fatalf("missing before line: %q", out)
	}
	if !strings.Contains(out, "After also available: lock (7 samples)") {
		t.Fatalf("missing after line: %q", out)
	}
}

func TestPrintEventSelectionForDiffOneSidedWarning(t *testing.T) {
	out := captureStderr(func() {
		printEventSelectionForDiff(
			"cpu",
			eventReasonDiffOneSidedMetadata,
			map[string]int{"wall": 30},
			nil,
		)
	})

	if !strings.Contains(out, "Event: cpu (single-sided metadata; kept requested)") {
		t.Fatalf("missing event line: %q", out)
	}
	if !strings.Contains(out, "Warning: only before input exposes JFR event metadata") {
		t.Fatalf("missing one-sided warning: %q", out)
	}
	if !strings.Contains(out, "event compatibility could not be verified") {
		t.Fatalf("missing compatibility text: %q", out)
	}
	if !strings.Contains(out, "ap-query collapse --event cpu") {
		t.Fatalf("missing regeneration tip: %q", out)
	}
}

func TestIsKnownEventType(t *testing.T) {
	for _, et := range validEventTypes {
		if !isKnownEventType(et) {
			t.Errorf("isKnownEventType(%q) = false, want true", et)
		}
	}
	for _, bad := range []string{"", "bogus", "CPU", "itimer"} {
		if isKnownEventType(bad) {
			t.Errorf("isKnownEventType(%q) = true, want false", bad)
		}
	}
}

func TestValidEventTypesMatchEventOrder(t *testing.T) {
	if len(validEventTypes) != len(eventOrder) {
		t.Fatalf("validEventTypes has %d entries, eventOrder has %d", len(validEventTypes), len(eventOrder))
	}
	for i, et := range validEventTypes {
		rank, ok := eventOrder[et]
		if !ok {
			t.Errorf("eventOrder missing %q", et)
		}
		if rank != i {
			t.Errorf("eventOrder[%q] = %d, want %d", et, rank, i)
		}
	}
}

func TestValidEventTypesString(t *testing.T) {
	got := validEventTypesString()
	want := "cpu, wall, alloc, lock"
	if got != want {
		t.Errorf("validEventTypesString() = %q, want %q", got, want)
	}
}

func TestJFRTraceAutoSelectSingleEvent(t *testing.T) {
	path := jfrFixture("wall.jfr")
	parsed, err := parseJFRData(path, nil, parseOpts{})
	if err != nil {
		t.Fatalf("parseJFRData: %v", err)
	}
	eventCounts := parsed.eventCounts
	eventType, _ := resolveEventType("cpu", false, eventCounts)
	if eventType != "wall" {
		t.Fatalf("resolveEventType = %q, want wall", eventType)
	}
	sf, _, err := openInput(path, eventType)
	if err != nil {
		t.Fatalf("openInput: %v", err)
	}
	out := captureOutput(func() {
		cmdTrace(sf, "Workload.cpuWork", 0.5, false)
	})
	if !strings.Contains(out, "Hottest leaf:") {
		t.Fatalf("expected trace output for wall.jfr auto-select, got:\n%s", out)
	}
}
