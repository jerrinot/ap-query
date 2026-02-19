package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/google/pprof/profile"
)

// ---------------------------------------------------------------------------
// pprof SampleType → ap-query event classification
// ---------------------------------------------------------------------------

type pprofSampleMapping struct {
	eventType string
	priority  int // higher wins when multiple SampleTypes map to same event
	valueIdx  int // index into Sample.Value
}

// classifyPprofSampleTypes maps pprof SampleTypes to ap-query event types.
// When multiple SampleTypes map to the same event, the highest-priority wins.
func classifyPprofSampleTypes(sts []*profile.ValueType) map[string]pprofSampleMapping {
	result := make(map[string]pprofSampleMapping)
	for i, st := range sts {
		eventType, priority := classifyPprofSampleType(st)
		if eventType == "" {
			continue
		}
		if existing, ok := result[eventType]; !ok || priority > existing.priority {
			result[eventType] = pprofSampleMapping{
				eventType: eventType,
				priority:  priority,
				valueIdx:  i,
			}
		}
	}
	return result
}

func classifyPprofSampleType(st *profile.ValueType) (eventType string, priority int) {
	typ := strings.ToLower(st.Type)
	unit := strings.ToLower(st.Unit)

	switch {
	case typ == "cpu" && unit == "nanoseconds":
		return "cpu", 2
	case typ == "samples" && unit == "count":
		return "cpu", 1
	case typ == "wall" && unit == "nanoseconds":
		return "wall", 2
	case typ == "alloc_objects" && unit == "count":
		return "alloc", 1
	case typ == "alloc_space" && unit == "bytes":
		return "alloc", 2
	case typ == "inuse_objects" && unit == "count":
		return "alloc", 1
	case typ == "inuse_space" && unit == "bytes":
		return "alloc", 2
	case typ == "contentions" && unit == "count":
		return "lock", 1
	case typ == "delay" && unit == "nanoseconds":
		return "lock", 2
	default:
		return "", 0
	}
}

// ---------------------------------------------------------------------------
// pprof → parsedProfile
// ---------------------------------------------------------------------------

func parsePprofData(path string, stackEvents map[string]struct{}) (*parsedProfile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// profile.Parse handles gzip detection internally.
	prof, err := profile.Parse(f)
	if err != nil {
		return nil, fmt.Errorf("pprof parse: %w", err)
	}

	return buildParsedProfile(prof, stackEvents)
}

func parsePprofFromReader(r io.Reader, stackEvents map[string]struct{}) (*parsedProfile, error) {
	prof, err := profile.Parse(r)
	if err != nil {
		return nil, fmt.Errorf("pprof parse: %w", err)
	}
	return buildParsedProfile(prof, stackEvents)
}

func buildParsedProfile(prof *profile.Profile, stackEvents map[string]struct{}) (*parsedProfile, error) {
	mappings := classifyPprofSampleTypes(prof.SampleType)
	if len(mappings) == 0 {
		// No known event types; create synthetic mapping for first sample type
		if len(prof.SampleType) > 0 {
			mappings["cpu"] = pprofSampleMapping{eventType: "cpu", priority: 0, valueIdx: 0}
		} else {
			return &parsedProfile{
				eventCounts:   map[string]int{},
				stacksByEvent: map[string]*stackFile{},
			}, nil
		}
	}

	// Build aggregation maps for requested events.
	aggByEvent := make(map[string]map[stackKey]*aggValue)
	eventCounts := make(map[string]int)

	for eventType := range mappings {
		if stackEvents != nil {
			if _, ok := stackEvents[eventType]; !ok {
				continue
			}
		}
		aggByEvent[eventType] = make(map[stackKey]*aggValue)
	}

	for _, sample := range prof.Sample {
		for eventType, mapping := range mappings {
			if mapping.valueIdx >= len(sample.Value) {
				continue
			}
			value := sample.Value[mapping.valueIdx]
			if value <= 0 {
				continue
			}

			// Count all events (even if not in stackEvents).
			eventCounts[eventType] += int(value)

			agg, ok := aggByEvent[eventType]
			if !ok {
				continue
			}

			frames, lines := resolvePprofStack(sample)
			if len(frames) == 0 {
				continue
			}

			thread := extractPprofThread(sample)
			key := stackKey{
				frames: buildStackKeyWithLines(frames, lines),
				thread: thread,
			}
			if v, ok := agg[key]; ok {
				v.count += int(value)
			} else {
				agg[key] = &aggValue{frames: frames, lines: lines, count: int(value)}
			}
		}
	}

	stacksByEvent := make(map[string]*stackFile, len(aggByEvent))
	for eventType, agg := range aggByEvent {
		stacksByEvent[eventType] = buildStackFile(agg)
	}

	var spanNanos int64
	if prof.DurationNanos > 0 {
		spanNanos = prof.DurationNanos
	}

	return &parsedProfile{
		eventCounts:   eventCounts,
		stacksByEvent: stacksByEvent,
		timedEvents:   nil, // pprof has no per-sample timestamps
		spanNanos:     spanNanos,
	}, nil
}

// resolvePprofStack extracts frames and line numbers from a pprof sample.
// pprof locations are leaf-first; we reverse to root-first.
// Within each location, Line entries are innermost-first; we reverse too.
func resolvePprofStack(sample *profile.Sample) ([]string, []uint32) {
	// Count total frames (including inlined).
	total := 0
	for _, loc := range sample.Location {
		if len(loc.Line) == 0 {
			total++ // unsymbolized
		} else {
			total += len(loc.Line)
		}
	}
	if total == 0 {
		return nil, nil
	}

	frames := make([]string, total)
	lines := make([]uint32, total)
	idx := total - 1 // fill from end (reverse)

	for _, loc := range sample.Location {
		if len(loc.Line) == 0 {
			// Unsymbolized location.
			frames[idx] = fmt.Sprintf("0x%x", loc.Address)
			lines[idx] = 0
			idx--
			continue
		}
		// Line entries: [0] = innermost (leaf), reverse to outermost first.
		for _, line := range loc.Line {
			name := ""
			if line.Function != nil {
				name = line.Function.Name
			}
			if name == "" {
				name = fmt.Sprintf("0x%x", loc.Address)
			}
			frames[idx] = name
			if line.Line > 0 {
				lines[idx] = uint32(line.Line)
			}
			idx--
		}
	}

	return frames, lines
}

// extractPprofThread gets thread info from pprof labels.
func extractPprofThread(sample *profile.Sample) string {
	for k, v := range sample.Label {
		if k == "thread" && len(v) > 0 {
			return v[0]
		}
	}
	for k, v := range sample.NumLabel {
		if k == "thread_id" && len(v) > 0 {
			return fmt.Sprintf("thread-%d", v[0])
		}
	}
	return ""
}
