package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"strings"
	"testing"
)

func countJFRChunks(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	if len(data) == 0 {
		return 0, fmt.Errorf("empty file")
	}

	off := 0
	chunks := 0
	for off < len(data) {
		if off+jfrChunkHeaderSize > len(data) {
			return 0, fmt.Errorf("truncated chunk header at offset %d", off)
		}
		if magic := binary.BigEndian.Uint32(data[off : off+4]); magic != jfrChunkMagic {
			return 0, fmt.Errorf("invalid chunk signature 0x%08x at offset %d", magic, off)
		}
		size := int(binary.BigEndian.Uint64(data[off+8 : off+16]))
		if size <= 0 {
			return 0, fmt.Errorf("invalid chunk size %d at offset %d", size, off)
		}
		if off+size > len(data) {
			return 0, fmt.Errorf("chunk at offset %d extends beyond file size", off)
		}
		off += size
		chunks++
	}
	return chunks, nil
}

func samplesContainingMethod(sf *stackFile, pattern string) int {
	total := 0
	for _, st := range sf.stacks {
		for _, frame := range st.frames {
			if strings.Contains(frame, pattern) {
				total += st.count
				break
			}
		}
	}
	return total
}

func TestJFRMultiChunkFixtureHasMultipleChunks(t *testing.T) {
	path := jfrFixture("multichunk.jfr")
	chunks, err := countJFRChunks(path)
	if err != nil {
		t.Fatalf("countJFRChunks: %v", err)
	}
	if chunks < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", chunks)
	}

	parsed, err := parseJFRData(path, singleJFREventType("cpu"), parseOpts{})
	if err != nil {
		t.Fatalf("parseJFRData: %v", err)
	}
	if parsed.eventCounts["cpu"] <= 0 {
		t.Fatalf("expected cpu events in multichunk fixture, got %v", parsed.eventCounts)
	}
}

func TestParseJFRDataMultiChunkAllEventsMatchesSingleCPU(t *testing.T) {
	path := jfrFixture("multichunk.jfr")

	parsedAll, err := parseJFRData(path, allJFREventTypes(), parseOpts{})
	if err != nil {
		t.Fatalf("parseJFRData(all): %v", err)
	}
	parsedCPU, err := parseJFRData(path, singleJFREventType("cpu"), parseOpts{})
	if err != nil {
		t.Fatalf("parseJFRData(cpu): %v", err)
	}

	got := parsedAll.stacksByEvent["cpu"]
	if got == nil {
		got = &stackFile{}
	}
	want := parsedCPU.stacksByEvent["cpu"]
	if want == nil {
		want = &stackFile{}
	}

	if got.totalSamples != want.totalSamples {
		t.Fatalf("cpu totalSamples mismatch: all=%d single=%d", got.totalSamples, want.totalSamples)
	}
	if parsedAll.eventCounts["cpu"] != parsedCPU.eventCounts["cpu"] {
		t.Fatalf("cpu eventCounts mismatch: all=%d single=%d", parsedAll.eventCounts["cpu"], parsedCPU.eventCounts["cpu"])
	}
}

func TestJFRMultiChunkAlternatingPhasesPresent(t *testing.T) {
	sf, _, err := openInput(jfrFixture("multichunk.jfr"), "cpu")
	if err != nil {
		t.Fatalf("openInput: %v", err)
	}
	if sf.totalSamples == 0 {
		t.Fatal("expected >0 cpu samples")
	}

	a := samplesContainingMethod(sf, "phaseA")
	b := samplesContainingMethod(sf, "phaseB")
	if a == 0 || b == 0 {
		t.Fatalf("expected samples for both phaseA and phaseB, got phaseA=%d phaseB=%d", a, b)
	}

	aPct := 100.0 * float64(a) / float64(sf.totalSamples)
	bPct := 100.0 * float64(b) / float64(sf.totalSamples)
	if aPct < 10.0 || bPct < 10.0 {
		t.Fatalf("expected both phases to contribute >=10%%, got phaseA=%.1f%% phaseB=%.1f%%", aPct, bPct)
	}
}
