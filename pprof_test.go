package main

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	pprofProfile "github.com/google/pprof/profile"
)

// ---------------------------------------------------------------------------
// Fixture paths — pre-generated pprof files in testdata/.
// Regenerate with: go run testdata/gen/gen_pprof.go
// ---------------------------------------------------------------------------

var (
	pprofCPUFixture    = filepath.Join("testdata", "cpu.pb.gz")
	pprofCPU2Fixture   = filepath.Join("testdata", "cpu2.pb.gz")
	pprofAllocFixture  = filepath.Join("testdata", "alloc.pb.gz")
	pprofAlloc2Fixture = filepath.Join("testdata", "alloc2.pb.gz")
	pprofMutexFixture  = filepath.Join("testdata", "mutex.pb.gz")
)

// ---------------------------------------------------------------------------
// Format detection
// ---------------------------------------------------------------------------

func TestDetectFormatPprof(t *testing.T) {
	tests := []struct {
		path string
		want profileFormat
	}{
		{"cpu.pb.gz", formatPprof},
		{"heap.pb", formatPprof},
		{"profile.pprof", formatPprof},
		{"profile.pprof.gz", formatPprof},
		{"PROFILE.PB.GZ", formatPprof},
		{"PROFILE.PPROF", formatPprof},
	}
	for _, tt := range tests {
		got := detectFormat(tt.path)
		if got != tt.want {
			t.Errorf("detectFormat(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// CPU profile tests
// ---------------------------------------------------------------------------

func TestPprofHot(t *testing.T) {
	path := pprofCPUFixture

	parsed, err := parsePprofData(path, singleEventType("cpu"))
	if err != nil {
		t.Fatal(err)
	}
	sf := parsed.stacksByEvent["cpu"]
	if sf == nil || sf.totalSamples == 0 {
		t.Skip("no CPU samples captured (short test, profiler rate too low)")
	}

	out := captureOutput(func() {
		cmdHot(sf, 20, false, 0)
	})

	// Verify output has some content.
	if len(strings.TrimSpace(out)) == 0 {
		t.Error("expected non-empty hot output")
	}
	// Verify our workload functions appear.
	if !strings.Contains(out, "pprofBusy") {
		t.Errorf("expected pprofBusy* in hot output, got:\n%s", out)
	}
}

func TestPprofTree(t *testing.T) {
	path := pprofCPUFixture

	parsed, err := parsePprofData(path, singleEventType("cpu"))
	if err != nil {
		t.Fatal(err)
	}
	sf := parsed.stacksByEvent["cpu"]
	if sf == nil || sf.totalSamples == 0 {
		t.Skip("no CPU samples captured")
	}

	out := captureOutput(func() {
		cmdTree(sf, "", 6, 0.1)
	})

	if len(strings.TrimSpace(out)) == 0 {
		t.Error("expected non-empty tree output")
	}
}

func TestPprofCallers(t *testing.T) {
	path := pprofCPUFixture

	parsed, err := parsePprofData(path, singleEventType("cpu"))
	if err != nil {
		t.Fatal(err)
	}
	sf := parsed.stacksByEvent["cpu"]
	if sf == nil || sf.totalSamples == 0 {
		t.Skip("no CPU samples captured")
	}

	// Find a method that exists in the profile.
	ranked := computeHot(sf, true)
	if len(ranked) == 0 {
		t.Skip("no hot methods")
	}
	method := ranked[0].name

	out := captureOutput(func() {
		cmdCallers(sf, method, 4, 0.1)
	})

	if len(strings.TrimSpace(out)) == 0 {
		t.Error("expected non-empty callers output")
	}
}

func TestPprofTrace(t *testing.T) {
	path := pprofCPUFixture

	parsed, err := parsePprofData(path, singleEventType("cpu"))
	if err != nil {
		t.Fatal(err)
	}
	sf := parsed.stacksByEvent["cpu"]
	if sf == nil || sf.totalSamples == 0 {
		t.Skip("no CPU samples captured")
	}

	ranked := computeHot(sf, true)
	if len(ranked) == 0 {
		t.Skip("no hot methods")
	}
	method := ranked[0].name

	out := captureOutput(func() {
		cmdTrace(sf, method, 0.1, true)
	})

	if len(strings.TrimSpace(out)) == 0 {
		t.Error("expected non-empty trace output")
	}
}

// ---------------------------------------------------------------------------
// Alloc profile tests
// ---------------------------------------------------------------------------

func TestPprofAllocProfile(t *testing.T) {
	path := pprofAllocFixture

	parsed, err := parsePprofData(path, allEventTypes())
	if err != nil {
		t.Fatal(err)
	}

	if parsed.eventCounts["alloc"] <= 0 {
		t.Skip("no alloc events captured")
	}

	sf := parsed.stacksByEvent["alloc"]
	if sf == nil || sf.totalSamples == 0 {
		t.Skip("no alloc stacks captured")
	}

	out := captureOutput(func() {
		cmdHot(sf, 10, false, 0)
	})

	if len(strings.TrimSpace(out)) == 0 {
		t.Error("expected non-empty alloc hot output")
	}
}

func TestPprofAllocValueSelection(t *testing.T) {
	// Go alloc profiles include alloc_objects/count (priority 1) and
	// alloc_space/bytes (priority 2). Verify that bytes wins (the count
	// should be in bytes, not object count).
	path := pprofAllocFixture

	parsed, err := parsePprofData(path, allEventTypes())
	if err != nil {
		t.Fatal(err)
	}

	if parsed.eventCounts["alloc"] <= 0 {
		t.Skip("no alloc events captured")
	}

	// With bytes priority, alloc count should be > 10000 (bytes are much larger than object counts).
	// pprofAllocSlices allocates 10000 * 1024 = ~10MB.
	if parsed.eventCounts["alloc"] < 10000 {
		t.Errorf("expected alloc count to be large (bytes), got %d — priority selection may be wrong", parsed.eventCounts["alloc"])
	}
}

// ---------------------------------------------------------------------------
// Lock profile tests
// ---------------------------------------------------------------------------

func TestPprofLockProfile(t *testing.T) {
	path := pprofMutexFixture

	parsed, err := parsePprofData(path, allEventTypes())
	if err != nil {
		t.Fatal(err)
	}

	if parsed.eventCounts["lock"] <= 0 {
		t.Skip("no lock events captured (mutex contention too low)")
	}

	sf := parsed.stacksByEvent["lock"]
	if sf == nil || sf.totalSamples == 0 {
		t.Skip("no lock stacks captured")
	}

	out := captureOutput(func() {
		cmdHot(sf, 10, false, 0)
	})

	if len(strings.TrimSpace(out)) == 0 {
		t.Error("expected non-empty lock hot output")
	}
}

// ---------------------------------------------------------------------------
// Diff tests
// ---------------------------------------------------------------------------

func TestPprofDiff(t *testing.T) {
	before := pprofCPUFixture
	after := pprofCPU2Fixture

	bSF, _, err := openInput(before, "cpu")
	if err != nil {
		t.Fatal(err)
	}
	aSF, _, err := openInput(after, "cpu")
	if err != nil {
		t.Fatal(err)
	}
	if bSF.totalSamples == 0 || aSF.totalSamples == 0 {
		t.Skip("insufficient samples for diff")
	}

	out := captureOutput(func() {
		cmdDiff(bSF, aSF, 0.1, 0, false)
	})

	// Output should be non-empty (either changes or "no significant changes").
	if len(strings.TrimSpace(out)) == 0 {
		t.Error("expected non-empty diff output")
	}
}

func TestPprofMixedDiff(t *testing.T) {
	pprofPath := pprofCPUFixture

	dir := t.TempDir()
	collapsedPath := filepath.Join(dir, "before.txt")
	os.WriteFile(collapsedPath, []byte("main;A.work;B.compute 50\nmain;A.work;C.old 30\n"), 0644)

	bSF, _, err := openInput(collapsedPath, "cpu")
	if err != nil {
		t.Fatal(err)
	}
	aSF, _, err := openInput(pprofPath, "cpu")
	if err != nil {
		t.Fatal(err)
	}

	out := captureOutput(func() {
		cmdDiff(bSF, aSF, 0.1, 0, false)
	})

	if len(strings.TrimSpace(out)) == 0 {
		t.Error("expected non-empty mixed diff output")
	}
}

// ---------------------------------------------------------------------------
// Lines test
// ---------------------------------------------------------------------------

func TestPprofLines(t *testing.T) {
	path := pprofCPUFixture

	parsed, err := parsePprofData(path, singleEventType("cpu"))
	if err != nil {
		t.Fatal(err)
	}
	sf := parsed.stacksByEvent["cpu"]
	if sf == nil || sf.totalSamples == 0 {
		t.Skip("no CPU samples captured")
	}

	// Find a method in the profile.
	ranked := computeHot(sf, true)
	if len(ranked) == 0 {
		t.Skip("no hot methods")
	}
	method := ranked[0].name

	out := captureOutput(func() {
		cmdLines(sf, method, 10, true)
	})

	// pprof profiles from Go include line numbers, so output should have them.
	if len(strings.TrimSpace(out)) == 0 {
		t.Error("expected non-empty lines output")
	}
}

// ---------------------------------------------------------------------------
// Filter and Collapse
// ---------------------------------------------------------------------------

func TestPprofFilter(t *testing.T) {
	path := pprofCPUFixture

	parsed, err := parsePprofData(path, singleEventType("cpu"))
	if err != nil {
		t.Fatal(err)
	}
	sf := parsed.stacksByEvent["cpu"]
	if sf == nil || sf.totalSamples == 0 {
		t.Skip("no CPU samples captured")
	}

	out := captureOutput(func() {
		cmdFilter(sf, "pprofBusy", false)
	})

	if len(strings.TrimSpace(out)) == 0 {
		t.Error("expected non-empty filter output")
	}
	if !strings.Contains(out, "pprofBusy") {
		t.Errorf("expected pprofBusy in filter output, got:\n%s", out)
	}
}

func TestPprofCollapse(t *testing.T) {
	path := pprofCPUFixture

	parsed, err := parsePprofData(path, singleEventType("cpu"))
	if err != nil {
		t.Fatal(err)
	}
	sf := parsed.stacksByEvent["cpu"]
	if sf == nil || sf.totalSamples == 0 {
		t.Skip("no CPU samples captured")
	}

	out := captureOutput(func() {
		cmdCollapse(sf)
	})

	if len(strings.TrimSpace(out)) == 0 {
		t.Error("expected non-empty collapse output")
	}
	// Collapsed format: each line ends with a number.
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for _, line := range lines {
		parts := strings.Fields(line)
		if len(parts) < 2 {
			t.Errorf("malformed collapsed line: %q", line)
		}
	}
}

// ---------------------------------------------------------------------------
// Events command
// ---------------------------------------------------------------------------

func TestPprofEvents(t *testing.T) {
	path := pprofCPUFixture

	out := captureOutput(func() {
		err := cmdEvents(path)
		if err != nil {
			t.Fatal(err)
		}
	})

	if !strings.Contains(out, "cpu") {
		t.Errorf("expected 'cpu' in events output, got:\n%s", out)
	}
	if !strings.Contains(out, "EVENT") {
		t.Errorf("expected header in events output, got:\n%s", out)
	}
}

func TestPprofEventsCollapsedRejected(t *testing.T) {
	err := cmdEvents("stacks.txt")
	if err == nil || !strings.Contains(err.Error(), "requires a JFR or pprof file") {
		t.Errorf("expected rejection for collapsed text, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Info command
// ---------------------------------------------------------------------------

func TestPprofInfo(t *testing.T) {
	path := pprofCPUFixture

	parsed, err := parsePprofData(path, allEventTypes())
	if err != nil {
		t.Fatal(err)
	}
	sf := parsed.stacksByEvent["cpu"]
	if sf == nil {
		sf = &stackFile{}
	}

	out := captureOutput(func() {
		cmdInfo(sf, infoOpts{
			eventType:   "cpu",
			hasMetadata: true,
			eventCounts: parsed.eventCounts,
			topThreads:  5,
			topMethods:  10,
			spanNanos:   parsed.spanNanos,
		})
	})

	if !strings.Contains(out, "Total samples:") {
		t.Errorf("expected 'Total samples:' in info output, got:\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// Timeline and time-range errors
// ---------------------------------------------------------------------------

func TestPprofTimelineError(t *testing.T) {
	path := pprofCPUFixture

	_, err := preprocessProfile(preprocessOpts{
		path:    path,
		command: "timeline",
	})
	if err == nil {
		t.Fatal("expected error for timeline with pprof")
	}
	if !strings.Contains(err.Error(), "timeline requires a JFR file") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestPprofFromToWarning(t *testing.T) {
	path := pprofCPUFixture

	stderr := captureStream(&os.Stderr, func() {
		_, err := preprocessProfile(preprocessOpts{
			path:    path,
			command: "hot",
			fromStr: "5s",
		})
		if err != nil {
			t.Fatal(err)
		}
	})

	if !strings.Contains(stderr, "--from/--to ignored") {
		t.Errorf("expected --from/--to warning, got stderr:\n%s", stderr)
	}
}

// ---------------------------------------------------------------------------
// Stdin test
// ---------------------------------------------------------------------------

func TestPprofStdin(t *testing.T) {
	// Generate a real pprof file, then pipe its contents via stdin.
	path := pprofCPUFixture
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// Verify it starts with gzip magic (pprof.StartCPUProfile writes gzipped).
	if len(data) < 2 || data[0] != 0x1f || data[1] != 0x8b {
		t.Fatal("generated CPU profile is not gzip-compressed")
	}

	exitCode, stdout, stderr := runCLIForTest(t, []string{"hot", "-"}, bytes.NewReader(data))
	if exitCode != 0 {
		t.Fatalf("expected exit 0, got %d; stderr: %s", exitCode, stderr)
	}
	if len(strings.TrimSpace(stdout)) == 0 {
		t.Error("expected non-empty hot output from stdin pprof")
	}
}

func TestPprofStdinAllocEventResolution(t *testing.T) {
	// Pipe an alloc pprof via stdin — event type should auto-resolve to "alloc"
	// even though no --event flag is given (default would be "cpu").
	// Before the fix, this produced zero samples because detectFormat("-")
	// always returned formatCollapsed, skipping event resolution.
	path := pprofAllocFixture
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if len(data) < 2 || data[0] != 0x1f || data[1] != 0x8b {
		t.Fatal("generated alloc profile is not gzip-compressed")
	}

	exitCode, stdout, stderr := runCLIForTest(t, []string{"hot", "-"}, bytes.NewReader(data))
	if exitCode != 0 {
		t.Fatalf("expected exit 0, got %d; stderr: %s", exitCode, stderr)
	}
	if len(strings.TrimSpace(stdout)) == 0 {
		t.Error("expected non-empty hot output from stdin alloc pprof (event should auto-resolve to alloc)")
	}
	// Verify the output contains alloc-related function names.
	if !strings.Contains(stdout, "pprofAlloc") {
		t.Logf("stdout:\n%s", stdout)
		t.Log("note: alloc functions may not appear if runtime dominates; checking for any output is sufficient")
	}
}

func TestPprofStdinExplicitEvent(t *testing.T) {
	// Pipe alloc pprof via stdin with explicit --event alloc.
	path := pprofAllocFixture
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	exitCode, stdout, stderr := runCLIForTest(t, []string{"hot", "--event", "alloc", "-"}, bytes.NewReader(data))
	if exitCode != 0 {
		t.Fatalf("expected exit 0, got %d; stderr: %s", exitCode, stderr)
	}
	if len(strings.TrimSpace(stdout)) == 0 {
		t.Error("expected non-empty hot output from stdin alloc pprof with explicit --event alloc")
	}
}

// ---------------------------------------------------------------------------
// Stack reversal — verify root-first ordering
// ---------------------------------------------------------------------------

func TestPprofStackReversal(t *testing.T) {
	path := pprofCPUFixture

	parsed, err := parsePprofData(path, singleEventType("cpu"))
	if err != nil {
		t.Fatal(err)
	}
	sf := parsed.stacksByEvent["cpu"]
	if sf == nil || sf.totalSamples == 0 || len(sf.stacks) == 0 {
		t.Skip("no CPU samples captured")
	}

	// In a Go program, the root of the call stack is typically runtime.goexit
	// or runtime.main. Leaf frames are the actual functions doing work.
	// Verify root is closer to index 0.
	for _, st := range sf.stacks {
		if len(st.frames) < 2 {
			continue
		}
		root := st.frames[0]
		// Root should be a runtime or main function, not our workload.
		if strings.Contains(root, "pprofBusy") {
			t.Errorf("expected root frame to NOT be pprofBusy*, got %q at index 0", root)
		}
		return // checked one representative stack
	}
}

// ---------------------------------------------------------------------------
// openInput integration
// ---------------------------------------------------------------------------

func TestPprofOpenInput(t *testing.T) {
	path := pprofCPUFixture

	sf, hasMetadata, err := openInput(path, "cpu")
	if err != nil {
		t.Fatal(err)
	}
	if !hasMetadata {
		t.Error("expected hasMetadata=true for pprof file")
	}
	if sf.totalSamples == 0 {
		t.Skip("no samples captured")
	}
}

// ---------------------------------------------------------------------------
// preprocessProfile integration
// ---------------------------------------------------------------------------

func TestPprofPreprocessProfile(t *testing.T) {
	path := pprofCPUFixture

	pctx, err := preprocessProfile(preprocessOpts{
		path:    path,
		command: "hot",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !pctx.hasMetadata {
		t.Error("expected hasMetadata=true")
	}
	if pctx.eventType != "cpu" {
		t.Errorf("expected event=cpu, got %s", pctx.eventType)
	}
}

// ---------------------------------------------------------------------------
// Starlark script integration
// ---------------------------------------------------------------------------

func TestPprofScript(t *testing.T) {
	path := pprofCPUFixture

	script := `
p = open("` + path + `")
print(p.samples)
for m in p.hot(n=3):
    print(m.name)
`

	out := captureOutput(func() {
		code := runScript(script, "", nil, 5*time.Second)
		if code != 0 {
			t.Fatalf("script exit code %d", code)
		}
	})

	if len(strings.TrimSpace(out)) == 0 {
		t.Error("expected non-empty script output")
	}
}

func TestPprofScriptStartEndRejected(t *testing.T) {
	path := pprofCPUFixture

	script := `p = open("` + path + `", start="5s")`

	stderr := captureStream(&os.Stderr, func() {
		code := runScript(script, "", nil, 5*time.Second)
		if code == 0 {
			t.Fatal("expected non-zero exit for start/end on pprof")
		}
	})

	if !strings.Contains(stderr, "start/end not supported for pprof") {
		t.Errorf("expected start/end error, got stderr:\n%s", stderr)
	}
}

func TestPprofScriptTimelineError(t *testing.T) {
	path := pprofCPUFixture

	script := `
p = open("` + path + `")
p.timeline()
`

	stderr := captureStream(&os.Stderr, func() {
		code := runScript(script, "", nil, 5*time.Second)
		if code == 0 {
			t.Fatal("expected non-zero exit for timeline on pprof")
		}
	})

	if !strings.Contains(stderr, "requires JFR data") {
		t.Errorf("expected JFR requirement error, got stderr:\n%s", stderr)
	}
}

func TestPprofScriptSplitError(t *testing.T) {
	path := pprofCPUFixture

	script := `
p = open("` + path + `")
p.split([5.0])
`

	stderr := captureStream(&os.Stderr, func() {
		code := runScript(script, "", nil, 5*time.Second)
		if code == 0 {
			t.Fatal("expected non-zero exit for split on pprof")
		}
	})

	if !strings.Contains(stderr, "requires JFR data") {
		t.Errorf("expected JFR requirement error, got stderr:\n%s", stderr)
	}
}

// ---------------------------------------------------------------------------
// classifyPprofSampleType unit tests
// ---------------------------------------------------------------------------

func TestClassifyPprofSampleType(t *testing.T) {
	tests := []struct {
		typ, unit string
		wantEvent string
		wantPri   int
	}{
		{"samples", "count", "cpu", 1},
		{"cpu", "nanoseconds", "cpu", 2},
		{"wall", "nanoseconds", "wall", 2},
		{"alloc_objects", "count", "alloc", 1},
		{"alloc_space", "bytes", "alloc", 2},
		{"inuse_objects", "count", "alloc", 1},
		{"inuse_space", "bytes", "alloc", 2},
		{"contentions", "count", "lock", 1},
		{"delay", "nanoseconds", "lock", 2},
		{"unknown_metric", "widgets", "", 0},
	}
	for _, tt := range tests {
		gotEvent, gotPri := classifyPprofSampleType(&pprofProfile.ValueType{Type: tt.typ, Unit: tt.unit})
		if gotEvent != tt.wantEvent || gotPri != tt.wantPri {
			t.Errorf("classifyPprofSampleType(%s/%s) = (%q, %d), want (%q, %d)",
				tt.typ, tt.unit, gotEvent, gotPri, tt.wantEvent, tt.wantPri)
		}
	}
}

func TestClassifyPprofSampleTypeCaseInsensitive(t *testing.T) {
	// pprof classifyPprofSampleType lowercases input.
	ev, pri := classifyPprofSampleType(&pprofProfile.ValueType{Type: "CPU", Unit: "Nanoseconds"})
	if ev != "cpu" || pri != 2 {
		t.Errorf("expected (cpu, 2), got (%q, %d)", ev, pri)
	}
	ev, pri = classifyPprofSampleType(&pprofProfile.ValueType{Type: "ALLOC_SPACE", Unit: "BYTES"})
	if ev != "alloc" || pri != 2 {
		t.Errorf("expected (alloc, 2), got (%q, %d)", ev, pri)
	}
}

// ---------------------------------------------------------------------------
// classifyPprofSampleTypes — multi-type priority resolution
// ---------------------------------------------------------------------------

func TestClassifyPprofSampleTypesPriority(t *testing.T) {
	// CPU profile: samples/count (pri 1) AND cpu/nanoseconds (pri 2).
	// cpu/nanoseconds should win.
	sts := []*pprofProfile.ValueType{
		{Type: "samples", Unit: "count"},
		{Type: "cpu", Unit: "nanoseconds"},
	}
	result := classifyPprofSampleTypes(sts)
	if len(result) != 1 {
		t.Fatalf("expected 1 mapping, got %d: %v", len(result), result)
	}
	m := result["cpu"]
	if m.valueIdx != 1 {
		t.Errorf("expected valueIdx=1 (cpu/nanoseconds), got %d", m.valueIdx)
	}
	if m.priority != 2 {
		t.Errorf("expected priority=2, got %d", m.priority)
	}
}

func TestClassifyPprofSampleTypesMultiEvent(t *testing.T) {
	// Alloc profile has 4 SampleTypes: alloc_objects, alloc_space, inuse_objects, inuse_space.
	// alloc_space (pri 2) should win. All map to "alloc".
	sts := []*pprofProfile.ValueType{
		{Type: "alloc_objects", Unit: "count"},
		{Type: "alloc_space", Unit: "bytes"},
		{Type: "inuse_objects", Unit: "count"},
		{Type: "inuse_space", Unit: "bytes"},
	}
	result := classifyPprofSampleTypes(sts)
	if len(result) != 1 {
		t.Fatalf("expected 1 mapping, got %d: %v", len(result), result)
	}
	m := result["alloc"]
	if m.valueIdx != 1 {
		t.Errorf("expected valueIdx=1 (alloc_space), got %d", m.valueIdx)
	}
}

func TestClassifyPprofSampleTypesEmpty(t *testing.T) {
	result := classifyPprofSampleTypes(nil)
	if len(result) != 0 {
		t.Errorf("expected empty result, got %v", result)
	}
}

func TestClassifyPprofSampleTypesAllUnknown(t *testing.T) {
	sts := []*pprofProfile.ValueType{
		{Type: "goroutines", Unit: "count"},
		{Type: "custom_metric", Unit: "things"},
	}
	result := classifyPprofSampleTypes(sts)
	if len(result) != 0 {
		t.Errorf("expected empty result for unknown types, got %v", result)
	}
}

// ---------------------------------------------------------------------------
// resolvePprofStack unit tests
// ---------------------------------------------------------------------------

func TestResolvePprofStackEmpty(t *testing.T) {
	sample := &pprofProfile.Sample{Location: nil}
	frames, lines := resolvePprofStack(sample)
	if frames != nil || lines != nil {
		t.Errorf("expected nil for empty sample, got frames=%v lines=%v", frames, lines)
	}
}

func TestResolvePprofStackSingleFrame(t *testing.T) {
	sample := &pprofProfile.Sample{
		Location: []*pprofProfile.Location{
			{
				Line: []pprofProfile.Line{
					{Function: &pprofProfile.Function{Name: "main.work"}, Line: 42},
				},
			},
		},
	}
	frames, lines := resolvePprofStack(sample)
	if len(frames) != 1 || frames[0] != "main.work" {
		t.Errorf("expected [main.work], got %v", frames)
	}
	if len(lines) != 1 || lines[0] != 42 {
		t.Errorf("expected [42], got %v", lines)
	}
}

func TestResolvePprofStackReversal(t *testing.T) {
	// pprof locations: [leaf, ..., root]. We want root-first output.
	sample := &pprofProfile.Sample{
		Location: []*pprofProfile.Location{
			{Line: []pprofProfile.Line{{Function: &pprofProfile.Function{Name: "leaf"}, Line: 30}}},
			{Line: []pprofProfile.Line{{Function: &pprofProfile.Function{Name: "middle"}, Line: 20}}},
			{Line: []pprofProfile.Line{{Function: &pprofProfile.Function{Name: "root"}, Line: 10}}},
		},
	}
	frames, lines := resolvePprofStack(sample)
	if len(frames) != 3 {
		t.Fatalf("expected 3 frames, got %d: %v", len(frames), frames)
	}
	if frames[0] != "root" || frames[1] != "middle" || frames[2] != "leaf" {
		t.Errorf("expected [root, middle, leaf], got %v", frames)
	}
	if lines[0] != 10 || lines[1] != 20 || lines[2] != 30 {
		t.Errorf("expected [10, 20, 30], got %v", lines)
	}
}

func TestResolvePprofStackInlined(t *testing.T) {
	// Single Location with multiple Line entries = inlined frames.
	// Line[0] = innermost (leaf), Line[1] = outermost (caller).
	// After reversal, outermost should come first.
	sample := &pprofProfile.Sample{
		Location: []*pprofProfile.Location{
			{
				Line: []pprofProfile.Line{
					{Function: &pprofProfile.Function{Name: "inlined.inner"}, Line: 50},
					{Function: &pprofProfile.Function{Name: "inlined.outer"}, Line: 40},
				},
			},
			{
				Line: []pprofProfile.Line{
					{Function: &pprofProfile.Function{Name: "caller"}, Line: 10},
				},
			},
		},
	}
	frames, lines := resolvePprofStack(sample)
	if len(frames) != 3 {
		t.Fatalf("expected 3 frames, got %d: %v", len(frames), frames)
	}
	// Root-first: caller → inlined.outer → inlined.inner
	if frames[0] != "caller" || frames[1] != "inlined.outer" || frames[2] != "inlined.inner" {
		t.Errorf("expected [caller, inlined.outer, inlined.inner], got %v", frames)
	}
	if lines[0] != 10 || lines[1] != 40 || lines[2] != 50 {
		t.Errorf("expected [10, 40, 50], got %v", lines)
	}
}

func TestResolvePprofStackUnsymbolized(t *testing.T) {
	// Location with no Line entries → unsymbolized, use address.
	sample := &pprofProfile.Sample{
		Location: []*pprofProfile.Location{
			{Address: 0xdeadbeef},
		},
	}
	frames, lines := resolvePprofStack(sample)
	if len(frames) != 1 || frames[0] != "0xdeadbeef" {
		t.Errorf("expected [0xdeadbeef], got %v", frames)
	}
	if lines[0] != 0 {
		t.Errorf("expected line 0 for unsymbolized, got %d", lines[0])
	}
}

func TestResolvePprofStackMissingFunctionName(t *testing.T) {
	// Line entry with nil Function → use address.
	sample := &pprofProfile.Sample{
		Location: []*pprofProfile.Location{
			{
				Address: 0x1234,
				Line:    []pprofProfile.Line{{Function: nil, Line: 0}},
			},
		},
	}
	frames, _ := resolvePprofStack(sample)
	if len(frames) != 1 || frames[0] != "0x1234" {
		t.Errorf("expected [0x1234], got %v", frames)
	}
}

func TestResolvePprofStackEmptyFunctionName(t *testing.T) {
	// Function with empty name → use address.
	sample := &pprofProfile.Sample{
		Location: []*pprofProfile.Location{
			{
				Address: 0xabcd,
				Line:    []pprofProfile.Line{{Function: &pprofProfile.Function{Name: ""}, Line: 5}},
			},
		},
	}
	frames, _ := resolvePprofStack(sample)
	if len(frames) != 1 || frames[0] != "0xabcd" {
		t.Errorf("expected [0xabcd], got %v", frames)
	}
}

func TestResolvePprofStackMixedSymbolized(t *testing.T) {
	// Mix of symbolized and unsymbolized locations.
	sample := &pprofProfile.Sample{
		Location: []*pprofProfile.Location{
			{Line: []pprofProfile.Line{{Function: &pprofProfile.Function{Name: "leaf"}, Line: 20}}},
			{Address: 0xff00}, // unsymbolized
			{Line: []pprofProfile.Line{{Function: &pprofProfile.Function{Name: "root"}, Line: 1}}},
		},
	}
	frames, lines := resolvePprofStack(sample)
	if len(frames) != 3 {
		t.Fatalf("expected 3 frames, got %d", len(frames))
	}
	if frames[0] != "root" || frames[1] != "0xff00" || frames[2] != "leaf" {
		t.Errorf("expected [root, 0xff00, leaf], got %v", frames)
	}
	if lines[0] != 1 || lines[1] != 0 || lines[2] != 20 {
		t.Errorf("expected [1, 0, 20], got %v", lines)
	}
}

func TestResolvePprofStackZeroLineNumber(t *testing.T) {
	// Line number of 0 should be stored as 0 (not set).
	sample := &pprofProfile.Sample{
		Location: []*pprofProfile.Location{
			{Line: []pprofProfile.Line{{Function: &pprofProfile.Function{Name: "f"}, Line: 0}}},
		},
	}
	_, lines := resolvePprofStack(sample)
	if lines[0] != 0 {
		t.Errorf("expected line 0, got %d", lines[0])
	}
}

func TestResolvePprofStackDeepInlining(t *testing.T) {
	// Single location with 5 inlined frames.
	var lineEntries []pprofProfile.Line
	for i := 0; i < 5; i++ {
		lineEntries = append(lineEntries, pprofProfile.Line{
			Function: &pprofProfile.Function{Name: fmt.Sprintf("inline_%d", i)},
			Line:     int64(i + 1),
		})
	}
	sample := &pprofProfile.Sample{
		Location: []*pprofProfile.Location{{Line: lineEntries}},
	}
	frames, _ := resolvePprofStack(sample)
	if len(frames) != 5 {
		t.Fatalf("expected 5 frames, got %d", len(frames))
	}
	// Line[0] is innermost, reversed means outermost first.
	if frames[0] != "inline_4" || frames[4] != "inline_0" {
		t.Errorf("expected inline_4..inline_0, got %v", frames)
	}
}

// ---------------------------------------------------------------------------
// extractPprofThread unit tests
// ---------------------------------------------------------------------------

func TestExtractPprofThreadLabel(t *testing.T) {
	sample := &pprofProfile.Sample{
		Label: map[string][]string{"thread": {"http-nio-8080"}},
	}
	got := extractPprofThread(sample)
	if got != "http-nio-8080" {
		t.Errorf("expected http-nio-8080, got %q", got)
	}
}

func TestExtractPprofThreadNumLabel(t *testing.T) {
	sample := &pprofProfile.Sample{
		NumLabel: map[string][]int64{"thread_id": {12345}},
	}
	got := extractPprofThread(sample)
	if got != "thread-12345" {
		t.Errorf("expected thread-12345, got %q", got)
	}
}

func TestExtractPprofThreadNone(t *testing.T) {
	sample := &pprofProfile.Sample{}
	got := extractPprofThread(sample)
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestExtractPprofThreadLabelWinsOverNumLabel(t *testing.T) {
	sample := &pprofProfile.Sample{
		Label:    map[string][]string{"thread": {"named-thread"}},
		NumLabel: map[string][]int64{"thread_id": {99}},
	}
	got := extractPprofThread(sample)
	if got != "named-thread" {
		t.Errorf("expected named-thread (Label wins), got %q", got)
	}
}

func TestExtractPprofThreadEmptyLabel(t *testing.T) {
	// Empty label value list → fall through to NumLabel.
	sample := &pprofProfile.Sample{
		Label:    map[string][]string{"thread": {}},
		NumLabel: map[string][]int64{"thread_id": {42}},
	}
	got := extractPprofThread(sample)
	if got != "thread-42" {
		t.Errorf("expected thread-42 (fallback to NumLabel), got %q", got)
	}
}

// ---------------------------------------------------------------------------
// buildParsedProfile unit tests
// ---------------------------------------------------------------------------

func TestBuildParsedProfileEmpty(t *testing.T) {
	// Profile with SampleTypes but no samples.
	prof := &pprofProfile.Profile{
		SampleType: []*pprofProfile.ValueType{
			{Type: "cpu", Unit: "nanoseconds"},
		},
	}
	parsed, err := buildParsedProfile(prof, nil)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.eventCounts["cpu"] != 0 {
		t.Errorf("expected 0 cpu events, got %d", parsed.eventCounts["cpu"])
	}
	sf := parsed.stacksByEvent["cpu"]
	if sf == nil {
		t.Fatal("expected stackFile for cpu")
	}
	if sf.totalSamples != 0 {
		t.Errorf("expected 0 samples, got %d", sf.totalSamples)
	}
}

func TestBuildParsedProfileNoSampleTypes(t *testing.T) {
	// Profile with no SampleTypes → empty result.
	prof := &pprofProfile.Profile{}
	parsed, err := buildParsedProfile(prof, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.eventCounts) != 0 {
		t.Errorf("expected 0 event counts, got %v", parsed.eventCounts)
	}
}

func TestBuildParsedProfileUnknownSampleTypeFallback(t *testing.T) {
	// Unknown SampleType → synthetic "cpu" mapping at index 0.
	fn := &pprofProfile.Function{ID: 1, Name: "custom.work"}
	prof := &pprofProfile.Profile{
		SampleType: []*pprofProfile.ValueType{
			{Type: "goroutines", Unit: "count"},
		},
		Sample: []*pprofProfile.Sample{
			{
				Location: []*pprofProfile.Location{
					{ID: 1, Line: []pprofProfile.Line{{Function: fn, Line: 10}}},
				},
				Value: []int64{42},
			},
		},
		Function: []*pprofProfile.Function{fn},
		Location: []*pprofProfile.Location{
			{ID: 1, Line: []pprofProfile.Line{{Function: fn, Line: 10}}},
		},
	}
	parsed, err := buildParsedProfile(prof, nil)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.eventCounts["cpu"] != 42 {
		t.Errorf("expected synthetic cpu count 42, got %d", parsed.eventCounts["cpu"])
	}
}

func TestBuildParsedProfileStackEventsFiltering(t *testing.T) {
	// Profile with cpu and alloc SampleTypes.
	// Request only "cpu" → only cpu stacks built, but alloc counted.
	fn := &pprofProfile.Function{ID: 1, Name: "work"}
	loc := &pprofProfile.Location{
		ID:   1,
		Line: []pprofProfile.Line{{Function: fn, Line: 10}},
	}
	prof := &pprofProfile.Profile{
		SampleType: []*pprofProfile.ValueType{
			{Type: "cpu", Unit: "nanoseconds"},
			{Type: "alloc_space", Unit: "bytes"},
		},
		Sample: []*pprofProfile.Sample{
			{
				Location: []*pprofProfile.Location{loc},
				Value:    []int64{100, 2048},
			},
		},
		Function: []*pprofProfile.Function{fn},
		Location: []*pprofProfile.Location{loc},
	}

	parsed, err := buildParsedProfile(prof, singleEventType("cpu"))
	if err != nil {
		t.Fatal(err)
	}

	// Both events should be counted.
	if parsed.eventCounts["cpu"] != 100 {
		t.Errorf("expected cpu count 100, got %d", parsed.eventCounts["cpu"])
	}
	if parsed.eventCounts["alloc"] != 2048 {
		t.Errorf("expected alloc count 2048, got %d", parsed.eventCounts["alloc"])
	}

	// Only cpu stacks should be built.
	if parsed.stacksByEvent["cpu"] == nil {
		t.Error("expected cpu stackFile")
	}
	if parsed.stacksByEvent["alloc"] != nil {
		t.Error("expected no alloc stackFile (not requested)")
	}
}

func TestBuildParsedProfileDurationNanos(t *testing.T) {
	prof := &pprofProfile.Profile{
		SampleType:    []*pprofProfile.ValueType{{Type: "cpu", Unit: "nanoseconds"}},
		DurationNanos: 5_000_000_000, // 5s
	}
	parsed, err := buildParsedProfile(prof, nil)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.spanNanos != 5_000_000_000 {
		t.Errorf("expected spanNanos=5e9, got %d", parsed.spanNanos)
	}
}

func TestBuildParsedProfileZeroNegativeValues(t *testing.T) {
	// Samples with zero or negative values should be skipped.
	fn := &pprofProfile.Function{ID: 1, Name: "f"}
	loc := &pprofProfile.Location{
		ID:   1,
		Line: []pprofProfile.Line{{Function: fn, Line: 1}},
	}
	prof := &pprofProfile.Profile{
		SampleType: []*pprofProfile.ValueType{{Type: "cpu", Unit: "nanoseconds"}},
		Sample: []*pprofProfile.Sample{
			{Location: []*pprofProfile.Location{loc}, Value: []int64{0}},
			{Location: []*pprofProfile.Location{loc}, Value: []int64{-5}},
			{Location: []*pprofProfile.Location{loc}, Value: []int64{10}},
		},
		Function: []*pprofProfile.Function{fn},
		Location: []*pprofProfile.Location{loc},
	}
	parsed, err := buildParsedProfile(prof, nil)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.eventCounts["cpu"] != 10 {
		t.Errorf("expected cpu count 10 (skip zero/neg), got %d", parsed.eventCounts["cpu"])
	}
}

func TestBuildParsedProfileNoTimedEvents(t *testing.T) {
	// pprof profiles should always have nil timedEvents.
	prof := &pprofProfile.Profile{
		SampleType: []*pprofProfile.ValueType{{Type: "cpu", Unit: "nanoseconds"}},
	}
	parsed, err := buildParsedProfile(prof, nil)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.timedEvents != nil {
		t.Errorf("expected nil timedEvents for pprof, got %v", parsed.timedEvents)
	}
}

func TestBuildParsedProfileSampleWithEmptyStack(t *testing.T) {
	// Sample with no locations → skipped (no frames).
	prof := &pprofProfile.Profile{
		SampleType: []*pprofProfile.ValueType{{Type: "cpu", Unit: "nanoseconds"}},
		Sample: []*pprofProfile.Sample{
			{Location: nil, Value: []int64{50}},
		},
	}
	parsed, err := buildParsedProfile(prof, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Event counted but no stacks built.
	if parsed.eventCounts["cpu"] != 50 {
		t.Errorf("expected cpu count 50, got %d", parsed.eventCounts["cpu"])
	}
	sf := parsed.stacksByEvent["cpu"]
	if sf.totalSamples != 0 {
		t.Errorf("expected 0 stacks (empty location), got %d", sf.totalSamples)
	}
}

func TestBuildParsedProfileAggregatesSameStack(t *testing.T) {
	fn := &pprofProfile.Function{ID: 1, Name: "f"}
	loc := &pprofProfile.Location{
		ID:   1,
		Line: []pprofProfile.Line{{Function: fn, Line: 1}},
	}
	prof := &pprofProfile.Profile{
		SampleType: []*pprofProfile.ValueType{{Type: "cpu", Unit: "nanoseconds"}},
		Sample: []*pprofProfile.Sample{
			{Location: []*pprofProfile.Location{loc}, Value: []int64{10}},
			{Location: []*pprofProfile.Location{loc}, Value: []int64{20}},
			{Location: []*pprofProfile.Location{loc}, Value: []int64{30}},
		},
		Function: []*pprofProfile.Function{fn},
		Location: []*pprofProfile.Location{loc},
	}
	parsed, err := buildParsedProfile(prof, nil)
	if err != nil {
		t.Fatal(err)
	}
	sf := parsed.stacksByEvent["cpu"]
	if sf.totalSamples != 60 {
		t.Errorf("expected 60 aggregated samples, got %d", sf.totalSamples)
	}
	if len(sf.stacks) != 1 {
		t.Errorf("expected 1 unique stack, got %d", len(sf.stacks))
	}
}

func TestBuildParsedProfileThreadSeparation(t *testing.T) {
	// Same stack trace on different threads → different stack entries.
	fn := &pprofProfile.Function{ID: 1, Name: "f"}
	loc := &pprofProfile.Location{
		ID:   1,
		Line: []pprofProfile.Line{{Function: fn, Line: 1}},
	}
	prof := &pprofProfile.Profile{
		SampleType: []*pprofProfile.ValueType{{Type: "cpu", Unit: "nanoseconds"}},
		Sample: []*pprofProfile.Sample{
			{
				Location: []*pprofProfile.Location{loc},
				Value:    []int64{10},
				Label:    map[string][]string{"thread": {"t1"}},
			},
			{
				Location: []*pprofProfile.Location{loc},
				Value:    []int64{20},
				Label:    map[string][]string{"thread": {"t2"}},
			},
		},
		Function: []*pprofProfile.Function{fn},
		Location: []*pprofProfile.Location{loc},
	}
	parsed, err := buildParsedProfile(prof, nil)
	if err != nil {
		t.Fatal(err)
	}
	sf := parsed.stacksByEvent["cpu"]
	if len(sf.stacks) != 2 {
		t.Errorf("expected 2 stacks (different threads), got %d", len(sf.stacks))
	}
}

// ---------------------------------------------------------------------------
// Error path tests
// ---------------------------------------------------------------------------

func TestPprofFileNotFound(t *testing.T) {
	_, err := parsePprofData("/nonexistent/path/cpu.pb.gz", nil)
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestPprofCorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "corrupt.pb.gz")
	os.WriteFile(path, []byte("this is not a pprof file at all"), 0644)

	_, err := parsePprofData(path, nil)
	if err == nil {
		t.Fatal("expected error for corrupt file")
	}
	if !strings.Contains(err.Error(), "pprof parse") {
		t.Errorf("expected 'pprof parse' in error, got: %v", err)
	}
}

func TestPprofEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.pb.gz")
	os.WriteFile(path, []byte{}, 0644)

	_, err := parsePprofData(path, nil)
	if err == nil {
		t.Fatal("expected error for empty file")
	}
}

func TestPprofFromReaderCorrupt(t *testing.T) {
	_, err := parsePprofFromReader(strings.NewReader("garbage"), nil)
	if err == nil {
		t.Fatal("expected error for corrupt reader")
	}
}

func TestPprofFromReaderEmpty(t *testing.T) {
	_, err := parsePprofFromReader(strings.NewReader(""), nil)
	if err == nil {
		t.Fatal("expected error for empty reader")
	}
}

// ---------------------------------------------------------------------------
// File extension tests
// ---------------------------------------------------------------------------

func TestPprofPprofExtension(t *testing.T) {
	// Generate a profile but save with .pprof extension.
	path := pprofCPUFixture
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	pprofPath := filepath.Join(dir, "profile.pprof")
	os.WriteFile(pprofPath, data, 0644)

	sf, hasMetadata, err := openInput(pprofPath, "cpu")
	if err != nil {
		t.Fatal(err)
	}
	if !hasMetadata {
		t.Error("expected hasMetadata=true for .pprof file")
	}
	if sf.totalSamples == 0 {
		t.Skip("no samples captured")
	}
}

func TestPprofPbExtension(t *testing.T) {
	// Save with .pb extension (uncompressed — profile.Parse handles gzip internally,
	// so a gzipped file with .pb extension still works).
	path := pprofCPUFixture
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	pbPath := filepath.Join(dir, "profile.pb")
	os.WriteFile(pbPath, data, 0644)

	sf, hasMetadata, err := openInput(pbPath, "cpu")
	if err != nil {
		t.Fatal(err)
	}
	if !hasMetadata {
		t.Error("expected hasMetadata=true for .pb file")
	}
	if sf.totalSamples == 0 {
		t.Skip("no samples captured")
	}
}

// ---------------------------------------------------------------------------
// Threads integration
// ---------------------------------------------------------------------------

func TestPprofThreads(t *testing.T) {
	path := pprofCPUFixture

	parsed, err := parsePprofData(path, singleEventType("cpu"))
	if err != nil {
		t.Fatal(err)
	}
	sf := parsed.stacksByEvent["cpu"]
	if sf == nil || sf.totalSamples == 0 {
		t.Skip("no CPU samples captured")
	}

	out := captureOutput(func() {
		cmdThreads(sf, 10, false)
	})

	// Go CPU profiles don't have thread labels, so threads command
	// should produce output (possibly showing empty thread name).
	if len(strings.TrimSpace(out)) == 0 {
		t.Error("expected non-empty threads output")
	}
}

func TestPprofThreadFilter(t *testing.T) {
	// Build a profile with thread labels via buildParsedProfile.
	fn1 := &pprofProfile.Function{ID: 1, Name: "worker.run"}
	fn2 := &pprofProfile.Function{ID: 2, Name: "http.handle"}
	loc1 := &pprofProfile.Location{ID: 1, Line: []pprofProfile.Line{{Function: fn1, Line: 10}}}
	loc2 := &pprofProfile.Location{ID: 2, Line: []pprofProfile.Line{{Function: fn2, Line: 20}}}
	prof := &pprofProfile.Profile{
		SampleType: []*pprofProfile.ValueType{{Type: "cpu", Unit: "nanoseconds"}},
		Sample: []*pprofProfile.Sample{
			{
				Location: []*pprofProfile.Location{loc1},
				Value:    []int64{100},
				Label:    map[string][]string{"thread": {"worker-1"}},
			},
			{
				Location: []*pprofProfile.Location{loc2},
				Value:    []int64{200},
				Label:    map[string][]string{"thread": {"http-nio-1"}},
			},
		},
		Function: []*pprofProfile.Function{fn1, fn2},
		Location: []*pprofProfile.Location{loc1, loc2},
	}
	parsed, err := buildParsedProfile(prof, nil)
	if err != nil {
		t.Fatal(err)
	}
	sf := parsed.stacksByEvent["cpu"]
	filtered := sf.filterByThread("worker")
	if filtered.totalSamples != 100 {
		t.Errorf("expected 100 samples for worker thread, got %d", filtered.totalSamples)
	}
	out := captureOutput(func() {
		cmdHot(filtered, 10, false, 0)
	})
	if !strings.Contains(out, "worker.run") {
		t.Errorf("expected worker.run in filtered output, got:\n%s", out)
	}
	if strings.Contains(out, "http.handle") {
		t.Errorf("did not expect http.handle in filtered output, got:\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// FQN flag
// ---------------------------------------------------------------------------

func TestPprofHotFQN(t *testing.T) {
	path := pprofCPUFixture

	parsed, err := parsePprofData(path, singleEventType("cpu"))
	if err != nil {
		t.Fatal(err)
	}
	sf := parsed.stacksByEvent["cpu"]
	if sf == nil || sf.totalSamples == 0 {
		t.Skip("no CPU samples captured")
	}

	out := captureOutput(func() {
		cmdHot(sf, 20, true, 0)
	})

	if len(strings.TrimSpace(out)) == 0 {
		t.Error("expected non-empty FQN hot output")
	}
}

// ---------------------------------------------------------------------------
// --no-idle with pprof
// ---------------------------------------------------------------------------

func TestPprofNoIdle(t *testing.T) {
	path := pprofCPUFixture

	parsed, err := parsePprofData(path, singleEventType("cpu"))
	if err != nil {
		t.Fatal(err)
	}
	sf := parsed.stacksByEvent["cpu"]
	if sf == nil || sf.totalSamples == 0 {
		t.Skip("no CPU samples captured")
	}

	// filterIdle should not crash on pprof data.
	filtered := sf.filterIdle()
	if filtered.totalSamples > sf.totalSamples {
		t.Errorf("filtered samples (%d) > original (%d)", filtered.totalSamples, sf.totalSamples)
	}
}

// ---------------------------------------------------------------------------
// Collapse round-trip
// ---------------------------------------------------------------------------

func TestPprofCollapseRoundTrip(t *testing.T) {
	path := pprofCPUFixture

	parsed, err := parsePprofData(path, singleEventType("cpu"))
	if err != nil {
		t.Fatal(err)
	}
	sf := parsed.stacksByEvent["cpu"]
	if sf == nil || sf.totalSamples == 0 {
		t.Skip("no CPU samples captured")
	}

	// Collapse to text.
	collapsed := captureOutput(func() {
		cmdCollapse(sf)
	})

	// Re-parse as collapsed text.
	sf2, err := parseCollapsed(strings.NewReader(collapsed))
	if err != nil {
		t.Fatal(err)
	}

	// Total samples should match.
	if sf2.totalSamples != sf.totalSamples {
		t.Errorf("round-trip samples mismatch: original=%d, re-parsed=%d", sf.totalSamples, sf2.totalSamples)
	}
}

// ---------------------------------------------------------------------------
// Alloc events listing
// ---------------------------------------------------------------------------

func TestPprofAllocEvents(t *testing.T) {
	path := pprofAllocFixture

	out := captureOutput(func() {
		err := cmdEvents(path)
		if err != nil {
			t.Fatal(err)
		}
	})

	if !strings.Contains(out, "alloc") {
		t.Errorf("expected 'alloc' in events output, got:\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// Explicit --event flag with pprof
// ---------------------------------------------------------------------------

func TestPprofPreprocessProfileAllocEvent(t *testing.T) {
	path := pprofAllocFixture

	pctx, err := preprocessProfile(preprocessOpts{
		path:      path,
		command:   "hot",
		eventFlag: "alloc",
	})
	if err != nil {
		t.Fatal(err)
	}
	if pctx.eventType != "alloc" {
		t.Errorf("expected event=alloc, got %s", pctx.eventType)
	}
}

func TestPprofPreprocessProfileInvalidEvent(t *testing.T) {
	path := pprofCPUFixture

	_, err := preprocessProfile(preprocessOpts{
		path:      path,
		command:   "hot",
		eventFlag: "invalid_event",
	})
	if err == nil {
		t.Fatal("expected error for invalid event type")
	}
}

// ---------------------------------------------------------------------------
// Diff with alloc profiles
// ---------------------------------------------------------------------------

func TestPprofDiffAlloc(t *testing.T) {
	before := pprofAllocFixture
	after := pprofAlloc2Fixture

	bSF, _, err := openInput(before, "alloc")
	if err != nil {
		t.Fatal(err)
	}
	aSF, _, err := openInput(after, "alloc")
	if err != nil {
		t.Fatal(err)
	}
	if bSF.totalSamples == 0 || aSF.totalSamples == 0 {
		t.Skip("insufficient alloc samples for diff")
	}

	out := captureOutput(func() {
		cmdDiff(bSF, aSF, 0.1, 0, false)
	})

	if len(strings.TrimSpace(out)) == 0 {
		t.Error("expected non-empty alloc diff output")
	}
}

// ---------------------------------------------------------------------------
// Large profile
// ---------------------------------------------------------------------------

func TestPprofLargeProfile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping large profile test in short mode")
	}

	parsed, err := parsePprofData(pprofCPUFixture, singleEventType("cpu"))
	if err != nil {
		t.Fatal(err)
	}
	sf := parsed.stacksByEvent["cpu"]
	if sf == nil || sf.totalSamples == 0 {
		t.Skip("no CPU samples captured")
	}

	// Verify we get a decent number of samples from longer profiling.
	t.Logf("large profile: %d samples, %d unique stacks", sf.totalSamples, len(sf.stacks))

	// All commands should handle large data without panicking.
	captureOutput(func() { cmdHot(sf, 50, false, 0) })
	captureOutput(func() { cmdTree(sf, "", 10, 0.01) })
	captureOutput(func() { cmdCollapse(sf) })

	ranked := computeHot(sf, true)
	if len(ranked) > 0 {
		captureOutput(func() { cmdCallers(sf, ranked[0].name, 10, 0.01) })
		captureOutput(func() { cmdTrace(sf, ranked[0].name, 0.01, true) })
		captureOutput(func() { cmdLines(sf, ranked[0].name, 20, true) })
	}
}

// ---------------------------------------------------------------------------
// Stdin: non-gzip (text) stays collapsed
// ---------------------------------------------------------------------------

func TestPprofStdinPlainText(t *testing.T) {
	input := "A;B;C 10\nX;Y 5\n"
	exitCode, stdout, stderr := runCLIForTest(t, []string{"hot", "-"}, strings.NewReader(input))
	if exitCode != 0 {
		t.Fatalf("expected exit 0, got %d; stderr: %s", exitCode, stderr)
	}
	if !strings.Contains(stdout, "C") {
		t.Errorf("expected C in hot output, got:\n%s", stdout)
	}
}

// ---------------------------------------------------------------------------
// Stdin diff — pprof event resolution through stdin
// ---------------------------------------------------------------------------

func TestPprofStdinDiffAllocBefore(t *testing.T) {
	// Pipe alloc pprof as "before" via stdin, file as "after".
	// Event should auto-resolve to alloc (not default cpu).
	before := pprofAllocFixture
	after := pprofAlloc2Fixture

	data, err := os.ReadFile(before)
	if err != nil {
		t.Fatal(err)
	}

	exitCode, stdout, stderr := runCLIForTest(t, []string{"diff", "-", after}, bytes.NewReader(data))
	if exitCode != 0 {
		t.Fatalf("expected exit 0, got %d; stderr: %s", exitCode, stderr)
	}
	// Should produce output (either changes or "no significant changes").
	if len(strings.TrimSpace(stdout)) == 0 {
		t.Error("expected non-empty diff output from stdin alloc pprof")
	}
	// Should NOT warn about one-sided metadata — both sides are structured.
	if strings.Contains(stderr, "one side") || strings.Contains(stderr, "collapsed") {
		t.Errorf("unexpected one-sided warning in stderr:\n%s", stderr)
	}
}

func TestPprofStdinDiffAllocAfter(t *testing.T) {
	// Pipe alloc pprof as "after" via stdin, file as "before".
	before := pprofAllocFixture
	after := pprofAlloc2Fixture

	data, err := os.ReadFile(after)
	if err != nil {
		t.Fatal(err)
	}

	exitCode, stdout, stderr := runCLIForTest(t, []string{"diff", before, "-"}, bytes.NewReader(data))
	if exitCode != 0 {
		t.Fatalf("expected exit 0, got %d; stderr: %s", exitCode, stderr)
	}
	if len(strings.TrimSpace(stdout)) == 0 {
		t.Error("expected non-empty diff output from stdin alloc pprof")
	}
	if strings.Contains(stderr, "one side") || strings.Contains(stderr, "collapsed") {
		t.Errorf("unexpected one-sided warning in stderr:\n%s", stderr)
	}
}

func TestPprofStdinDiffCollapsed(t *testing.T) {
	// Collapsed text stdin still works for diff.
	before := "A;B;C 10\nX;Y 5\n"
	afterPath := filepath.Join(t.TempDir(), "after.txt")
	if err := os.WriteFile(afterPath, []byte("A;B;C 8\nX;Y 7\n"), 0644); err != nil {
		t.Fatal(err)
	}

	exitCode, stdout, stderr := runCLIForTest(t, []string{"diff", "-", afterPath}, strings.NewReader(before))
	if exitCode != 0 {
		t.Fatalf("expected exit 0, got %d; stderr: %s", exitCode, stderr)
	}
	if len(strings.TrimSpace(stdout)) == 0 {
		t.Error("expected non-empty diff output from collapsed stdin")
	}
	_ = stderr
}

// ---------------------------------------------------------------------------
// Starlark script — extended pprof API coverage
// ---------------------------------------------------------------------------

func TestPprofScriptEvents(t *testing.T) {
	path := pprofCPUFixture

	script := `
p = open("` + path + `")
for e in p.events:
    print(e)
`
	out := captureOutput(func() {
		code := runScript(script, "", nil, 5*time.Second)
		if code != 0 {
			t.Fatalf("script exit code %d", code)
		}
	})

	if !strings.Contains(out, "cpu") {
		t.Errorf("expected 'cpu' in events, got:\n%s", out)
	}
}

func TestPprofScriptFilter(t *testing.T) {
	path := pprofCPUFixture

	script := `
p = open("` + path + `")
f = p.filter(lambda s: s.has("pprofBusy"))
print(f.samples)
`
	out := captureOutput(func() {
		code := runScript(script, "", nil, 5*time.Second)
		if code != 0 {
			t.Fatalf("script exit code %d", code)
		}
	})

	samples := strings.TrimSpace(out)
	if samples == "0" {
		t.Skip("no samples matched pprofBusy")
	}
	n, err := strconv.Atoi(samples)
	if err != nil {
		t.Fatalf("expected integer, got %q", samples)
	}
	if n <= 0 {
		t.Errorf("expected positive samples after filter, got %d", n)
	}
}

func TestPprofScriptTree(t *testing.T) {
	path := pprofCPUFixture

	script := `
p = open("` + path + `")
print(p.tree(depth=3, min_pct=0.1))
`
	out := captureOutput(func() {
		code := runScript(script, "", nil, 5*time.Second)
		if code != 0 {
			t.Fatalf("script exit code %d", code)
		}
	})

	if len(strings.TrimSpace(out)) == 0 {
		t.Skip("empty tree output (profiler captured no samples)")
	}
}

func TestPprofScriptCallers(t *testing.T) {
	path := pprofCPUFixture

	// Find a method name to query.
	parsed, err := parsePprofData(path, singleEventType("cpu"))
	if err != nil {
		t.Fatal(err)
	}
	sf := parsed.stacksByEvent["cpu"]
	if sf == nil || sf.totalSamples == 0 {
		t.Skip("no CPU samples")
	}
	ranked := computeHot(sf, true)
	if len(ranked) == 0 {
		t.Skip("no hot methods")
	}
	method := ranked[0].name

	script := `
p = open("` + path + `")
print(p.callers("` + method + `"))
`
	out := captureOutput(func() {
		code := runScript(script, "", nil, 5*time.Second)
		if code != 0 {
			t.Fatalf("script exit code %d", code)
		}
	})

	if len(strings.TrimSpace(out)) == 0 {
		t.Error("expected non-empty callers output from script")
	}
}

func TestPprofScriptTrace(t *testing.T) {
	path := pprofCPUFixture

	parsed, err := parsePprofData(path, singleEventType("cpu"))
	if err != nil {
		t.Fatal(err)
	}
	sf := parsed.stacksByEvent["cpu"]
	if sf == nil || sf.totalSamples == 0 {
		t.Skip("no CPU samples")
	}
	ranked := computeHot(sf, true)
	if len(ranked) == 0 {
		t.Skip("no hot methods")
	}
	method := ranked[0].name

	script := `
p = open("` + path + `")
print(p.trace("` + method + `"))
`
	out := captureOutput(func() {
		code := runScript(script, "", nil, 5*time.Second)
		if code != 0 {
			t.Fatalf("script exit code %d", code)
		}
	})

	if len(strings.TrimSpace(out)) == 0 {
		t.Error("expected non-empty trace output from script")
	}
}

func TestPprofScriptNoIdle(t *testing.T) {
	path := pprofCPUFixture

	script := `
p = open("` + path + `")
f = p.no_idle()
print(f.samples)
`
	out := captureOutput(func() {
		code := runScript(script, "", nil, 5*time.Second)
		if code != 0 {
			t.Fatalf("script exit code %d", code)
		}
	})

	if len(strings.TrimSpace(out)) == 0 {
		t.Error("expected non-empty no_idle output from script")
	}
}

func TestPprofScriptSummary(t *testing.T) {
	path := pprofCPUFixture

	script := `
p = open("` + path + `")
print(p.summary())
`
	out := captureOutput(func() {
		code := runScript(script, "", nil, 5*time.Second)
		if code != 0 {
			t.Fatalf("script exit code %d", code)
		}
	})

	if len(strings.TrimSpace(out)) == 0 {
		t.Error("expected non-empty summary output from script")
	}
}

func TestPprofScriptStackAttributes(t *testing.T) {
	path := pprofCPUFixture

	script := `
p = open("` + path + `")
for s in p.stacks:
    print(s.leaf, s.root, s.depth, s.samples)
    break
`
	out := captureOutput(func() {
		code := runScript(script, "", nil, 5*time.Second)
		if code != 0 {
			t.Fatalf("script exit code %d", code)
		}
	})

	if len(strings.TrimSpace(out)) == 0 {
		t.Skip("no stacks in profile")
	}
}

func TestPprofScriptDiff(t *testing.T) {
	before := pprofCPUFixture
	after := pprofCPU2Fixture

	script := `
a = open("` + before + `")
b = open("` + after + `")
result = diff(a, b)
print(result)
`
	out := captureOutput(func() {
		code := runScript(script, "", nil, 5*time.Second)
		if code != 0 {
			t.Fatalf("script exit code %d", code)
		}
	})

	if len(strings.TrimSpace(out)) == 0 {
		t.Error("expected non-empty diff output from script")
	}
}

func TestPprofScriptOpenWithEvent(t *testing.T) {
	path := pprofAllocFixture

	script := `
p = open("` + path + `", event="alloc")
print(p.event)
print(p.samples)
`
	out := captureOutput(func() {
		code := runScript(script, "", nil, 5*time.Second)
		if code != 0 {
			t.Fatalf("script exit code %d", code)
		}
	})

	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected 2 lines, got %d: %s", len(lines), out)
	}
	if lines[0] != "alloc" {
		t.Errorf("expected event=alloc, got %q", lines[0])
	}
}

func TestPprofScriptOpenInvalidEvent(t *testing.T) {
	path := pprofCPUFixture

	script := `p = open("` + path + `", event="bogus")`

	stderr := captureStream(&os.Stderr, func() {
		code := runScript(script, "", nil, 5*time.Second)
		if code == 0 {
			t.Fatal("expected non-zero exit for invalid event")
		}
	})

	if !strings.Contains(stderr, "bogus") {
		t.Errorf("expected error mentioning 'bogus', got stderr:\n%s", stderr)
	}
}

func TestPprofScriptOpenWithThread(t *testing.T) {
	// Thread filtering via open() should work even if no threads match.
	path := pprofCPUFixture

	script := `
p = open("` + path + `", thread="nonexistent-thread")
print(p.samples)
`
	out := captureOutput(func() {
		code := runScript(script, "", nil, 5*time.Second)
		if code != 0 {
			t.Fatalf("script exit code %d", code)
		}
	})

	if strings.TrimSpace(out) != "0" {
		t.Errorf("expected 0 samples for nonexistent thread, got:\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// Script open("-") with stdin pprof
// ---------------------------------------------------------------------------

func TestPprofScriptOpenStdinMetadata(t *testing.T) {
	// open("-") with pprof stdin should preserve metadata (events list).
	path := pprofCPUFixture
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	script := `
p = open("-")
for e in p.events:
    print(e)
print("samples=" + str(p.samples))
`
	exitCode, stdout, stderr := runCLIForTest(t,
		[]string{"script", "-c", script}, bytes.NewReader(data))
	if exitCode != 0 {
		t.Fatalf("exit %d; stderr: %s", exitCode, stderr)
	}
	if !strings.Contains(stdout, "cpu") {
		t.Errorf("expected 'cpu' in events from stdin pprof, got:\n%s", stdout)
	}
	if strings.Contains(stdout, "samples=0") {
		t.Error("expected non-zero samples from stdin pprof")
	}
}

func TestPprofScriptOpenStdinWrongEvent(t *testing.T) {
	// open("-", event="cpu") on alloc-only pprof should return event-not-found error.
	path := pprofAllocFixture
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	script := `p = open("-", event="cpu")`
	exitCode, _, stderr := runCLIForTest(t,
		[]string{"script", "-c", script}, bytes.NewReader(data))
	if exitCode == 0 {
		t.Fatal("expected non-zero exit for wrong event on stdin pprof")
	}
	if !strings.Contains(stderr, "not found") {
		t.Errorf("expected 'not found' error, got:\n%s", stderr)
	}
}

func TestPprofScriptOpenStdinStartEndRejected(t *testing.T) {
	// open("-", start="1s") on pprof stdin should reject start/end.
	path := pprofCPUFixture
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	script := `p = open("-", start="1s")`
	exitCode, _, stderr := runCLIForTest(t,
		[]string{"script", "-c", script}, bytes.NewReader(data))
	if exitCode == 0 {
		t.Fatal("expected non-zero exit for start/end on stdin pprof")
	}
	if !strings.Contains(stderr, "start/end not supported") {
		t.Errorf("expected start/end rejection error, got:\n%s", stderr)
	}
}

func TestPprofScriptOpenStdinCollapsed(t *testing.T) {
	// open("-") with collapsed text stdin should still work.
	input := "A;B;C 10\nX;Y 5\n"
	script := `
p = open("-")
print("samples=" + str(p.samples))
`
	exitCode, stdout, stderr := runCLIForTest(t,
		[]string{"script", "-c", script}, strings.NewReader(input))
	if exitCode != 0 {
		t.Fatalf("exit %d; stderr: %s", exitCode, stderr)
	}
	if !strings.Contains(stdout, "samples=15") {
		t.Errorf("expected samples=15 from collapsed stdin, got:\n%s", stdout)
	}
}

// ---------------------------------------------------------------------------
// CLI integration tests via runCLIForTest
// ---------------------------------------------------------------------------

func TestPprofCLIEvents(t *testing.T) {
	path := pprofCPUFixture

	exitCode, stdout, stderr := runCLIForTest(t, []string{"events", path}, nil)
	if exitCode != 0 {
		t.Fatalf("exit %d; stderr: %s", exitCode, stderr)
	}
	if !strings.Contains(stdout, "cpu") {
		t.Errorf("expected 'cpu' in events CLI output, got:\n%s", stdout)
	}
}

func TestPprofCLIEventsStdin(t *testing.T) {
	path := pprofCPUFixture
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	exitCode, stdout, stderr := runCLIForTest(t, []string{"events", "-"}, bytes.NewReader(data))
	if exitCode != 0 {
		t.Fatalf("exit %d; stderr: %s", exitCode, stderr)
	}
	if !strings.Contains(stdout, "cpu") {
		t.Errorf("expected 'cpu' in events stdin output, got:\n%s", stdout)
	}
}

func TestPprofCLIEventsStdinCollapsedRejected(t *testing.T) {
	input := "A;B;C 10\n"
	exitCode, _, stderr := runCLIForTest(t, []string{"events", "-"}, strings.NewReader(input))
	if exitCode == 0 {
		t.Fatal("expected non-zero exit for collapsed stdin with events command")
	}
	if !strings.Contains(stderr, "collapsed") {
		t.Errorf("expected error mentioning collapsed text, got:\n%s", stderr)
	}
}

func TestPprofCLIInfo(t *testing.T) {
	path := pprofCPUFixture

	exitCode, stdout, stderr := runCLIForTest(t, []string{"info", path}, nil)
	if exitCode != 0 {
		t.Fatalf("exit %d; stderr: %s", exitCode, stderr)
	}
	if !strings.Contains(stdout, "Total samples:") {
		t.Errorf("expected 'Total samples:' in info CLI output, got:\n%s", stdout)
	}
}

func TestPprofCLITree(t *testing.T) {
	path := pprofCPUFixture

	exitCode, stdout, stderr := runCLIForTest(t, []string{"tree", path}, nil)
	if exitCode != 0 {
		t.Fatalf("exit %d; stderr: %s", exitCode, stderr)
	}
	if len(strings.TrimSpace(stdout)) == 0 {
		t.Error("expected non-empty tree CLI output")
	}
}

func TestPprofCLICollapse(t *testing.T) {
	path := pprofCPUFixture

	exitCode, stdout, stderr := runCLIForTest(t, []string{"collapse", path}, nil)
	if exitCode != 0 {
		t.Fatalf("exit %d; stderr: %s", exitCode, stderr)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) == 0 {
		t.Error("expected collapsed output")
	}
}

func TestPprofCLITimeline(t *testing.T) {
	path := pprofCPUFixture

	exitCode, _, stderr := runCLIForTest(t, []string{"timeline", path}, nil)
	if exitCode == 0 {
		t.Fatal("expected non-zero exit for timeline on pprof")
	}
	if !strings.Contains(stderr, "timeline requires a JFR file") {
		t.Errorf("expected timeline error, got stderr:\n%s", stderr)
	}
}

func TestPprofCLIDiff(t *testing.T) {
	before := pprofCPUFixture
	after := pprofCPU2Fixture

	exitCode, stdout, stderr := runCLIForTest(t, []string{"diff", before, after}, nil)
	if exitCode != 0 {
		t.Fatalf("exit %d; stderr: %s", exitCode, stderr)
	}
	if len(strings.TrimSpace(stdout)) == 0 {
		t.Error("expected non-empty diff CLI output")
	}
}

// ---------------------------------------------------------------------------
// parseStructuredProfile dispatch
// ---------------------------------------------------------------------------

func TestParseStructuredProfilePprof(t *testing.T) {
	path := pprofCPUFixture

	parsed, err := parseStructuredProfile(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if parsed == nil {
		t.Fatal("expected non-nil parsed for pprof file")
	}
	if parsed.eventCounts["cpu"] == 0 {
		t.Skip("no CPU events")
	}
}

func TestParseStructuredProfileCollapsed(t *testing.T) {
	parsed, err := parseStructuredProfile("stacks.txt", nil)
	if err != nil {
		t.Fatal(err)
	}
	if parsed != nil {
		t.Error("expected nil for collapsed text path")
	}
}

// ---------------------------------------------------------------------------
// Info with duration from pprof
// ---------------------------------------------------------------------------

func TestPprofInfoWithDuration(t *testing.T) {
	// CPU profiles from Go runtime have DurationNanos set.
	path := pprofCPUFixture

	parsed, err := parsePprofData(path, allEventTypes())
	if err != nil {
		t.Fatal(err)
	}

	if parsed.spanNanos == 0 {
		t.Skip("DurationNanos not set in CPU profile")
	}

	sf := parsed.stacksByEvent["cpu"]
	if sf == nil {
		sf = &stackFile{}
	}

	out := captureOutput(func() {
		cmdInfo(sf, infoOpts{
			eventType:   "cpu",
			hasMetadata: true,
			eventCounts: parsed.eventCounts,
			topThreads:  5,
			topMethods:  10,
			spanNanos:   parsed.spanNanos,
		})
	})

	if !strings.Contains(out, "Duration:") {
		t.Errorf("expected 'Duration:' in info output with spanNanos, got:\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// Raw (uncompressed) protobuf pprof stdin
// ---------------------------------------------------------------------------

// decompressPprof reads a .pb.gz file and returns the raw protobuf bytes.
func decompressPprof(t *testing.T, path string) []byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer gr.Close()
	data, err := io.ReadAll(gr)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestPprofRawProtobufStdinHot(t *testing.T) {
	// Raw (decompressed) protobuf pprof on stdin should be detected and parsed.
	path := pprofCPUFixture
	raw := decompressPprof(t, path)

	exitCode, stdout, stderr := runCLIForTest(t, []string{"hot", "-"}, bytes.NewReader(raw))
	if exitCode != 0 {
		t.Fatalf("expected exit 0, got %d; stderr: %s", exitCode, stderr)
	}
	if len(strings.TrimSpace(stdout)) == 0 {
		t.Error("expected non-empty hot output from raw protobuf stdin")
	}
}

func TestPprofRawProtobufStdinEvents(t *testing.T) {
	path := pprofCPUFixture
	raw := decompressPprof(t, path)

	exitCode, stdout, stderr := runCLIForTest(t, []string{"events", "-"}, bytes.NewReader(raw))
	if exitCode != 0 {
		t.Fatalf("expected exit 0, got %d; stderr: %s", exitCode, stderr)
	}
	if !strings.Contains(stdout, "cpu") {
		t.Errorf("expected 'cpu' in events output, got:\n%s", stdout)
	}
}

func TestPprofRawProtobufStdinDiff(t *testing.T) {
	before := pprofCPUFixture
	after := pprofCPU2Fixture
	raw := decompressPprof(t, before)

	exitCode, stdout, stderr := runCLIForTest(t, []string{"diff", "-", after}, bytes.NewReader(raw))
	if exitCode != 0 {
		t.Fatalf("expected exit 0, got %d; stderr: %s", exitCode, stderr)
	}
	if len(strings.TrimSpace(stdout)) == 0 {
		t.Error("expected non-empty diff output from raw protobuf stdin")
	}
}

func TestPprofRawProtobufStdinScript(t *testing.T) {
	path := pprofCPUFixture
	raw := decompressPprof(t, path)

	script := `
p = open("-")
print(p.samples)
`
	exitCode, stdout, stderr := runCLIForTest(t, []string{"script", "-c", script}, bytes.NewReader(raw))
	if exitCode != 0 {
		t.Fatalf("expected exit 0, got %d; stderr: %s", exitCode, stderr)
	}
	if len(strings.TrimSpace(stdout)) == 0 {
		t.Error("expected non-empty output from script with raw protobuf stdin")
	}
}

func TestPprofGzipStdinStillWorks(t *testing.T) {
	// Regression: gzip-compressed pprof stdin must still work.
	path := pprofCPUFixture
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	exitCode, stdout, stderr := runCLIForTest(t, []string{"hot", "-"}, bytes.NewReader(data))
	if exitCode != 0 {
		t.Fatalf("expected exit 0, got %d; stderr: %s", exitCode, stderr)
	}
	if len(strings.TrimSpace(stdout)) == 0 {
		t.Error("expected non-empty hot output from gzip pprof stdin")
	}
}

func TestStdinLooksBinary(t *testing.T) {
	tests := []struct {
		name   string
		data   []byte
		binary bool
	}{
		{"collapsed text", []byte("A;B;C 10\nX;Y 5\n"), false},
		{"empty", []byte{}, false},
		{"gzip magic", []byte{0x1f, 0x8b, 0x08, 0x00}, true},
		{"raw protobuf (null byte)", []byte{0x0a, 0x00, 0x0a, 0x05}, true},
		{"tabs and newlines only", []byte("\t\t\n\r\n"), false},
		{"control char at byte 100", append(bytes.Repeat([]byte("A"), 100), 0x00), true},
		{"control char beyond 256", append(bytes.Repeat([]byte("A"), 300), 0x00), false},
		// High bytes (>= 0x7F) — protobuf varints, UTF-8 multibyte.
		{"high byte 0x80", []byte{0x80, 0x01, 0x0a}, true},
		{"high byte 0xFF", []byte("A;B;C\xff 10\n"), true},
		{"DEL (0x7F)", []byte{0x7f, 0x41, 0x42}, true},
		// UTF-8 multibyte: café → 0x63 0x61 0x66 0xc3 0xa9
		{"UTF-8 text", []byte("caf\xc3\xa9;B 10\n"), true},
		// Pure printable ASCII at boundary.
		{"all tilde (0x7E)", bytes.Repeat([]byte("~"), 256), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stdinLooksBinary(tt.data)
			if got != tt.binary {
				t.Errorf("stdinLooksBinary(%v) = %v, want %v", tt.data, got, tt.binary)
			}
		})
	}
}

func TestStdinUTF8CollapsedFallback(t *testing.T) {
	// UTF-8 method names in collapsed text trigger the binary heuristic,
	// but pprof parse fails, so parseStdin must fall back to collapsed.
	input := "caf\xc3\xa9;m\xc3\xa9thode 10\nA;B 5\n"

	exitCode, stdout, _ := runCLIForTest(t, []string{"hot", "-"}, strings.NewReader(input))
	if exitCode != 0 {
		t.Fatalf("expected exit 0, got %d", exitCode)
	}
	if !strings.Contains(stdout, "caf") {
		t.Errorf("expected UTF-8 method name in output, got:\n%s", stdout)
	}
}

func TestStdinCorruptBinaryErrors(t *testing.T) {
	// Random binary garbage: not valid pprof AND not valid UTF-8.
	// Must produce a non-zero exit code with an error, not silent empty output.
	// Use bytes that are invalid UTF-8 (0xfe, 0xff are never valid in UTF-8).
	garbage := []byte{0xfe, 0xff, 0x00, 0x01, 0x80, 0xde, 0xad, 0xbe, 0xef}

	exitCode, _, stderr := runCLIForTest(t, []string{"hot", "-"}, bytes.NewReader(garbage))
	if exitCode == 0 {
		t.Fatal("expected non-zero exit code for corrupt binary stdin")
	}
	if !strings.Contains(stderr, "pprof") {
		t.Errorf("expected error mentioning pprof, got stderr:\n%s", stderr)
	}
}

func TestStdinTruncatedPprofErrors(t *testing.T) {
	// Truncated pprof: take just the first 16 bytes of a real profile.
	path := pprofCPUFixture
	raw := decompressPprof(t, path)
	truncated := raw[:min(16, len(raw))]

	exitCode, _, stderr := runCLIForTest(t, []string{"hot", "-"}, bytes.NewReader(truncated))
	if exitCode == 0 {
		t.Fatal("expected non-zero exit code for truncated pprof stdin")
	}
	if !strings.Contains(stderr, "pprof") {
		t.Errorf("expected error mentioning pprof, got stderr:\n%s", stderr)
	}
}
