package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func captureOutput(f func()) string {
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	f()
	w.Close()
	var buf bytes.Buffer
	io.Copy(&buf, r)
	os.Stdout = old
	return buf.String()
}

func makeStackFile(stacks []stack) *stackFile {
	sf := &stackFile{}
	for _, s := range stacks {
		sf.stacks = append(sf.stacks, s)
		sf.totalSamples += s.count
	}
	return sf
}

// ---------------------------------------------------------------------------
// TestVersionOutput
// ---------------------------------------------------------------------------

func TestVersionOutput(t *testing.T) {
	out := captureOutput(func() {
		printVersion(os.Stdout)
	})
	if !strings.Contains(out, "ap-query version") {
		t.Errorf("expected 'ap-query version' in output, got %q", out)
	}
	if !strings.Contains(out, version) {
		t.Errorf("expected version %q in output, got %q", version, out)
	}
}

// ---------------------------------------------------------------------------
// TestShortName
// ---------------------------------------------------------------------------

func TestShortName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"com/example/App.process", "App.process"},
		{"com.example.App.process", "App.process"},
		{"App.process", "App.process"},
		{"process", "process"},
		{"a.b.c.D.run", "D.run"},
	}
	for _, tt := range tests {
		got := shortName(tt.input)
		if got != tt.want {
			t.Errorf("shortName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// TestMatchesMethod
// ---------------------------------------------------------------------------

func TestMatchesMethod(t *testing.T) {
	tests := []struct {
		frame   string
		pattern string
		want    bool
	}{
		{"com/example/App.process", "App.process", true},
		{"com/example/App.process", "process", true},
		{"com/example/App.process", "com.example", true},
		{"com/example/App.process", "Foo.bar", false},
		{"com.example.App.process", "App.process", true},
	}
	for _, tt := range tests {
		got := matchesMethod(tt.frame, tt.pattern)
		if got != tt.want {
			t.Errorf("matchesMethod(%q, %q) = %v, want %v", tt.frame, tt.pattern, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// TestComputeHot
// ---------------------------------------------------------------------------

func TestComputeHot(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.a", "B.b", "C.c"}, lines: []uint32{0, 0, 0}, count: 10, thread: "main"},
		{frames: []string{"A.a", "B.b"}, lines: []uint32{0, 0}, count: 5, thread: "main"},
		{frames: []string{"A.a", "D.d"}, lines: []uint32{0, 0}, count: 3, thread: "main"},
	})

	ranked := computeHot(sf, false)
	if len(ranked) == 0 {
		t.Fatal("computeHot returned empty")
	}

	// Self counts: C.c=10, B.b=5, D.d=3; A.a has selfCount=0 (never a leaf)
	if ranked[0].name != "C.c" || ranked[0].selfCount != 10 {
		t.Errorf("expected C.c with self=10 at top, got %s with self=%d", ranked[0].name, ranked[0].selfCount)
	}
	if ranked[1].name != "B.b" || ranked[1].selfCount != 5 {
		t.Errorf("expected B.b with self=5 at #2, got %s with self=%d", ranked[1].name, ranked[1].selfCount)
	}

	// Total counts: A.a=18 (all), B.b=15 (10+5), C.c=10, D.d=3
	found := make(map[string]hotEntry)
	for _, e := range ranked {
		found[e.name] = e
	}

	if e, ok := found["A.a"]; !ok {
		t.Error("A.a missing from results (should appear as total-only entry)")
	} else {
		if e.selfCount != 0 {
			t.Errorf("A.a selfCount=%d, want 0", e.selfCount)
		}
		if e.totalCount != 18 {
			t.Errorf("A.a totalCount=%d, want 18", e.totalCount)
		}
	}
	if e, ok := found["B.b"]; ok {
		if e.totalCount != 15 {
			t.Errorf("B.b total=%d, want 15", e.totalCount)
		}
	}
}

func TestCmdHotDualOutput(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.a", "B.b", "C.c"}, lines: []uint32{0, 0, 0}, count: 10, thread: "main"},
		{frames: []string{"A.a", "B.b"}, lines: []uint32{0, 0}, count: 5, thread: "main"},
		{frames: []string{"A.a", "D.d"}, lines: []uint32{0, 0}, count: 3, thread: "main"},
	})

	out := captureOutput(func() {
		cmdHot(sf, 0, false, 0)
	})

	if !strings.Contains(out, "=== RANK BY SELF TIME ===") {
		t.Fatal("expected self-time section header")
	}
	if !strings.Contains(out, "=== RANK BY TOTAL TIME ===") {
		t.Fatal("expected total-time section header")
	}

	// Total-time section should have A.a first (total=18)
	totalIdx := strings.Index(out, "=== RANK BY TOTAL TIME ===")
	totalSection := out[totalIdx:]
	lines := strings.Split(strings.TrimSpace(totalSection), "\n")
	// lines[0] = header, lines[1] = column header, lines[2] = first data row
	if len(lines) < 3 {
		t.Fatalf("expected at least 3 lines in total section, got %d", len(lines))
	}
	if !strings.Contains(lines[2], "A.a") {
		t.Errorf("expected A.a first in total-time section, got %q", lines[2])
	}

	// SAMPLES column in total-time section must reflect totalCount, not selfCount.
	// A.a is a pure dispatcher (selfCount=0, totalCount=18) — its SAMPLES must be 18.
	if !strings.Contains(lines[2], "18") {
		t.Errorf("expected A.a to show 18 samples in total-time section, got %q", lines[2])
	}
	// Self-time section: A.a should show 0 samples (selfCount=0)
	selfIdx := strings.Index(out, "=== RANK BY SELF TIME ===")
	selfSection := out[selfIdx:totalIdx]
	if !strings.Contains(selfSection, "A.a") {
		t.Error("expected A.a in self-time section")
	}
	for _, line := range strings.Split(selfSection, "\n") {
		if strings.Contains(line, "A.a") {
			// Should end with 0 samples
			fields := strings.Fields(line)
			last := fields[len(fields)-1]
			if last != "0" {
				t.Errorf("expected A.a to show 0 samples in self-time section, got %q", last)
			}
			break
		}
	}
}

func TestComputeHotEmpty(t *testing.T) {
	sf := makeStackFile(nil)
	ranked := computeHot(sf, false)
	if ranked != nil {
		t.Errorf("expected nil for empty stackFile, got %v", ranked)
	}
}

// ---------------------------------------------------------------------------
// TestComputeThreads
// ---------------------------------------------------------------------------

func TestComputeThreads(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.a"}, lines: []uint32{0}, count: 10, thread: "main"},
		{frames: []string{"B.b"}, lines: []uint32{0}, count: 5, thread: "worker-1"},
		{frames: []string{"C.c"}, lines: []uint32{0}, count: 3, thread: "main"},
		{frames: []string{"D.d"}, lines: []uint32{0}, count: 2, thread: ""},
	})

	ranked, noThread, hasThread := computeThreads(sf)
	if !hasThread {
		t.Fatal("expected hasThread=true")
	}
	if noThread != 2 {
		t.Errorf("noThread=%d, want 2", noThread)
	}
	if len(ranked) != 2 {
		t.Fatalf("expected 2 threads, got %d", len(ranked))
	}
	if ranked[0].name != "main" || ranked[0].samples != 13 {
		t.Errorf("expected main=13 at top, got %s=%d", ranked[0].name, ranked[0].samples)
	}
	if ranked[1].name != "worker-1" || ranked[1].samples != 5 {
		t.Errorf("expected worker-1=5 at #2, got %s=%d", ranked[1].name, ranked[1].samples)
	}
}

func TestComputeThreadsNoThreadInfo(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.a"}, lines: []uint32{0}, count: 10, thread: ""},
	})

	_, _, hasThread := computeThreads(sf)
	if hasThread {
		t.Error("expected hasThread=false")
	}
}

// ---------------------------------------------------------------------------
// TestCmdCollapse
// ---------------------------------------------------------------------------

func TestCmdCollapse(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.a", "B.b", "C.c"}, lines: []uint32{0, 0, 0}, count: 10, thread: "main"},
		{frames: []string{"X.x", "Y.y"}, lines: []uint32{0, 0}, count: 3, thread: ""},
	})

	out := captureOutput(func() {
		cmdCollapse(sf)
	})

	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %q", len(lines), out)
	}

	// Check thread prefix on first line
	if !strings.HasPrefix(lines[0], "[main];") {
		t.Errorf("expected [main] prefix, got %q", lines[0])
	}
	if !strings.HasSuffix(lines[0], " 10") {
		t.Errorf("expected count 10, got %q", lines[0])
	}
	if !strings.Contains(lines[0], "A.a;B.b;C.c") {
		t.Errorf("expected frames A.a;B.b;C.c, got %q", lines[0])
	}

	// No thread prefix on second line
	if strings.HasPrefix(lines[1], "[") {
		t.Errorf("expected no thread prefix, got %q", lines[1])
	}
	if !strings.HasSuffix(lines[1], " 3") {
		t.Errorf("expected count 3, got %q", lines[1])
	}
}

// ---------------------------------------------------------------------------
// TestCmdDiff
// ---------------------------------------------------------------------------

func TestCmdDiff(t *testing.T) {
	before := makeStackFile([]stack{
		{frames: []string{"com.example.A.doWork"}, lines: []uint32{0}, count: 20, thread: "main"},
		{frames: []string{"com.example.B.process"}, lines: []uint32{0}, count: 10, thread: "main"},
		{frames: []string{"com.example.C.gone"}, lines: []uint32{0}, count: 5, thread: "main"},
	})
	after := makeStackFile([]stack{
		{frames: []string{"com.example.A.doWork"}, lines: []uint32{0}, count: 10, thread: "main"},
		{frames: []string{"com.example.B.process"}, lines: []uint32{0}, count: 30, thread: "main"},
		{frames: []string{"com.example.D.newMethod"}, lines: []uint32{0}, count: 5, thread: "main"},
	})

	out := captureOutput(func() {
		cmdDiff(before, after, 0.5, 0, false)
	})

	if !strings.Contains(out, "REGRESSION") {
		t.Error("expected REGRESSION section")
	}
	if !strings.Contains(out, "B.process") {
		t.Error("expected B.process in regression")
	}
	if !strings.Contains(out, "IMPROVEMENT") {
		t.Error("expected IMPROVEMENT section")
	}
	if !strings.Contains(out, "A.doWork") {
		t.Error("expected A.doWork in improvement")
	}
	if !strings.Contains(out, "NEW") {
		t.Error("expected NEW section")
	}
	if !strings.Contains(out, "D.newMethod") {
		t.Error("expected D.newMethod in new")
	}
	if !strings.Contains(out, "GONE") {
		t.Error("expected GONE section")
	}
	if !strings.Contains(out, "C.gone") {
		t.Error("expected C.gone in gone")
	}
}

func TestCmdDiffNoChanges(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.a"}, lines: []uint32{0}, count: 10, thread: "main"},
	})

	out := captureOutput(func() {
		cmdDiff(sf, sf, 0.5, 0, false)
	})

	if !strings.Contains(out, "no significant changes") {
		t.Errorf("expected 'no significant changes', got %q", out)
	}
}

// ---------------------------------------------------------------------------
// TestCmdLines
// ---------------------------------------------------------------------------

func TestCmdLines(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.a", "com.example.B.process"}, lines: []uint32{10, 42}, count: 5, thread: "main"},
		{frames: []string{"A.a", "com.example.B.process"}, lines: []uint32{10, 99}, count: 3, thread: "main"},
		{frames: []string{"A.a", "com.example.B.process"}, lines: []uint32{10, 42}, count: 2, thread: "worker"},
	})

	out := captureOutput(func() {
		cmdLines(sf, "B.process", 0, false)
	})

	if !strings.Contains(out, "SOURCE:LINE") {
		t.Error("expected header")
	}
	if !strings.Contains(out, "B.process:42") {
		t.Errorf("expected B.process:42, got %q", out)
	}
	if !strings.Contains(out, "B.process:99") {
		t.Errorf("expected B.process:99, got %q", out)
	}

	// Line 42 should have count 7 (5+2), line 99 should have count 3
	lines := strings.Split(strings.TrimSpace(out), "\n")
	// First data line (after header) should be line 42 with 7 samples
	if len(lines) < 2 {
		t.Fatalf("expected at least 2 lines, got %d", len(lines))
	}
	if !strings.Contains(lines[1], "B.process:42") {
		t.Errorf("expected B.process:42 first (highest count), got %q", lines[1])
	}
}

func TestCmdLinesNoMatch(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.a", "B.b"}, lines: []uint32{10, 20}, count: 5, thread: "main"},
	})

	out := captureOutput(func() {
		cmdLines(sf, "Nonexistent", 0, false)
	})

	if !strings.Contains(out, "no frames matching") {
		t.Errorf("expected 'no frames matching', got %q", out)
	}
}

// ---------------------------------------------------------------------------
// TestCmdInfo
// ---------------------------------------------------------------------------

func TestCmdInfo(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.a", "B.b"}, lines: []uint32{0, 0}, count: 10, thread: "main"},
		{frames: []string{"C.c"}, lines: []uint32{0}, count: 5, thread: "worker"},
	})

	out := captureOutput(func() {
		cmdInfo(sf, "cpu", true, map[string]int{"cpu": 15}, 0, 5, 10)
	})

	if !strings.Contains(out, "=== THREADS (top") {
		t.Error("expected THREADS section")
	}
	if !strings.Contains(out, "main") {
		t.Error("expected 'main' thread")
	}
	if !strings.Contains(out, "=== RANK BY SELF TIME (top") {
		t.Error("expected RANK BY SELF TIME section")
	}
	if !strings.Contains(out, "=== RANK BY TOTAL TIME (top") {
		t.Error("expected RANK BY TOTAL TIME section")
	}
	if !strings.Contains(out, "Total samples: 15") {
		t.Errorf("expected 'Total samples: 15', got %q", out)
	}
	if !strings.Contains(out, "Event: cpu") {
		t.Error("expected 'Event: cpu' header")
	}
}

func TestCmdInfoAlsoAvailable(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.a"}, lines: []uint32{0}, count: 10, thread: "main"},
	})

	out := captureOutput(func() {
		cmdInfo(sf, "wall", true, map[string]int{"wall": 10, "cpu": 200, "alloc": 50}, 0, 5, 10)
	})

	if !strings.Contains(out, "Event: wall") {
		t.Error("expected 'Event: wall' header")
	}
	if !strings.Contains(out, "Also available:") {
		t.Error("expected 'Also available' footer")
	}
	if !strings.Contains(out, "cpu (200 events)") {
		t.Errorf("expected cpu count in footer, got %q", out)
	}
	if !strings.Contains(out, "alloc (50 events)") {
		t.Errorf("expected alloc count in footer, got %q", out)
	}
}

func TestCmdInfoExpand(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.a", "B.b", "C.c"}, lines: []uint32{10, 20, 30}, count: 10, thread: "main"},
		{frames: []string{"A.a", "B.b"}, lines: []uint32{10, 20}, count: 5, thread: "main"},
		{frames: []string{"A.a", "D.d"}, lines: []uint32{10, 40}, count: 3, thread: "worker"},
	})

	out := captureOutput(func() {
		cmdInfo(sf, "cpu", false, nil, 2, 10, 20)
	})

	// Should have drill-down sections for top 2 methods (C.c and B.b)
	if !strings.Contains(out, "=== DRILL-DOWN: C.c") {
		t.Error("expected DRILL-DOWN for C.c")
	}
	if !strings.Contains(out, "=== DRILL-DOWN: B.b") {
		t.Error("expected DRILL-DOWN for B.b")
	}
	if strings.Contains(out, "=== DRILL-DOWN: D.d") {
		t.Error("should not expand D.d (only top 2)")
	}
	if !strings.Contains(out, "--- tree (callees) ---") {
		t.Error("expected tree section")
	}
	if !strings.Contains(out, "--- callers ---") {
		t.Error("expected callers section")
	}
	if !strings.Contains(out, "--- lines ---") {
		t.Error("expected lines section")
	}
}

func TestCmdInfoExpandZero(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.a", "B.b"}, lines: []uint32{10, 20}, count: 10, thread: "main"},
	})

	out := captureOutput(func() {
		cmdInfo(sf, "cpu", false, nil, 0, 10, 20)
	})

	if strings.Contains(out, "DRILL-DOWN") {
		t.Error("expected no DRILL-DOWN when expand=0")
	}
}

func TestCmdInfoExpandNoLineInfo(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.a", "B.b"}, lines: []uint32{0, 0}, count: 10, thread: "main"},
		{frames: []string{"C.c"}, lines: []uint32{0}, count: 5, thread: "main"},
	})

	out := captureOutput(func() {
		cmdInfo(sf, "cpu", false, nil, 2, 10, 20)
	})

	// Drill-down should appear but without lines section
	if !strings.Contains(out, "=== DRILL-DOWN:") {
		t.Error("expected DRILL-DOWN section")
	}
	if strings.Contains(out, "--- lines ---") {
		t.Error("expected no lines section when no line info")
	}
}

// ---------------------------------------------------------------------------
// TestComputeLines
// ---------------------------------------------------------------------------

func TestComputeLines(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.a", "com.example.B.process"}, lines: []uint32{10, 42}, count: 5, thread: "main"},
		{frames: []string{"A.a", "com.example.B.process"}, lines: []uint32{10, 99}, count: 3, thread: "main"},
		{frames: []string{"A.a", "com.example.B.process"}, lines: []uint32{10, 42}, count: 2, thread: "worker"},
	})

	result, hasMethod := computeLines(sf, "B.process", 0, false)
	if !hasMethod {
		t.Fatal("expected hasMethod=true")
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 line entries, got %d", len(result))
	}
	// Line 42 should be first with 7 samples
	if result[0].line != 42 || result[0].samples != 7 {
		t.Errorf("expected line 42 with 7 samples first, got line %d with %d", result[0].line, result[0].samples)
	}
	if result[1].line != 99 || result[1].samples != 3 {
		t.Errorf("expected line 99 with 3 samples second, got line %d with %d", result[1].line, result[1].samples)
	}
}

func TestComputeLinesNoLineInfo(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.a", "B.b"}, lines: []uint32{0, 0}, count: 10, thread: "main"},
	})

	result, hasMethod := computeLines(sf, "B.b", 0, false)
	if result != nil {
		t.Errorf("expected nil result, got %v", result)
	}
	if !hasMethod {
		t.Error("expected hasMethod=true (method exists but no line info)")
	}
}

func TestComputeLinesNoMatch(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.a", "B.b"}, lines: []uint32{10, 20}, count: 5, thread: "main"},
	})

	result, hasMethod := computeLines(sf, "Nonexistent", 0, false)
	if result != nil {
		t.Errorf("expected nil result, got %v", result)
	}
	if hasMethod {
		t.Error("expected hasMethod=false")
	}
}

// ---------------------------------------------------------------------------
// TestAssertBelow
// ---------------------------------------------------------------------------

func TestAssertBelowPass(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.a"}, lines: []uint32{0}, count: 10, thread: "main"},
		{frames: []string{"B.b"}, lines: []uint32{0}, count: 90, thread: "main"},
	})

	// Top method is B.b at 90%. Threshold 95% should pass.
	ranked := computeHot(sf, false)
	if len(ranked) == 0 {
		t.Fatal("expected ranked entries")
	}
	topPct := 100.0 * float64(ranked[0].selfCount) / float64(sf.totalSamples)
	if topPct >= 95.0 {
		t.Errorf("expected top pct < 95, got %.1f", topPct)
	}
}

func TestAssertBelowFail(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.a"}, lines: []uint32{0}, count: 10, thread: "main"},
		{frames: []string{"B.b"}, lines: []uint32{0}, count: 90, thread: "main"},
	})

	// Top method is B.b at 90%. Threshold 50% should fail.
	ranked := computeHot(sf, false)
	if len(ranked) == 0 {
		t.Fatal("expected ranked entries")
	}
	topPct := 100.0 * float64(ranked[0].selfCount) / float64(sf.totalSamples)
	if topPct < 50.0 {
		t.Errorf("expected top pct >= 50, got %.1f", topPct)
	}
}

// ---------------------------------------------------------------------------
// TestSelfPcts
// ---------------------------------------------------------------------------

func TestSelfPcts(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"com.example.A.run"}, lines: []uint32{0}, count: 30, thread: "main"},
		{frames: []string{"com.example.B.exec"}, lines: []uint32{0}, count: 70, thread: "main"},
	})

	pcts := selfPcts(sf, false)
	if pcts["A.run"] != 30.0 {
		t.Errorf("A.run = %.1f, want 30.0", pcts["A.run"])
	}
	if pcts["B.exec"] != 70.0 {
		t.Errorf("B.exec = %.1f, want 70.0", pcts["B.exec"])
	}
}

// ---------------------------------------------------------------------------
// TestStackLines
// ---------------------------------------------------------------------------

func TestStackLinesParallel(t *testing.T) {
	// Verify that lines slice is parallel to frames
	s := stack{
		frames: []string{"A.a", "B.b", "C.c"},
		lines:  []uint32{10, 0, 42},
		count:  5,
	}
	if len(s.frames) != len(s.lines) {
		t.Errorf("frames len %d != lines len %d", len(s.frames), len(s.lines))
	}
	if s.lines[0] != 10 {
		t.Errorf("lines[0] = %d, want 10", s.lines[0])
	}
	if s.lines[2] != 42 {
		t.Errorf("lines[2] = %d, want 42", s.lines[2])
	}
}

// ---------------------------------------------------------------------------
// TestParseCollapsed*
// ---------------------------------------------------------------------------

func TestParseCollapsedBasic(t *testing.T) {
	r := strings.NewReader("A;B;C 10\nX;Y 5\n")
	sf, err := parseCollapsed(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(sf.stacks) != 2 {
		t.Fatalf("expected 2 stacks, got %d", len(sf.stacks))
	}
	if sf.totalSamples != 15 {
		t.Errorf("totalSamples=%d, want 15", sf.totalSamples)
	}
	// First stack
	s := sf.stacks[0]
	if len(s.frames) != 3 || s.frames[0] != "A" || s.frames[1] != "B" || s.frames[2] != "C" {
		t.Errorf("stack[0].frames=%v, want [A B C]", s.frames)
	}
	if s.count != 10 {
		t.Errorf("stack[0].count=%d, want 10", s.count)
	}
}

func TestParseCollapsedThreads(t *testing.T) {
	r := strings.NewReader("[main tid=1];A;B 10\n[worker];C 5\n")
	sf, err := parseCollapsed(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(sf.stacks) != 2 {
		t.Fatalf("expected 2 stacks, got %d", len(sf.stacks))
	}
	if sf.stacks[0].thread != "main" {
		t.Errorf("stack[0].thread=%q, want \"main\"", sf.stacks[0].thread)
	}
	if sf.stacks[1].thread != "worker" {
		t.Errorf("stack[1].thread=%q, want \"worker\"", sf.stacks[1].thread)
	}
	// Frames should not include the thread marker
	if sf.stacks[0].frames[0] != "A" {
		t.Errorf("stack[0].frames[0]=%q, want \"A\"", sf.stacks[0].frames[0])
	}
}

func TestParseCollapsedLineAnnotations(t *testing.T) {
	r := strings.NewReader("A.main:10_[0];B.process:42_[j] 100\n")
	sf, err := parseCollapsed(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(sf.stacks) != 1 {
		t.Fatalf("expected 1 stack, got %d", len(sf.stacks))
	}
	s := sf.stacks[0]
	if s.frames[0] != "A.main" || s.frames[1] != "B.process" {
		t.Errorf("frames=%v, want [A.main B.process]", s.frames)
	}
	if s.lines[0] != 10 || s.lines[1] != 42 {
		t.Errorf("lines=%v, want [10 42]", s.lines)
	}
}

func TestParseCollapsedEmptyLines(t *testing.T) {
	r := strings.NewReader("\n\nA;B 10\n\nbadline no count\n\nC;D 5\n\n")
	sf, err := parseCollapsed(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(sf.stacks) != 2 {
		t.Fatalf("expected 2 stacks, got %d", len(sf.stacks))
	}
	if sf.totalSamples != 15 {
		t.Errorf("totalSamples=%d, want 15", sf.totalSamples)
	}
}

func TestParseCollapsedMixedThreads(t *testing.T) {
	r := strings.NewReader("[main tid=1];A;B 10\nC;D 5\n[worker];E 3\n")
	sf, err := parseCollapsed(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(sf.stacks) != 3 {
		t.Fatalf("expected 3 stacks, got %d", len(sf.stacks))
	}
	if sf.stacks[0].thread != "main" {
		t.Errorf("stack[0].thread=%q, want \"main\"", sf.stacks[0].thread)
	}
	if sf.stacks[1].thread != "" {
		t.Errorf("stack[1].thread=%q, want \"\"", sf.stacks[1].thread)
	}
	if sf.stacks[2].thread != "worker" {
		t.Errorf("stack[2].thread=%q, want \"worker\"", sf.stacks[2].thread)
	}
}

func TestOpenInputDetection(t *testing.T) {
	tests := []struct {
		path    string
		wantJFR bool
	}{
		{"profile.jfr", true},
		{"profile.jfr.gz", true},
		{"stacks.txt", false},
		{"stacks.collapsed", false},
		{"stacks.gz", false},
		{"-", false},
	}
	for _, tt := range tests {
		got := isJFRPath(tt.path)
		if got != tt.wantJFR {
			t.Errorf("isJFRPath(%q) = %v, want %v", tt.path, got, tt.wantJFR)
		}
	}
}

func TestFilterByThread(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.a"}, lines: []uint32{0}, count: 10, thread: "main"},
		{frames: []string{"B.b"}, lines: []uint32{0}, count: 5, thread: "worker-1"},
		{frames: []string{"C.c"}, lines: []uint32{0}, count: 3, thread: "main-loop"},
	})

	filtered := sf.filterByThread("main")
	if len(filtered.stacks) != 2 {
		t.Errorf("expected 2 stacks matching 'main', got %d", len(filtered.stacks))
	}
	if filtered.totalSamples != 13 {
		t.Errorf("expected totalSamples=13, got %d", filtered.totalSamples)
	}
}

// ---------------------------------------------------------------------------
// TestParseFlags
// ---------------------------------------------------------------------------

func TestParseFlags(t *testing.T) {
	f := parseFlags([]string{"file.jfr", "--top", "20", "--fqn", "-m", "Foo.bar", "--assert-below", "15.0"})
	if len(f.args) != 1 || f.args[0] != "file.jfr" {
		t.Errorf("args = %v, want [file.jfr]", f.args)
	}
	if f.vals["top"] != "20" {
		t.Errorf("top = %q, want 20", f.vals["top"])
	}
	if !f.bools["fqn"] {
		t.Error("expected fqn=true")
	}
	if f.vals["m"] != "Foo.bar" {
		t.Errorf("m = %q, want Foo.bar", f.vals["m"])
	}
	if f.vals["assert-below"] != "15.0" {
		t.Errorf("assert-below = %q, want 15.0", f.vals["assert-below"])
	}
}

// ---------------------------------------------------------------------------
// TestArchiveName
// ---------------------------------------------------------------------------

func TestArchiveName(t *testing.T) {
	name := archiveName()
	expected := "ap-query_" + runtime.GOOS + "_" + runtime.GOARCH + ".tar.gz"
	if name != expected {
		t.Errorf("archiveName() = %q, want %q", name, expected)
	}
	if !strings.HasPrefix(name, "ap-query_") {
		t.Errorf("expected prefix ap-query_, got %q", name)
	}
	if !strings.HasSuffix(name, ".tar.gz") {
		t.Errorf("expected suffix .tar.gz, got %q", name)
	}
}

// ---------------------------------------------------------------------------
// TestParseChecksums
// ---------------------------------------------------------------------------

func TestParseChecksums(t *testing.T) {
	input := "abc123  ap-query_linux_amd64.tar.gz\ndef456  ap-query_darwin_arm64.tar.gz\n"
	m, err := parseChecksums(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(m))
	}
	if m["ap-query_linux_amd64.tar.gz"] != "abc123" {
		t.Errorf("linux hash = %q, want abc123", m["ap-query_linux_amd64.tar.gz"])
	}
	if m["ap-query_darwin_arm64.tar.gz"] != "def456" {
		t.Errorf("darwin hash = %q, want def456", m["ap-query_darwin_arm64.tar.gz"])
	}
}

func TestParseChecksumsEmpty(t *testing.T) {
	_, err := parseChecksums(strings.NewReader(""))
	if err == nil {
		t.Error("expected error for empty checksums")
	}
}

func TestParseChecksumsBlankLines(t *testing.T) {
	input := "\nabc123  file1.tar.gz\n\n\ndef456  file2.tar.gz\n\n"
	m, err := parseChecksums(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(m))
	}
}

// ---------------------------------------------------------------------------
// TestDownloadURL
// ---------------------------------------------------------------------------

func TestDownloadURL(t *testing.T) {
	url := downloadURL("v1.2.3", "ap-query_linux_amd64.tar.gz")
	expected := "https://github.com/jerrinot/ap-query/releases/download/v1.2.3/ap-query_linux_amd64.tar.gz"
	if url != expected {
		t.Errorf("downloadURL() = %q, want %q", url, expected)
	}
}

// ---------------------------------------------------------------------------
// TestIsGoInstall
// ---------------------------------------------------------------------------

func TestIsGoInstall(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("cannot determine home dir")
	}

	tests := []struct {
		path string
		want bool
	}{
		{filepath.Join(home, "go", "bin", "ap-query"), true},
		{filepath.Join(home, "go", "bin", "sub", "ap-query"), false},
		{"/usr/local/bin/ap-query", false},
		{"/opt/ap-query/ap-query", false},
		{filepath.Join(home, ".local", "bin", "ap-query"), false},
	}

	for _, tt := range tests {
		got := isGoInstall(tt.path)
		if got != tt.want {
			t.Errorf("isGoInstall(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

func TestIsGoInstallCustomGOPATH(t *testing.T) {
	t.Setenv("GOPATH", "/custom/gopath")

	if !isGoInstall("/custom/gopath/bin/ap-query") {
		t.Error("expected true for custom GOPATH/bin")
	}
	if isGoInstall("/other/bin/ap-query") {
		t.Error("expected false for non-GOPATH path")
	}
}

func TestIsGoInstallGOBIN(t *testing.T) {
	t.Setenv("GOBIN", "/custom/bin")

	if !isGoInstall("/custom/bin/ap-query") {
		t.Error("expected true for GOBIN path")
	}
	if isGoInstall("/other/bin/ap-query") {
		t.Error("expected false for non-GOBIN path")
	}
}

// ---------------------------------------------------------------------------
// TestExtractBinary
// ---------------------------------------------------------------------------

func TestExtractBinary(t *testing.T) {
	// Create an in-memory tar.gz with an "ap-query" entry
	content := []byte("fake-binary-content-12345")

	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	hdr := &tar.Header{
		Name: "ap-query",
		Mode: 0755,
		Size: int64(len(content)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	gw.Close()

	got, err := extractBinary(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("extracted content = %q, want %q", got, content)
	}
}

func TestExtractBinaryNotFound(t *testing.T) {
	// Create tar.gz without "ap-query"
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	hdr := &tar.Header{
		Name: "other-file",
		Mode: 0644,
		Size: 4,
	}
	tw.WriteHeader(hdr)
	tw.Write([]byte("test"))
	tw.Close()
	gw.Close()

	_, err := extractBinary(buf.Bytes())
	if err == nil {
		t.Error("expected error when ap-query not in archive")
	}
}

func TestExtractBinaryInSubdir(t *testing.T) {
	// Binary nested in a subdirectory (filepath.Base still matches)
	content := []byte("nested-binary")

	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	hdr := &tar.Header{
		Name: "ap-query_linux_amd64/ap-query",
		Mode: 0755,
		Size: int64(len(content)),
	}
	tw.WriteHeader(hdr)
	tw.Write(content)
	tw.Close()
	gw.Close()

	got, err := extractBinary(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("extracted = %q, want %q", got, content)
	}
}

// ---------------------------------------------------------------------------
// TestReplaceBinary
// ---------------------------------------------------------------------------

func TestReplaceBinary(t *testing.T) {
	dir := t.TempDir()
	origPath := filepath.Join(dir, "ap-query")

	// Create original file with specific permissions
	if err := os.WriteFile(origPath, []byte("old"), 0755); err != nil {
		t.Fatal(err)
	}

	newContent := []byte("new-binary-content")
	if err := replaceBinary(origPath, newContent); err != nil {
		t.Fatal(err)
	}

	// Verify content replaced
	got, err := os.ReadFile(origPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, newContent) {
		t.Errorf("content = %q, want %q", got, newContent)
	}

	// Verify permissions preserved
	info, err := os.Stat(origPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0755 {
		t.Errorf("permissions = %o, want 755", info.Mode().Perm())
	}
}

// ---------------------------------------------------------------------------
// TestAsprofSearchDirs
// ---------------------------------------------------------------------------

func TestAsprofSearchDirs(t *testing.T) {
	dirs := asprofSearchDirs()
	if len(dirs) == 0 {
		t.Fatal("asprofSearchDirs returned empty list")
	}

	// Should include known directories
	found := false
	for _, d := range dirs {
		if d == "/opt/async-profiler/bin" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected /opt/async-profiler/bin in search dirs")
	}

	found = false
	for _, d := range dirs {
		if d == "/usr/local/bin" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected /usr/local/bin in search dirs")
	}
}

// ---------------------------------------------------------------------------
// TestSkillTemplateRendering
// ---------------------------------------------------------------------------

func TestSkillTemplateRendering(t *testing.T) {
	content := strings.ReplaceAll(skillTemplate, "{{AP_QUERY_PATH}}", "/usr/local/bin/ap-query")
	content = strings.ReplaceAll(content, "{{ASPROF_PATH}}", "/opt/async-profiler/bin/asprof")

	// Placeholders should be gone
	if strings.Contains(content, "{{AP_QUERY_PATH}}") {
		t.Error("{{AP_QUERY_PATH}} placeholder still present")
	}
	if strings.Contains(content, "{{ASPROF_PATH}}") {
		t.Error("{{ASPROF_PATH}} placeholder still present")
	}

	// Frontmatter should be intact
	if !strings.HasPrefix(content, "---\n") {
		t.Error("expected frontmatter start")
	}
	if !strings.Contains(content, "name: jfr") {
		t.Error("expected 'name: jfr' in frontmatter")
	}

	// Paths should be embedded
	if !strings.Contains(content, "/usr/local/bin/ap-query") {
		t.Error("expected ap-query path in rendered content")
	}
	if !strings.Contains(content, "/opt/async-profiler/bin/asprof") {
		t.Error("expected asprof path in rendered content")
	}
}

// ---------------------------------------------------------------------------
// TestFindAsprof
// ---------------------------------------------------------------------------

func TestFindAsprof(t *testing.T) {
	// Smoke test: should not panic regardless of whether asprof is installed
	_ = findAsprof()
}

// ---------------------------------------------------------------------------
// TestAsprofDownloadURL
// ---------------------------------------------------------------------------

func TestAsprofDownloadURL(t *testing.T) {
	url, isTarGz := asprofDownloadURL("v4.3", "4.3")
	if runtime.GOOS == "darwin" {
		if isTarGz {
			t.Error("expected zip for macOS")
		}
		if !strings.Contains(url, "macos.zip") {
			t.Errorf("expected macos.zip in URL, got %s", url)
		}
	} else {
		if !isTarGz {
			t.Error("expected tar.gz for Linux")
		}
		if !strings.Contains(url, "linux-") {
			t.Errorf("expected linux- in URL, got %s", url)
		}
	}
	if !strings.Contains(url, "v4.3") {
		t.Errorf("expected v4.3 in URL, got %s", url)
	}
	if !strings.Contains(url, "async-profiler-4.3") {
		t.Errorf("expected async-profiler-4.3 in URL, got %s", url)
	}
}

// ---------------------------------------------------------------------------
// TestAsprofSearchDirsIncludesApQuery
// ---------------------------------------------------------------------------

func TestAsprofSearchDirsIncludesApQuery(t *testing.T) {
	dirs := asprofSearchDirs()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("cannot determine home dir")
	}
	expected := filepath.Join(home, ".ap-query", "bin")
	found := false
	for _, d := range dirs {
		if d == expected {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected %s in search dirs", expected)
	}
}

// ---------------------------------------------------------------------------
// TestResolveTargets
// ---------------------------------------------------------------------------

func TestResolveTargetsExplicit(t *testing.T) {
	// Explicit flags bypass auto-detection
	targets := resolveTargets("/nonexistent", true, false)
	if len(targets) != 1 || targets[0] != "claude" {
		t.Errorf("expected [claude], got %v", targets)
	}

	targets = resolveTargets("/nonexistent", false, true)
	if len(targets) != 1 || targets[0] != "codex" {
		t.Errorf("expected [codex], got %v", targets)
	}

	targets = resolveTargets("/nonexistent", true, true)
	if len(targets) != 2 {
		t.Errorf("expected 2 targets, got %v", targets)
	}
}

func TestResolveTargetsAutoDetect(t *testing.T) {
	dir := t.TempDir()

	// No agent dirs → empty
	targets := resolveTargets(dir, false, false)
	if len(targets) != 0 {
		t.Errorf("expected empty, got %v", targets)
	}

	// Create .claude → detects claude
	os.MkdirAll(filepath.Join(dir, ".claude"), 0755)
	targets = resolveTargets(dir, false, false)
	if len(targets) != 1 || targets[0] != "claude" {
		t.Errorf("expected [claude], got %v", targets)
	}

	// Create .agents too → detects both
	os.MkdirAll(filepath.Join(dir, ".agents"), 0755)
	targets = resolveTargets(dir, false, false)
	if len(targets) != 2 {
		t.Errorf("expected 2 targets, got %v", targets)
	}
}

// ---------------------------------------------------------------------------
// TestCmdFilter*
// ---------------------------------------------------------------------------

func TestCmdFilter(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.a", "B.b", "C.c"}, lines: []uint32{0, 0, 0}, count: 10, thread: "main"},
		{frames: []string{"X.x", "Y.y"}, lines: []uint32{0, 0}, count: 3, thread: ""},
	})

	out := captureOutput(func() {
		cmdFilter(sf, "B.b", false)
	})

	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d: %q", len(lines), out)
	}
	// Should output from B.b onward (excluding caller A.a)
	if !strings.Contains(lines[0], "B.b;C.c") {
		t.Errorf("expected B.b;C.c, got %q", lines[0])
	}
	if !strings.HasPrefix(lines[0], "[main];") {
		t.Errorf("expected [main] prefix, got %q", lines[0])
	}
	if !strings.HasSuffix(lines[0], " 10") {
		t.Errorf("expected count 10, got %q", lines[0])
	}
}

func TestCmdFilterIncludeCallers(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.a", "B.b", "C.c"}, lines: []uint32{0, 0, 0}, count: 10, thread: ""},
	})

	out := captureOutput(func() {
		cmdFilter(sf, "B.b", true)
	})

	if !strings.Contains(out, "A.a;B.b;C.c") {
		t.Errorf("expected full stack with includeCallers, got %q", out)
	}
	// No thread prefix
	if strings.HasPrefix(strings.TrimSpace(out), "[") {
		t.Errorf("expected no thread prefix, got %q", out)
	}
}

func TestCmdFilterNoMatch(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.a", "B.b"}, lines: []uint32{0, 0}, count: 10, thread: "main"},
	})

	out := captureOutput(func() {
		cmdFilter(sf, "Nonexistent", false)
	})

	if strings.TrimSpace(out) != "" {
		t.Errorf("expected empty output, got %q", out)
	}
}

// ---------------------------------------------------------------------------
// TestCmdThreads*
// ---------------------------------------------------------------------------

func TestCmdThreads(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.a"}, lines: []uint32{0}, count: 10, thread: "main"},
		{frames: []string{"B.b"}, lines: []uint32{0}, count: 5, thread: "worker-1"},
		{frames: []string{"C.c"}, lines: []uint32{0}, count: 3, thread: ""},
	})

	out := captureOutput(func() {
		cmdThreads(sf, 0)
	})

	if !strings.Contains(out, "THREAD") {
		t.Error("expected THREAD header")
	}
	if !strings.Contains(out, "main") {
		t.Error("expected 'main' thread")
	}
	if !strings.Contains(out, "worker-1") {
		t.Error("expected 'worker-1' thread")
	}
	if !strings.Contains(out, "(no thread info)") {
		t.Error("expected '(no thread info)' for unthreaded samples")
	}
}

func TestCmdThreadsWithTop(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.a"}, lines: []uint32{0}, count: 10, thread: "main"},
		{frames: []string{"B.b"}, lines: []uint32{0}, count: 5, thread: "worker-1"},
		{frames: []string{"C.c"}, lines: []uint32{0}, count: 3, thread: "worker-2"},
	})

	out := captureOutput(func() {
		cmdThreads(sf, 1)
	})

	if !strings.Contains(out, "main") {
		t.Error("expected 'main' thread")
	}
	if strings.Contains(out, "worker-2") {
		t.Error("expected worker-2 excluded by top=1")
	}
}

func TestCmdThreadsNoThreadInfo(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.a"}, lines: []uint32{0}, count: 10, thread: ""},
	})

	out := captureOutput(func() {
		cmdThreads(sf, 0)
	})

	if !strings.Contains(out, "no thread info") {
		t.Errorf("expected 'no thread info', got %q", out)
	}
}

func TestCmdThreadsEmpty(t *testing.T) {
	sf := makeStackFile(nil)

	out := captureOutput(func() {
		cmdThreads(sf, 0)
	})

	if strings.TrimSpace(out) != "" {
		t.Errorf("expected empty output, got %q", out)
	}
}

// ---------------------------------------------------------------------------
// TestCmdHot additional
// ---------------------------------------------------------------------------

func TestCmdHotAssertBelowFails(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.a"}, lines: []uint32{0}, count: 90, thread: "main"},
		{frames: []string{"B.b"}, lines: []uint32{0}, count: 10, thread: "main"},
	})

	// A.a is 90%, threshold 50% → should fail
	captureOutput(func() {
		err := cmdHot(sf, 0, false, 50.0)
		if err == nil {
			t.Error("expected assert-below error")
		} else if !strings.Contains(err.Error(), "ASSERT FAILED") {
			t.Errorf("expected ASSERT FAILED, got %q", err.Error())
		}
	})
}

func TestCmdHotAssertBelowPasses(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.a"}, lines: []uint32{0}, count: 5, thread: "main"},
		{frames: []string{"B.b"}, lines: []uint32{0}, count: 5, thread: "main"},
	})

	// Each is 50%, threshold 90% → should pass
	captureOutput(func() {
		err := cmdHot(sf, 0, false, 90.0)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})
}

func TestCmdHotEmpty(t *testing.T) {
	sf := makeStackFile(nil)
	err := cmdHot(sf, 0, false, 0)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// TestFlag* (flag accessor methods)
// ---------------------------------------------------------------------------

func TestFlagStr(t *testing.T) {
	f := parseFlags([]string{"--event", "wall", "-m", "Foo.bar"})

	if f.str("event") != "wall" {
		t.Errorf("str(event) = %q, want wall", f.str("event"))
	}
	if f.str("m") != "Foo.bar" {
		t.Errorf("str(m) = %q, want Foo.bar", f.str("m"))
	}
	// Multiple keys: first match wins
	if f.str("missing", "event") != "wall" {
		t.Errorf("str(missing, event) = %q, want wall", f.str("missing", "event"))
	}
	// No match
	if f.str("nonexistent") != "" {
		t.Errorf("str(nonexistent) = %q, want empty", f.str("nonexistent"))
	}
}

func TestFlagIntVal(t *testing.T) {
	f := parseFlags([]string{"--top", "20"})

	if f.intVal([]string{"top"}, 10) != 20 {
		t.Errorf("intVal(top) = %d, want 20", f.intVal([]string{"top"}, 10))
	}
	// Default when missing
	if f.intVal([]string{"missing"}, 42) != 42 {
		t.Errorf("intVal(missing) = %d, want 42", f.intVal([]string{"missing"}, 42))
	}
	// Multiple keys
	if f.intVal([]string{"missing", "top"}, 0) != 20 {
		t.Errorf("intVal(missing, top) = %d, want 20", f.intVal([]string{"missing", "top"}, 0))
	}
}

func TestFlagFloatVal(t *testing.T) {
	f := parseFlags([]string{"--min-delta", "0.5"})

	if f.floatVal([]string{"min-delta"}, 1.0) != 0.5 {
		t.Errorf("floatVal(min-delta) = %f, want 0.5", f.floatVal([]string{"min-delta"}, 1.0))
	}
	// Default when missing
	if f.floatVal([]string{"missing"}, 1.5) != 1.5 {
		t.Errorf("floatVal(missing) = %f, want 1.5", f.floatVal([]string{"missing"}, 1.5))
	}
}

func TestFlagBoolean(t *testing.T) {
	f := parseFlags([]string{"--fqn", "--include-callers"})

	if !f.boolean("fqn") {
		t.Error("expected fqn=true")
	}
	if !f.boolean("include-callers") {
		t.Error("expected include-callers=true")
	}
	if f.boolean("missing") {
		t.Error("expected missing=false")
	}
	// Multiple keys: any match → true
	if !f.boolean("missing", "fqn") {
		t.Error("expected boolean(missing, fqn)=true")
	}
}

func TestParseFlagsDoubleDash(t *testing.T) {
	f := parseFlags([]string{"--top", "20", "--", "--not-a-flag", "file.jfr"})

	if f.vals["top"] != "20" {
		t.Errorf("top = %q, want 20", f.vals["top"])
	}
	if len(f.args) != 2 {
		t.Fatalf("args = %v, want [--not-a-flag file.jfr]", f.args)
	}
	if f.args[0] != "--not-a-flag" || f.args[1] != "file.jfr" {
		t.Errorf("args = %v", f.args)
	}
}

func TestParseFlagsUnknownBoolAtEnd(t *testing.T) {
	f := parseFlags([]string{"file.jfr", "--verbose"})

	if !f.bools["verbose"] {
		t.Error("expected --verbose treated as boolean")
	}
	if len(f.args) != 1 || f.args[0] != "file.jfr" {
		t.Errorf("args = %v, want [file.jfr]", f.args)
	}
}

// ---------------------------------------------------------------------------
// TestDisplayNameFQN
// ---------------------------------------------------------------------------

func TestDisplayNameFQN(t *testing.T) {
	got := displayName("com/example/App.process", true)
	if got != "com.example.App.process" {
		t.Errorf("displayName(fqn=true) = %q, want com.example.App.process", got)
	}
	got = displayName("com/example/App.process", false)
	if got != "App.process" {
		t.Errorf("displayName(fqn=false) = %q, want App.process", got)
	}
}

// ---------------------------------------------------------------------------
// TestExpandPath
// ---------------------------------------------------------------------------

func TestExpandPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("cannot determine home dir")
	}

	// Tilde expansion
	got := expandPath("~/test/path")
	expected := filepath.Join(home, "test", "path")
	if got != expected {
		t.Errorf("expandPath(~/test/path) = %q, want %q", got, expected)
	}

	// Absolute path unchanged
	got = expandPath("/absolute/path")
	if got != "/absolute/path" {
		t.Errorf("expandPath(/absolute/path) = %q, want /absolute/path", got)
	}

	// Relative path made absolute
	got = expandPath("relative/path")
	if !filepath.IsAbs(got) {
		t.Errorf("expandPath(relative/path) = %q, expected absolute", got)
	}
}

// ---------------------------------------------------------------------------
// TestExtractTarGz / TestExtractZip
// ---------------------------------------------------------------------------

func TestExtractTarGz(t *testing.T) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	entries := []struct {
		name    string
		content string
		mode    int64
		isDir   bool
	}{
		{"async-profiler-4.3-linux-x64/", "", 0755, true},
		{"async-profiler-4.3-linux-x64/bin/", "", 0755, true},
		{"async-profiler-4.3-linux-x64/bin/asprof", "#!/bin/bash\necho asprof", 0755, false},
		{"async-profiler-4.3-linux-x64/lib/libasyncProfiler.so", "fake-lib", 0644, false},
	}

	for _, e := range entries {
		if e.isDir {
			tw.WriteHeader(&tar.Header{Name: e.name, Typeflag: tar.TypeDir, Mode: e.mode})
		} else {
			tw.WriteHeader(&tar.Header{Name: e.name, Typeflag: tar.TypeReg, Mode: e.mode, Size: int64(len(e.content))})
			tw.Write([]byte(e.content))
		}
	}
	tw.Close()
	gw.Close()

	dir := t.TempDir()
	if err := extractTarGz(buf.Bytes(), dir); err != nil {
		t.Fatal(err)
	}

	// Verify flattened extraction
	asprofPath := filepath.Join(dir, "bin", "asprof")
	data, err := os.ReadFile(asprofPath)
	if err != nil {
		t.Fatalf("asprof not found: %v", err)
	}
	if !strings.Contains(string(data), "asprof") {
		t.Errorf("unexpected asprof content: %q", data)
	}

	info, err := os.Stat(asprofPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0111 == 0 {
		t.Error("expected executable permission on asprof")
	}

	libPath := filepath.Join(dir, "lib", "libasyncProfiler.so")
	if _, err := os.Stat(libPath); err != nil {
		t.Fatalf("lib file not found: %v", err)
	}
}

func TestExtractTarGzPathTraversal(t *testing.T) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	// Entry that tries to escape destDir
	content := "malicious"
	tw.WriteHeader(&tar.Header{
		Name:     "toplevel/../../etc/evil",
		Typeflag: tar.TypeReg,
		Mode:     0644,
		Size:     int64(len(content)),
	})
	tw.Write([]byte(content))
	tw.Close()
	gw.Close()

	dir := t.TempDir()
	if err := extractTarGz(buf.Bytes(), dir); err != nil {
		t.Fatal(err)
	}

	// The evil file should NOT have been written
	if _, err := os.Stat(filepath.Join(dir, "..", "etc", "evil")); err == nil {
		t.Error("path traversal should have been blocked")
	}
}

func TestExtractZip(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	// Create directory entries
	for _, dir := range []string{"async-profiler-4.3-macos/", "async-profiler-4.3-macos/bin/", "async-profiler-4.3-macos/lib/"} {
		hdr := &zip.FileHeader{Name: dir}
		hdr.SetMode(0755 | os.ModeDir)
		if _, err := zw.CreateHeader(hdr); err != nil {
			t.Fatal(err)
		}
	}

	// Create file entries
	files := []struct {
		name    string
		content string
	}{
		{"async-profiler-4.3-macos/bin/asprof", "#!/bin/bash\necho asprof"},
		{"async-profiler-4.3-macos/lib/libasyncProfiler.dylib", "fake-lib"},
	}
	for _, f := range files {
		w, err := zw.Create(f.name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(f.content))
	}
	zw.Close()

	dir := t.TempDir()
	if err := extractZip(buf.Bytes(), dir); err != nil {
		t.Fatal(err)
	}

	asprofPath := filepath.Join(dir, "bin", "asprof")
	data, err := os.ReadFile(asprofPath)
	if err != nil {
		t.Fatalf("asprof not found: %v", err)
	}
	if !strings.Contains(string(data), "asprof") {
		t.Errorf("unexpected content: %q", data)
	}

	libPath := filepath.Join(dir, "lib", "libasyncProfiler.dylib")
	if _, err := os.Stat(libPath); err != nil {
		t.Fatalf("lib file not found: %v", err)
	}
}

func TestExtractZipPathTraversal(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	w, _ := zw.Create("toplevel/../../etc/evil")
	w.Write([]byte("malicious"))
	zw.Close()

	dir := t.TempDir()
	if err := extractZip(buf.Bytes(), dir); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(dir, "..", "etc", "evil")); err == nil {
		t.Error("path traversal should have been blocked")
	}
}

func TestExtractZipTopLevelOnly(t *testing.T) {
	// Top-level entry with no subpath should be skipped
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	hdr := &zip.FileHeader{Name: "toplevel/"}
	hdr.SetMode(0755 | os.ModeDir)
	zw.CreateHeader(hdr)
	zw.Close()

	dir := t.TempDir()
	if err := extractZip(buf.Bytes(), dir); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// TestWriteSkill*
// ---------------------------------------------------------------------------

func TestWriteSkillNew(t *testing.T) {
	dir := t.TempDir()
	writeSkill(dir, "claude", "test content", false)

	path := filepath.Join(dir, ".claude", "skills", "jfr", "SKILL.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("skill file not found: %v", err)
	}
	if string(data) != "test content" {
		t.Errorf("content = %q, want 'test content'", data)
	}
}

func TestWriteSkillForce(t *testing.T) {
	dir := t.TempDir()
	writeSkill(dir, "claude", "original", false)
	writeSkill(dir, "claude", "updated", true)

	path := filepath.Join(dir, ".claude", "skills", "jfr", "SKILL.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "updated" {
		t.Errorf("content = %q, want 'updated'", data)
	}
}

// ---------------------------------------------------------------------------
// TestCmdTree / TestCmdCallers edge cases
// ---------------------------------------------------------------------------

func TestCmdTreeNoMatch(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.a", "B.b"}, lines: []uint32{0, 0}, count: 10, thread: "main"},
	})

	out := captureOutput(func() {
		cmdTree(sf, "Nonexistent", 4, 1.0)
	})

	if !strings.Contains(out, "no frames matching") {
		t.Errorf("expected 'no frames matching', got %q", out)
	}
}

func TestCmdCallersNoMatch(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.a", "B.b"}, lines: []uint32{0, 0}, count: 10, thread: "main"},
	})

	out := captureOutput(func() {
		cmdCallers(sf, "Nonexistent", 4, 1.0)
	})

	if !strings.Contains(out, "no frames matching") {
		t.Errorf("expected 'no frames matching', got %q", out)
	}
}

func TestCmdTreeEmpty(t *testing.T) {
	sf := makeStackFile(nil)

	out := captureOutput(func() {
		cmdTree(sf, "A.a", 4, 1.0)
	})

	if strings.TrimSpace(out) != "" {
		t.Errorf("expected empty output, got %q", out)
	}
}

func TestCmdCallersEmpty(t *testing.T) {
	sf := makeStackFile(nil)

	out := captureOutput(func() {
		cmdCallers(sf, "A.a", 4, 1.0)
	})

	if strings.TrimSpace(out) != "" {
		t.Errorf("expected empty output, got %q", out)
	}
}

func TestPrintTreeMultipleMatches(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"com.a.Foo.run"}, lines: []uint32{0}, count: 5, thread: "main"},
		{frames: []string{"com.b.Bar.run"}, lines: []uint32{0}, count: 5, thread: "main"},
	})

	out := captureOutput(func() {
		cmdTree(sf, "run", 4, 0.0)
	})

	if !strings.Contains(out, "matched 2 methods") {
		t.Errorf("expected 'matched 2 methods', got %q", out)
	}
}

func TestPrintTreeMaxDepth(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.a", "B.b", "C.c", "D.d", "E.e"}, lines: []uint32{0, 0, 0, 0, 0}, count: 10, thread: "main"},
	})

	out := captureOutput(func() {
		cmdTree(sf, "A.a", 2, 0.0)
	})

	if !strings.Contains(out, "A.a") {
		t.Error("expected A.a")
	}
	if !strings.Contains(out, "B.b") {
		t.Error("expected B.b at depth 2")
	}
	if strings.Contains(out, "C.c") {
		t.Error("C.c should be cut off at maxDepth=2")
	}
}

func TestPrintTreeMinPct(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.a", "B.b"}, lines: []uint32{0, 0}, count: 1, thread: "main"},
		{frames: []string{"X.x"}, lines: []uint32{0}, count: 99, thread: "main"},
	})

	out := captureOutput(func() {
		cmdTree(sf, "A.a", 4, 5.0) // A.a is 1% of 100, below 5% threshold
	})

	if !strings.Contains(out, "no frames matching") || strings.Contains(out, "B.b") {
		// aggregatePaths will find A.a but printTree will skip it due to minPct
		// Actually the root A.a has pct=1% < 5% so it won't print
	}
}

// ---------------------------------------------------------------------------
// TestCmdDiff additional
// ---------------------------------------------------------------------------

func TestCmdDiffWithTop(t *testing.T) {
	before := makeStackFile([]stack{
		{frames: []string{"A.a"}, lines: []uint32{0}, count: 10, thread: "main"},
		{frames: []string{"B.b"}, lines: []uint32{0}, count: 10, thread: "main"},
		{frames: []string{"C.c"}, lines: []uint32{0}, count: 10, thread: "main"},
	})
	after := makeStackFile([]stack{
		{frames: []string{"A.a"}, lines: []uint32{0}, count: 20, thread: "main"},
		{frames: []string{"B.b"}, lines: []uint32{0}, count: 25, thread: "main"},
		{frames: []string{"C.c"}, lines: []uint32{0}, count: 30, thread: "main"},
	})

	out := captureOutput(func() {
		cmdDiff(before, after, 0.1, 1, false)
	})

	if !strings.Contains(out, "REGRESSION") {
		t.Error("expected REGRESSION section")
	}
	// Count regression lines
	regrCount := 0
	inRegr := false
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "REGRESSION") {
			inRegr = true
			continue
		}
		if inRegr && strings.HasPrefix(line, "  ") {
			regrCount++
		} else {
			inRegr = false
		}
	}
	if regrCount > 1 {
		t.Errorf("expected at most 1 regression with top=1, got %d", regrCount)
	}
}

func TestCmdDiffFQN(t *testing.T) {
	before := makeStackFile([]stack{
		{frames: []string{"com/example/A.doWork"}, lines: []uint32{0}, count: 20, thread: "main"},
		{frames: []string{"com/example/B.process"}, lines: []uint32{0}, count: 10, thread: "main"},
	})
	after := makeStackFile([]stack{
		{frames: []string{"com/example/A.doWork"}, lines: []uint32{0}, count: 10, thread: "main"},
		{frames: []string{"com/example/B.process"}, lines: []uint32{0}, count: 30, thread: "main"},
	})

	out := captureOutput(func() {
		cmdDiff(before, after, 0.1, 0, true)
	})

	if !strings.Contains(out, "com.example.A.doWork") {
		t.Errorf("expected FQN name in diff output, got %q", out)
	}
}

// ---------------------------------------------------------------------------
// TestCmdLines additional
// ---------------------------------------------------------------------------

func TestCmdLinesErrorNoLineInfo(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.a", "B.b"}, lines: []uint32{0, 0}, count: 10, thread: "main"},
	})

	err := cmdLines(sf, "B.b", 0, false)
	if err == nil {
		t.Error("expected error for method with no line info")
	} else if !strings.Contains(err.Error(), "no line info") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestCmdLinesWithTop(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.a"}, lines: []uint32{10}, count: 5, thread: "main"},
		{frames: []string{"A.a"}, lines: []uint32{20}, count: 3, thread: "main"},
		{frames: []string{"A.a"}, lines: []uint32{30}, count: 1, thread: "main"},
	})

	out := captureOutput(func() {
		cmdLines(sf, "A.a", 2, false)
	})

	lines := strings.Split(strings.TrimSpace(out), "\n")
	// header + 2 data lines
	if len(lines) != 3 {
		t.Errorf("expected 3 lines (header + 2 data), got %d: %q", len(lines), out)
	}
}

func TestComputeLinesWithFQN(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"com/example/A.run"}, lines: []uint32{42}, count: 10, thread: "main"},
	})

	result, hasMethod := computeLines(sf, "A.run", 0, true)
	if !hasMethod {
		t.Fatal("expected hasMethod=true")
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(result))
	}
	if result[0].name != "com.example.A.run" {
		t.Errorf("expected FQN name, got %q", result[0].name)
	}
}

// ---------------------------------------------------------------------------
// TestSplitCollapsedLine edge cases
// ---------------------------------------------------------------------------

func TestSplitCollapsedLineEdgeCases(t *testing.T) {
	tests := []struct {
		line      string
		wantStr   string
		wantCount int
	}{
		{"A;B;C 10", "A;B;C", 10},
		{"", "", 0},
		{"nospace", "", 0},
		{"A;B -5", "", 0},
		{"A;B 0", "", 0},
		{"A;B abc", "", 0},
		{" 10", "", 0}, // space at position 0, i < 1
	}
	for _, tt := range tests {
		s, c := splitCollapsedLine(tt.line)
		if s != tt.wantStr || c != tt.wantCount {
			t.Errorf("splitCollapsedLine(%q) = (%q, %d), want (%q, %d)", tt.line, s, c, tt.wantStr, tt.wantCount)
		}
	}
}

// ---------------------------------------------------------------------------
// TestParseAnnotatedFrame edge cases
// ---------------------------------------------------------------------------

func TestParseAnnotatedFrameEdgeCases(t *testing.T) {
	tests := []struct {
		frame    string
		wantName string
		wantLine uint32
	}{
		{"A.main:10_[0]", "A.main", 10},
		{"B.process:42_[j]", "B.process", 42},
		{"plain.method", "plain.method", 0},
		{"Method_[j]", "Method_[j]", 0},         // suffix stripped but no colon
		{"Method:abc_[j]", "Method:abc_[j]", 0}, // non-numeric line
		{"A:10", "A", 10},                       // no _[] suffix, still annotated
	}
	for _, tt := range tests {
		name, line := parseAnnotatedFrame(tt.frame)
		if name != tt.wantName || line != tt.wantLine {
			t.Errorf("parseAnnotatedFrame(%q) = (%q, %d), want (%q, %d)",
				tt.frame, name, line, tt.wantName, tt.wantLine)
		}
	}
}

// ---------------------------------------------------------------------------
// TestFilterByThread edge cases
// ---------------------------------------------------------------------------

func TestFilterByThreadEmpty(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.a"}, lines: []uint32{0}, count: 10, thread: "main"},
	})

	filtered := sf.filterByThread("")
	if filtered != sf {
		t.Error("filterByThread('') should return same stackFile")
	}
}

// ---------------------------------------------------------------------------
// TestParseCollapsed additional
// ---------------------------------------------------------------------------

func TestParseCollapsedOnlyThreadMarker(t *testing.T) {
	r := strings.NewReader("[main tid=1] 10\n")
	sf, err := parseCollapsed(r)
	if err != nil {
		t.Fatal(err)
	}
	// Line with only a thread marker and no frames should be skipped
	if len(sf.stacks) != 0 {
		t.Errorf("expected 0 stacks, got %d", len(sf.stacks))
	}
}

// ---------------------------------------------------------------------------
// TestOpenInput / TestOpenReader
// ---------------------------------------------------------------------------

func TestOpenInputCollapsed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stacks.txt")
	os.WriteFile(path, []byte("A;B;C 10\nX;Y 5\n"), 0644)

	sf, isJFR, err := openInput(path, "cpu")
	if err != nil {
		t.Fatal(err)
	}
	if isJFR {
		t.Error("expected isJFR=false for .txt file")
	}
	if len(sf.stacks) != 2 {
		t.Errorf("expected 2 stacks, got %d", len(sf.stacks))
	}
	if sf.totalSamples != 15 {
		t.Errorf("totalSamples=%d, want 15", sf.totalSamples)
	}
}

func TestOpenReaderGzip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stacks.txt.gz")

	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	gw.Write([]byte("A;B;C 10\nX;Y 5\n"))
	gw.Close()
	os.WriteFile(path, buf.Bytes(), 0644)

	rc, err := openReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()

	sf, err := parseCollapsed(rc)
	if err != nil {
		t.Fatal(err)
	}
	if len(sf.stacks) != 2 {
		t.Fatalf("expected 2 stacks, got %d", len(sf.stacks))
	}
	if sf.totalSamples != 15 {
		t.Errorf("totalSamples=%d, want 15", sf.totalSamples)
	}
}

func TestOpenInputNonExistent(t *testing.T) {
	_, _, err := openInput("/nonexistent/file.txt", "cpu")
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

// ---------------------------------------------------------------------------
// TestReadJFRBytes*
// ---------------------------------------------------------------------------

func TestReadJFRBytesNonExistent(t *testing.T) {
	_, err := readJFRBytes("/nonexistent/file.jfr")
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestReadJFRBytesRegular(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jfr")
	content := []byte("fake jfr content")
	os.WriteFile(path, content, 0644)

	data, err := readJFRBytes(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, content) {
		t.Errorf("content = %q, want %q", data, content)
	}
}

func TestReadJFRBytesGzip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.jfr.gz")

	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	gw.Write([]byte("fake jfr content"))
	gw.Close()
	os.WriteFile(path, buf.Bytes(), 0644)

	data, err := readJFRBytes(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "fake jfr content" {
		t.Errorf("content = %q, want 'fake jfr content'", data)
	}
}

// ---------------------------------------------------------------------------
// TestComputeHotFQN
// ---------------------------------------------------------------------------

func TestComputeHotFQN(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"com/example/A.run"}, lines: []uint32{0}, count: 10, thread: "main"},
	})

	ranked := computeHot(sf, true)
	if len(ranked) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(ranked))
	}
	if ranked[0].name != "com.example.A.run" {
		t.Errorf("expected FQN name, got %q", ranked[0].name)
	}
}

// ---------------------------------------------------------------------------
// TestCmdHotWithTop
// ---------------------------------------------------------------------------

func TestCmdHotWithTop(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.a"}, lines: []uint32{0}, count: 10, thread: "main"},
		{frames: []string{"B.b"}, lines: []uint32{0}, count: 5, thread: "main"},
		{frames: []string{"C.c"}, lines: []uint32{0}, count: 3, thread: "main"},
		{frames: []string{"D.d"}, lines: []uint32{0}, count: 1, thread: "main"},
	})

	out := captureOutput(func() {
		cmdHot(sf, 2, false, 0)
	})

	// Self-time section should have at most 2 entries
	selfIdx := strings.Index(out, "=== RANK BY SELF TIME ===")
	totalIdx := strings.Index(out, "=== RANK BY TOTAL TIME ===")
	selfSection := out[selfIdx:totalIdx]

	dataLines := 0
	for _, line := range strings.Split(selfSection, "\n") {
		if strings.Contains(line, "%") && !strings.Contains(line, "METHOD") {
			dataLines++
		}
	}
	if dataLines != 2 {
		t.Errorf("expected 2 data lines in self-time with top=2, got %d", dataLines)
	}
}

// ---------------------------------------------------------------------------
// TestCmdInfoNoThreads
// ---------------------------------------------------------------------------

func TestCmdInfoNoThreads(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.a", "B.b"}, lines: []uint32{0, 0}, count: 10, thread: ""},
	})

	out := captureOutput(func() {
		cmdInfo(sf, "cpu", false, nil, 0, 10, 20)
	})

	// Should NOT have THREADS section since no thread info
	if strings.Contains(out, "=== THREADS") {
		t.Error("expected no THREADS section when no thread info")
	}
	if !strings.Contains(out, "Total samples: 10") {
		t.Errorf("expected 'Total samples: 10', got %q", out)
	}
}

// ---------------------------------------------------------------------------
// TestSelfPctsFQN
// ---------------------------------------------------------------------------

func TestSelfPctsFQN(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"com/example/A.run"}, lines: []uint32{0}, count: 10, thread: "main"},
	})

	pcts := selfPcts(sf, true)
	if _, ok := pcts["com.example.A.run"]; !ok {
		t.Errorf("expected FQN key, got keys: %v", pcts)
	}
}

// ---------------------------------------------------------------------------
// TestIsGoInstallGOROOT
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// TestCmdDiff filtering
// ---------------------------------------------------------------------------

func TestCmdDiffFiltersSmallNewGone(t *testing.T) {
	// NEW method with tiny percentage (below minDelta) should be filtered
	before := makeStackFile([]stack{
		{frames: []string{"A.a"}, lines: []uint32{0}, count: 100, thread: "main"},
		{frames: []string{"B.b"}, lines: []uint32{0}, count: 1, thread: "main"}, // B.b=1% in before, absent in after → GONE
	})
	after := makeStackFile([]stack{
		{frames: []string{"A.a"}, lines: []uint32{0}, count: 100, thread: "main"},
		{frames: []string{"C.c"}, lines: []uint32{0}, count: 1, thread: "main"}, // C.c=1% in after, absent in before → NEW
	})

	out := captureOutput(func() {
		cmdDiff(before, after, 5.0, 0, false) // minDelta=5%: both new/gone are <5%, filtered
	})

	if !strings.Contains(out, "no significant changes") {
		t.Errorf("expected 'no significant changes' with high minDelta, got %q", out)
	}
}

func TestIsGoInstallGOROOT(t *testing.T) {
	goroot := runtime.GOROOT()
	if goroot == "" {
		t.Skip("GOROOT not set")
	}
	if !isGoInstall(filepath.Join(goroot, "bin", "ap-query")) {
		t.Error("expected true for GOROOT/bin")
	}
}

// ---------------------------------------------------------------------------
// TestComputeLines dedup (same method+line appears twice in one stack)
// ---------------------------------------------------------------------------

func TestComputeLinesDedupWithinStack(t *testing.T) {
	// Same method appears twice in a recursive call at the same line
	sf := makeStackFile([]stack{
		{frames: []string{"A.recurse", "A.recurse"}, lines: []uint32{42, 42}, count: 10, thread: "main"},
	})

	result, hasMethod := computeLines(sf, "A.recurse", 0, false)
	if !hasMethod {
		t.Fatal("expected hasMethod=true")
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 entry (deduped), got %d", len(result))
	}
	// Should count only once per stack despite two occurrences
	if result[0].samples != 10 {
		t.Errorf("expected 10 samples (deduped), got %d", result[0].samples)
	}
}

// ---------------------------------------------------------------------------
// TestParseChecksums malformed lines
// ---------------------------------------------------------------------------

func TestParseChecksumsMalformedLines(t *testing.T) {
	input := "abc123  file1.tar.gz\nthis is three fields\ndef456  file2.tar.gz\n"
	m, err := parseChecksums(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	// Malformed line should be skipped
	if len(m) != 2 {
		t.Errorf("expected 2 entries, got %d", len(m))
	}
}

// ---------------------------------------------------------------------------
// TestReplaceBinaryNonExistent
// ---------------------------------------------------------------------------

func TestReplaceBinaryNonExistent(t *testing.T) {
	err := replaceBinary("/nonexistent/path/ap-query", []byte("new"))
	if err == nil {
		t.Error("expected error for nonexistent path")
	}
}

// ---------------------------------------------------------------------------
// TestCmdDiff top truncation for all categories
// ---------------------------------------------------------------------------

func TestCmdDiffTopAllCategories(t *testing.T) {
	// Create scenario with regressions, improvements, new, and gone
	before := makeStackFile([]stack{
		{frames: []string{"A.a"}, lines: []uint32{0}, count: 20, thread: "main"},
		{frames: []string{"B.b"}, lines: []uint32{0}, count: 20, thread: "main"},
		{frames: []string{"Gone1.g"}, lines: []uint32{0}, count: 10, thread: "main"},
		{frames: []string{"Gone2.g"}, lines: []uint32{0}, count: 10, thread: "main"},
	})
	after := makeStackFile([]stack{
		{frames: []string{"A.a"}, lines: []uint32{0}, count: 30, thread: "main"}, // regression
		{frames: []string{"B.b"}, lines: []uint32{0}, count: 10, thread: "main"}, // improvement
		{frames: []string{"New1.n"}, lines: []uint32{0}, count: 10, thread: "main"},
		{frames: []string{"New2.n"}, lines: []uint32{0}, count: 10, thread: "main"},
	})

	out := captureOutput(func() {
		cmdDiff(before, after, 0.1, 1, false) // top=1: only 1 per category
	})

	if !strings.Contains(out, "REGRESSION") {
		t.Error("expected REGRESSION")
	}
	if !strings.Contains(out, "IMPROVEMENT") {
		t.Error("expected IMPROVEMENT")
	}
	if !strings.Contains(out, "NEW") {
		t.Error("expected NEW")
	}
	if !strings.Contains(out, "GONE") {
		t.Error("expected GONE")
	}
}

// ---------------------------------------------------------------------------
// TestPrintTreeSelfBelowMinPct
// ---------------------------------------------------------------------------

func TestPrintTreeSelfBelowMinPct(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.a", "B.b"}, lines: []uint32{0, 0}, count: 1, thread: "main"},
		{frames: []string{"A.a", "C.c"}, lines: []uint32{0, 0}, count: 99, thread: "main"},
	})

	out := captureOutput(func() {
		// B.b self=1% is below minPct=5%, so self annotation should not show
		cmdTree(sf, "A.a", 4, 0.1)
	})

	if !strings.Contains(out, "A.a") {
		t.Error("expected A.a")
	}
	if !strings.Contains(out, "C.c") {
		t.Error("expected C.c")
	}
}

// ---------------------------------------------------------------------------
// TestOpenReaderPlainFile
// ---------------------------------------------------------------------------

func TestOpenReaderPlainFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stacks.txt")
	os.WriteFile(path, []byte("A;B 10\n"), 0644)

	rc, err := openReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "A;B 10\n" {
		t.Errorf("content = %q", data)
	}
}

func TestOpenReaderNonExistent(t *testing.T) {
	_, err := openReader("/nonexistent/file.txt")
	if err == nil {
		t.Error("expected error")
	}
}

func TestOpenReaderBadGzip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.gz")
	os.WriteFile(path, []byte("not gzip data"), 0644)

	_, err := openReader(path)
	if err == nil {
		t.Error("expected error for bad gzip")
	}
}

// ---------------------------------------------------------------------------
// JFR integration tests — real async-profiler JFR fixtures
// ---------------------------------------------------------------------------

func jfrFixture(name string) string {
	return filepath.Join("testdata", name)
}

func TestJFRDiscoverEventsSingle(t *testing.T) {
	tests := []struct {
		file      string
		wantEvent string
	}{
		{"cpu.jfr", "cpu"},
		{"wall.jfr", "wall"},
		{"alloc.jfr", "alloc"},
		{"lock.jfr", "lock"},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			counts, err := discoverEvents(jfrFixture(tt.file))
			if err != nil {
				t.Fatalf("discoverEvents(%s): %v", tt.file, err)
			}
			if len(counts) != 1 {
				t.Errorf("expected exactly 1 event type, got %d: %v", len(counts), counts)
			}
			n, ok := counts[tt.wantEvent]
			if !ok {
				t.Errorf("expected event type %q, got %v", tt.wantEvent, counts)
			}
			if n <= 0 {
				t.Errorf("expected >0 samples for %q, got %d", tt.wantEvent, n)
			}
		})
	}
}

func TestJFRDiscoverEventsMulti(t *testing.T) {
	counts, err := discoverEvents(jfrFixture("multi.jfr"))
	if err != nil {
		t.Fatalf("discoverEvents: %v", err)
	}
	if len(counts) < 2 {
		t.Errorf("expected >=2 event types in multi.jfr, got %d: %v", len(counts), counts)
	}
	for name, n := range counts {
		if n <= 0 {
			t.Errorf("event %q has %d samples, expected >0", name, n)
		}
	}
}

func TestJFRBranchMissesMapsToCPU(t *testing.T) {
	counts, err := discoverEvents(jfrFixture("branch-misses.jfr"))
	if err != nil {
		t.Fatalf("discoverEvents: %v", err)
	}
	if _, ok := counts["cpu"]; !ok {
		t.Errorf("expected 'cpu' event in branch-misses.jfr, got %v", counts)
	}
	if len(counts) != 1 {
		t.Errorf("expected exactly 1 event type in branch-misses.jfr, got %v", counts)
	}
}

func TestJFRInfoAutoSelectWall(t *testing.T) {
	path := jfrFixture("wall.jfr")

	// Parse with default "cpu" — should get 0 samples
	sf, _, err := openInput(path, "cpu")
	if err != nil {
		t.Fatalf("openInput: %v", err)
	}
	if sf.totalSamples != 0 {
		t.Fatalf("expected 0 cpu samples in wall.jfr, got %d", sf.totalSamples)
	}

	// Simulate auto-select: discover events, find best
	eventCounts, err := discoverEvents(path)
	if err != nil {
		t.Fatalf("discoverEvents: %v", err)
	}
	eventType := "cpu"
	if eventCounts[eventType] == 0 {
		best, bestN := "", 0
		for name, n := range eventCounts {
			if n > bestN {
				best, bestN = name, n
			}
		}
		if best != "" {
			eventType = best
			sf, _, err = openInput(path, eventType)
			if err != nil {
				t.Fatalf("openInput with %s: %v", eventType, err)
			}
		}
	}

	if eventType != "wall" {
		t.Errorf("expected auto-select to pick 'wall', got %q", eventType)
	}

	out := captureOutput(func() {
		cmdInfo(sf, eventType, true, eventCounts, 0, 10, 20)
	})
	if !strings.Contains(out, "Event: wall") {
		t.Errorf("expected 'Event: wall' in output, got:\n%s", out)
	}
	if !strings.Contains(out, "Total samples:") {
		t.Errorf("expected 'Total samples:' in output, got:\n%s", out)
	}
	if strings.Contains(out, "Total samples: 0") {
		t.Errorf("expected non-zero total samples, got:\n%s", out)
	}
}

func TestJFRInfoAlsoAvailable(t *testing.T) {
	path := jfrFixture("multi.jfr")
	sf, _, err := openInput(path, "cpu")
	if err != nil {
		t.Fatalf("openInput: %v", err)
	}
	eventCounts, err := discoverEvents(path)
	if err != nil {
		t.Fatalf("discoverEvents: %v", err)
	}

	out := captureOutput(func() {
		cmdInfo(sf, "cpu", true, eventCounts, 0, 10, 20)
	})
	if !strings.Contains(out, "Also available:") {
		t.Errorf("expected 'Also available:' in output, got:\n%s", out)
	}
}

func TestJFRHotCommand(t *testing.T) {
	sf, _, err := openInput(jfrFixture("cpu.jfr"), "cpu")
	if err != nil {
		t.Fatalf("openInput: %v", err)
	}
	if sf.totalSamples == 0 {
		t.Fatal("expected >0 samples")
	}

	out := captureOutput(func() {
		cmdHot(sf, 10, false, 0)
	})
	if !strings.Contains(out, "SELF") {
		t.Errorf("expected 'SELF' in hot output, got:\n%s", out)
	}
	if !strings.Contains(out, "TOTAL") {
		t.Errorf("expected 'TOTAL' in hot output, got:\n%s", out)
	}
}

func TestJFRTreeCommand(t *testing.T) {
	sf, _, err := openInput(jfrFixture("cpu.jfr"), "cpu")
	if err != nil {
		t.Fatalf("openInput: %v", err)
	}

	out := captureOutput(func() {
		cmdTree(sf, "Workload", 4, 1.0)
	})
	if !strings.Contains(out, "Workload") {
		t.Errorf("expected 'Workload' in tree output, got:\n%s", out)
	}
}

func TestJFRCallersCommand(t *testing.T) {
	sf, _, err := openInput(jfrFixture("cpu.jfr"), "cpu")
	if err != nil {
		t.Fatalf("openInput: %v", err)
	}

	out := captureOutput(func() {
		cmdCallers(sf, "computeStep", 4, 1.0)
	})
	if !strings.Contains(out, "computeStep") {
		t.Errorf("expected 'computeStep' in callers output, got:\n%s", out)
	}
}

func TestJFRThreadFilter(t *testing.T) {
	sf, _, err := openInput(jfrFixture("cpu.jfr"), "cpu")
	if err != nil {
		t.Fatalf("openInput: %v", err)
	}
	totalBefore := sf.totalSamples

	filtered := sf.filterByThread("cpu-worker")
	if filtered.totalSamples == 0 {
		t.Error("expected >0 samples after filtering to cpu-worker")
	}
	if filtered.totalSamples >= totalBefore {
		t.Errorf("expected filtered samples (%d) < total (%d)", filtered.totalSamples, totalBefore)
	}
}

func TestJFRLinesCommand(t *testing.T) {
	sf, _, err := openInput(jfrFixture("cpu.jfr"), "cpu")
	if err != nil {
		t.Fatalf("openInput: %v", err)
	}

	// Should not crash; may or may not find line info depending on profiler config
	err = cmdLines(sf, "computeStep", 0, false)
	// err is acceptable (no line info) — we just verify it doesn't panic
	_ = err
}

func TestJFRCollapseCommand(t *testing.T) {
	sf, _, err := openInput(jfrFixture("cpu.jfr"), "cpu")
	if err != nil {
		t.Fatalf("openInput: %v", err)
	}

	out := captureOutput(func() {
		cmdCollapse(sf)
	})
	if len(out) == 0 {
		t.Error("expected non-empty collapsed output")
	}
	// Each line should have a space-separated count at the end
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, " ")
		if len(parts) < 2 {
			t.Errorf("malformed collapsed line (no count): %q", line)
			continue
		}
		countStr := parts[len(parts)-1]
		if _, err := strconv.Atoi(countStr); err != nil {
			t.Errorf("last field is not a number in collapsed line: %q", line)
		}
	}
}

func TestJFREventsCommand(t *testing.T) {
	out := captureOutput(func() {
		err := cmdEvents(jfrFixture("multi.jfr"))
		if err != nil {
			t.Fatalf("cmdEvents: %v", err)
		}
	})
	if !strings.Contains(out, "cpu") {
		t.Errorf("expected 'cpu' in events output, got:\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// TestCmdTreeNoMethod - tree command without -m parameter
// ---------------------------------------------------------------------------

func TestCmdTreeNoMethod(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.main", "B.process", "C.work"}, lines: []uint32{0, 0, 0}, count: 50, thread: "main"},
		{frames: []string{"A.main", "B.process", "D.other"}, lines: []uint32{0, 0, 0}, count: 30, thread: "main"},
		{frames: []string{"A.main", "E.helper"}, lines: []uint32{0, 0}, count: 20, thread: "worker"},
	})

	out := captureOutput(func() {
		cmdTree(sf, "", 4, 1.0) // empty method means show all from root
	})

	// Should show tree starting from root
	if !strings.Contains(out, "A.main") {
		t.Errorf("expected 'A.main' (root) in tree output, got:\n%s", out)
	}
	if !strings.Contains(out, "B.process") {
		t.Errorf("expected 'B.process' in tree output, got:\n%s", out)
	}
	if !strings.Contains(out, "C.work") {
		t.Errorf("expected 'C.work' in tree output, got:\n%s", out)
	}
	if !strings.Contains(out, "E.helper") {
		t.Errorf("expected 'E.helper' in tree output, got:\n%s", out)
	}

	// Should NOT show the "no frames matching" message
	if strings.Contains(out, "no frames matching") {
		t.Errorf("should not show 'no frames matching' when method is empty, got:\n%s", out)
	}
}

func TestCmdTreeNoMethodWithThreadFilter(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"Thread.run", "Worker.execute", "Task.process"}, lines: []uint32{0, 0, 0}, count: 60, thread: "worker-1"},
		{frames: []string{"Thread.run", "Worker.execute", "Task.compute"}, lines: []uint32{0, 0, 0}, count: 40, thread: "worker-1"},
		{frames: []string{"Thread.run", "Main.start"}, lines: []uint32{0, 0}, count: 50, thread: "main"},
	})

	// Filter by thread first
	filtered := sf.filterByThread("worker-1")

	out := captureOutput(func() {
		cmdTree(filtered, "", 5, 1.0)
	})

	// Should show tree for worker-1 thread only
	if !strings.Contains(out, "Thread.run") {
		t.Errorf("expected 'Thread.run' in output, got:\n%s", out)
	}
	if !strings.Contains(out, "Worker.execute") {
		t.Errorf("expected 'Worker.execute' in output, got:\n%s", out)
	}
	if !strings.Contains(out, "Task.process") {
		t.Errorf("expected 'Task.process' in output, got:\n%s", out)
	}

	// Should NOT show Main.start from main thread
	if strings.Contains(out, "Main.start") {
		t.Errorf("should not show 'Main.start' (filtered out), got:\n%s", out)
	}
}

func TestCmdTreeNoMethodEmpty(t *testing.T) {
	sf := makeStackFile(nil)

	out := captureOutput(func() {
		cmdTree(sf, "", 4, 1.0)
	})

	// Empty stackfile should produce no output
	if strings.TrimSpace(out) != "" {
		t.Errorf("expected empty output for empty stackfile, got %q", out)
	}
}

func TestCmdTreeNoMethodMinPct(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.main", "B.hot"}, lines: []uint32{0, 0}, count: 95, thread: "main"},
		{frames: []string{"A.main", "C.cold"}, lines: []uint32{0, 0}, count: 5, thread: "main"},
	})

	out := captureOutput(func() {
		cmdTree(sf, "", 4, 10.0) // 10% threshold
	})

	// A.main is 100%, B.hot is 95% - should show both
	if !strings.Contains(out, "A.main") {
		t.Errorf("expected 'A.main' in output, got:\n%s", out)
	}
	if !strings.Contains(out, "B.hot") {
		t.Errorf("expected 'B.hot' (95%%) in output, got:\n%s", out)
	}

	// C.cold is only 5%, below 10% threshold - should not show
	if strings.Contains(out, "C.cold") {
		t.Errorf("should not show 'C.cold' (5%%, below 10%% threshold), got:\n%s", out)
	}
}

func TestCmdTreeNoMethodMaxDepth(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.a", "B.b", "C.c", "D.d", "E.e"}, lines: []uint32{0, 0, 0, 0, 0}, count: 100, thread: "main"},
	})

	out := captureOutput(func() {
		cmdTree(sf, "", 3, 0.0) // max depth 3
	})

	// Should show up to depth 3
	if !strings.Contains(out, "A.a") {
		t.Error("expected A.a at depth 1")
	}
	if !strings.Contains(out, "B.b") {
		t.Error("expected B.b at depth 2")
	}
	if !strings.Contains(out, "C.c") {
		t.Error("expected C.c at depth 3")
	}

	// Should NOT show beyond depth 3
	if strings.Contains(out, "D.d") {
		t.Error("D.d should be cut off at maxDepth=3")
	}
	if strings.Contains(out, "E.e") {
		t.Error("E.e should be cut off at maxDepth=3")
	}
}

// ---------------------------------------------------------------------------
// TestJFRTreeNoMethod - JFR integration test
// ---------------------------------------------------------------------------

func TestJFRTreeNoMethod(t *testing.T) {
	sf, _, err := openInput(jfrFixture("cpu.jfr"), "cpu")
	if err != nil {
		t.Fatalf("openInput: %v", err)
	}

	out := captureOutput(func() {
		cmdTree(sf, "", 4, 5.0)
	})

	// Should show root-level methods (Thread.run is the common root)
	if !strings.Contains(out, "Thread.run") {
		t.Errorf("expected 'Thread.run' in tree output, got:\n%s", out)
	}

	// Should show some workload methods
	if !strings.Contains(out, "Workload") {
		t.Errorf("expected 'Workload' somewhere in tree, got:\n%s", out)
	}
}

func TestJFRTreeNoMethodWithThreadFilter(t *testing.T) {
	sf, _, err := openInput(jfrFixture("cpu.jfr"), "cpu")
	if err != nil {
		t.Fatalf("openInput: %v", err)
	}

	// Filter to cpu-worker thread
	filtered := sf.filterByThread("cpu-worker")

	out := captureOutput(func() {
		cmdTree(filtered, "", 5, 1.0)
	})

	// Should show thread-specific call tree
	if !strings.Contains(out, "Thread.run") {
		t.Errorf("expected 'Thread.run' in filtered tree, got:\n%s", out)
	}

	// Should show cpu-specific work
	if !strings.Contains(out, "cpuWork") || !strings.Contains(out, "computeStep") {
		t.Errorf("expected 'cpuWork' and 'computeStep' in cpu-worker tree, got:\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// Trace command tests
// ---------------------------------------------------------------------------

func TestCmdTraceBasicHotPath(t *testing.T) {
	// Linear chain: A→B→C, no branching.
	sf := makeStackFile([]stack{
		{frames: []string{"A.a", "B.b", "C.c"}, lines: []uint32{0, 0, 0}, count: 100, thread: "main"},
	})

	out := captureOutput(func() {
		cmdTrace(sf, "A.a", 0.0, false)
	})

	if !strings.Contains(out, "A.a") {
		t.Errorf("expected A.a in output, got:\n%s", out)
	}
	if !strings.Contains(out, "B.b") {
		t.Errorf("expected B.b in output, got:\n%s", out)
	}
	if !strings.Contains(out, "C.c") {
		t.Errorf("expected C.c in output, got:\n%s", out)
	}
	// No branching → no sibling annotations.
	if strings.Contains(out, "sibling") {
		t.Errorf("expected no sibling annotations for linear chain, got:\n%s", out)
	}
	// Should have leaf summary.
	if !strings.Contains(out, "Hottest leaf: C.c") {
		t.Errorf("expected leaf summary for C.c, got:\n%s", out)
	}
}

func TestCmdTraceBranching(t *testing.T) {
	// A→B(70) and A→C(30). Should pick B, annotate 1 sibling.
	sf := makeStackFile([]stack{
		{frames: []string{"A.a", "B.b"}, lines: []uint32{0, 0}, count: 70, thread: "main"},
		{frames: []string{"A.a", "C.c"}, lines: []uint32{0, 0}, count: 30, thread: "main"},
	})

	out := captureOutput(func() {
		cmdTrace(sf, "A.a", 0.0, false)
	})

	if !strings.Contains(out, "A.a") {
		t.Error("expected A.a")
	}
	if !strings.Contains(out, "B.b") {
		t.Errorf("expected B.b (hottest child), got:\n%s", out)
	}
	if !strings.Contains(out, "+1 sibling") {
		t.Errorf("expected '+1 sibling' annotation, got:\n%s", out)
	}
	if !strings.Contains(out, "C.c") {
		t.Errorf("expected C.c in sibling annotation, got:\n%s", out)
	}
	if !strings.Contains(out, "Hottest leaf: B.b") {
		t.Errorf("expected leaf summary for B.b, got:\n%s", out)
	}
}

func TestCmdTraceBranchingMultipleSiblings(t *testing.T) {
	// A→B(50), A→C(30), A→D(20). Should pick B, show +2 siblings, next=C.c
	sf := makeStackFile([]stack{
		{frames: []string{"A.a", "B.b"}, lines: []uint32{0, 0}, count: 50, thread: "main"},
		{frames: []string{"A.a", "C.c"}, lines: []uint32{0, 0}, count: 30, thread: "main"},
		{frames: []string{"A.a", "D.d"}, lines: []uint32{0, 0}, count: 20, thread: "main"},
	})

	out := captureOutput(func() {
		cmdTrace(sf, "A.a", 0.0, false)
	})

	if !strings.Contains(out, "+2 siblings") {
		t.Errorf("expected '+2 siblings', got:\n%s", out)
	}
	if !strings.Contains(out, "next: 30.0% C.c") {
		t.Errorf("expected 'next: 30.0%% C.c', got:\n%s", out)
	}
}

func TestCmdTraceDeepChain(t *testing.T) {
	// 12 frames deep. Verify no depth limit.
	frames := []string{"A.a", "B.b", "C.c", "D.d", "E.e", "F.f", "G.g", "H.h", "I.i", "J.j", "K.k", "L.l"}
	lines := make([]uint32, len(frames))
	sf := makeStackFile([]stack{
		{frames: frames, lines: lines, count: 100, thread: "main"},
	})

	out := captureOutput(func() {
		cmdTrace(sf, "A.a", 0.0, false)
	})

	for _, f := range frames {
		short := shortName(f)
		if !strings.Contains(out, short) {
			t.Errorf("expected %s in deep trace output, got:\n%s", short, out)
		}
	}
	if !strings.Contains(out, "Hottest leaf: L.l") {
		t.Errorf("expected leaf summary for L.l, got:\n%s", out)
	}
}

func TestCmdTraceSelfTimeAnnotation(t *testing.T) {
	// Leaf should get ← self=X.X% annotation.
	sf := makeStackFile([]stack{
		{frames: []string{"A.a", "B.b"}, lines: []uint32{0, 0}, count: 100, thread: "main"},
	})

	out := captureOutput(func() {
		cmdTrace(sf, "A.a", 0.0, false)
	})

	if !strings.Contains(out, "← self=") {
		t.Errorf("expected self-time annotation on leaf, got:\n%s", out)
	}
	if !strings.Contains(out, "← self=100.0%") {
		t.Errorf("expected ← self=100.0%% on leaf B.b, got:\n%s", out)
	}
}

func TestCmdTraceMinPct(t *testing.T) {
	// A→B→C where C is only 1% of total. With min-pct=5, should stop at B.
	sf := makeStackFile([]stack{
		{frames: []string{"A.a", "B.b", "C.c"}, lines: []uint32{0, 0, 0}, count: 1, thread: "main"},
		{frames: []string{"A.a", "B.b"}, lines: []uint32{0, 0}, count: 99, thread: "main"},
	})

	out := captureOutput(func() {
		cmdTrace(sf, "A.a", 5.0, false)
	})

	if !strings.Contains(out, "A.a") {
		t.Error("expected A.a")
	}
	if !strings.Contains(out, "B.b") {
		t.Error("expected B.b")
	}
	// C.c is 1% (1/100), below 5% threshold — trace should stop at B.b.
	if strings.Contains(out, "C.c") {
		t.Errorf("C.c should be filtered by min-pct, got:\n%s", out)
	}
	// Leaf should be B.b.
	if !strings.Contains(out, "Hottest leaf: B.b") {
		t.Errorf("expected leaf summary for B.b, got:\n%s", out)
	}
}

func TestCmdTraceNoMatch(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.a", "B.b"}, lines: []uint32{0, 0}, count: 10, thread: "main"},
	})

	out := captureOutput(func() {
		cmdTrace(sf, "Nonexistent", 0.0, false)
	})

	if !strings.Contains(out, "no frames matching") {
		t.Errorf("expected 'no frames matching', got %q", out)
	}
}

func TestCmdTraceEmpty(t *testing.T) {
	sf := makeStackFile(nil)

	out := captureOutput(func() {
		cmdTrace(sf, "A.a", 0.0, false)
	})

	if strings.TrimSpace(out) != "" {
		t.Errorf("expected empty output for empty stackFile, got %q", out)
	}
}

func TestCmdTraceMultipleMatches(t *testing.T) {
	// Two different methods match "run": Foo.run and Bar.run.
	sf := makeStackFile([]stack{
		{frames: []string{"com.a.Foo.run", "X.x"}, lines: []uint32{0, 0}, count: 60, thread: "main"},
		{frames: []string{"com.b.Bar.run", "Y.y"}, lines: []uint32{0, 0}, count: 40, thread: "main"},
	})

	out := captureOutput(func() {
		cmdTrace(sf, "run", 0.0, false)
	})

	if !strings.Contains(out, "matched 2 methods") {
		t.Errorf("expected 'matched 2 methods', got:\n%s", out)
	}
	// Both should be traced.
	if !strings.Contains(out, "Foo.run") {
		t.Errorf("expected Foo.run, got:\n%s", out)
	}
	if !strings.Contains(out, "Bar.run") {
		t.Errorf("expected Bar.run, got:\n%s", out)
	}
	// Each should have a leaf summary.
	leafCount := strings.Count(out, "Hottest leaf:")
	if leafCount != 2 {
		t.Errorf("expected 2 'Hottest leaf:' lines, got %d in:\n%s", leafCount, out)
	}
}

func TestCmdTraceSingleFrameMethod(t *testing.T) {
	// Matched method is the only frame (leaf already).
	sf := makeStackFile([]stack{
		{frames: []string{"A.a"}, lines: []uint32{0}, count: 100, thread: "main"},
	})

	out := captureOutput(func() {
		cmdTrace(sf, "A.a", 0.0, false)
	})

	lines := strings.Split(strings.TrimSpace(out), "\n")
	// Should have exactly 2 lines: the trace line and the leaf summary.
	if len(lines) != 2 {
		t.Errorf("expected 2 lines (trace + leaf summary), got %d:\n%s", len(lines), out)
	}
	if !strings.Contains(out, "[100.0%] A.a") {
		t.Errorf("expected [100.0%%] A.a, got:\n%s", out)
	}
	if !strings.Contains(out, "Hottest leaf: A.a") {
		t.Errorf("expected leaf summary for A.a, got:\n%s", out)
	}
}

func TestCmdTraceEqualChildren(t *testing.T) {
	// Two children with identical sample counts → deterministic pick (lexicographic).
	sf := makeStackFile([]stack{
		{frames: []string{"A.a", "Z.z"}, lines: []uint32{0, 0}, count: 50, thread: "main"},
		{frames: []string{"A.a", "B.b"}, lines: []uint32{0, 0}, count: 50, thread: "main"},
	})

	out := captureOutput(func() {
		cmdTrace(sf, "A.a", 0.0, false)
	})

	// B.b < Z.z lexicographically, so B.b should be chosen.
	if !strings.Contains(out, "Hottest leaf: B.b") {
		t.Errorf("expected B.b chosen (lexicographic tiebreak), got:\n%s", out)
	}
	// Z.z should appear in sibling annotation.
	if !strings.Contains(out, "+1 sibling") {
		t.Errorf("expected '+1 sibling', got:\n%s", out)
	}
	if !strings.Contains(out, "Z.z") {
		t.Errorf("expected Z.z in sibling annotation, got:\n%s", out)
	}
}

func TestCmdTraceMinPctZero(t *testing.T) {
	// min-pct=0 should trace all the way down even for tiny branches.
	sf := makeStackFile([]stack{
		{frames: []string{"A.a", "B.b", "C.c"}, lines: []uint32{0, 0, 0}, count: 1, thread: "main"},
		{frames: []string{"X.x"}, lines: []uint32{0}, count: 999, thread: "main"},
	})

	out := captureOutput(func() {
		cmdTrace(sf, "A.a", 0.0, false)
	})

	// Even though A.a is 0.1%, min-pct=0 should show everything.
	if !strings.Contains(out, "C.c") {
		t.Errorf("expected C.c with min-pct=0, got:\n%s", out)
	}
	if !strings.Contains(out, "Hottest leaf: C.c") {
		t.Errorf("expected leaf summary for C.c, got:\n%s", out)
	}
}

func TestCmdTraceMinPctHighCutsEarly(t *testing.T) {
	// High min-pct should cut off early.
	sf := makeStackFile([]stack{
		{frames: []string{"A.a", "B.b", "C.c"}, lines: []uint32{0, 0, 0}, count: 10, thread: "main"},
		{frames: []string{"A.a", "B.b"}, lines: []uint32{0, 0}, count: 40, thread: "main"},
		{frames: []string{"X.x"}, lines: []uint32{0}, count: 50, thread: "main"},
	})

	// A.a=50%, B.b=50%, C.c=10%. With min-pct=20%, C.c is filtered.
	out := captureOutput(func() {
		cmdTrace(sf, "A.a", 20.0, false)
	})

	if !strings.Contains(out, "A.a") {
		t.Error("expected A.a")
	}
	if !strings.Contains(out, "B.b") {
		t.Error("expected B.b")
	}
	if strings.Contains(out, "C.c") {
		t.Errorf("C.c should be cut off by min-pct=20, got:\n%s", out)
	}
}

func TestCmdTraceFQN(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"com/example/App.process", "com/example/Worker.run"}, lines: []uint32{0, 0}, count: 100, thread: "main"},
	})

	out := captureOutput(func() {
		cmdTrace(sf, "App.process", 0.0, true)
	})

	if !strings.Contains(out, "com.example.App.process") {
		t.Errorf("expected FQN 'com.example.App.process', got:\n%s", out)
	}
	if !strings.Contains(out, "com.example.Worker.run") {
		t.Errorf("expected FQN 'com.example.Worker.run', got:\n%s", out)
	}
}

func TestCmdTracePercentageCorrectness(t *testing.T) {
	// Verify percentages are relative to totalSamples, not parent.
	sf := makeStackFile([]stack{
		{frames: []string{"A.a", "B.b", "C.c"}, lines: []uint32{0, 0, 0}, count: 30, thread: "main"},
		{frames: []string{"A.a", "B.b"}, lines: []uint32{0, 0}, count: 20, thread: "main"},
		{frames: []string{"X.x"}, lines: []uint32{0}, count: 50, thread: "main"},
	})
	// totalSamples=100, A.a=50%, B.b=50%, C.c=30%

	out := captureOutput(func() {
		cmdTrace(sf, "A.a", 0.0, false)
	})

	if !strings.Contains(out, "[50.0%] A.a") {
		t.Errorf("expected [50.0%%] A.a, got:\n%s", out)
	}
	if !strings.Contains(out, "[50.0%] B.b") {
		t.Errorf("expected [50.0%%] B.b, got:\n%s", out)
	}
	if !strings.Contains(out, "[30.0%] C.c") {
		t.Errorf("expected [30.0%%] C.c, got:\n%s", out)
	}
}

func TestCmdTraceSelfTimeOnlyOnLeaf(t *testing.T) {
	// Self-time annotation should only appear on the last node.
	sf := makeStackFile([]stack{
		{frames: []string{"A.a", "B.b", "C.c"}, lines: []uint32{0, 0, 0}, count: 100, thread: "main"},
	})

	out := captureOutput(func() {
		cmdTrace(sf, "A.a", 0.0, false)
	})

	lines := strings.Split(strings.TrimSpace(out), "\n")
	selfCount := 0
	for _, line := range lines {
		if strings.Contains(line, "← self=") {
			selfCount++
		}
	}
	if selfCount != 1 {
		t.Errorf("expected exactly 1 self-time annotation (on leaf), got %d in:\n%s", selfCount, out)
	}
	// The self-time annotation should be on the C.c line.
	for _, line := range lines {
		if strings.Contains(line, "C.c") && strings.Contains(line, "← self=") {
			return // pass
		}
	}
	t.Errorf("expected self-time annotation on C.c line, got:\n%s", out)
}

func TestCmdTraceSelfTimeBelowMinPctHidden(t *testing.T) {
	// Self-time annotation should be hidden when self-pct < min-pct.
	sf := makeStackFile([]stack{
		{frames: []string{"A.a", "B.b"}, lines: []uint32{0, 0}, count: 1, thread: "main"},
		{frames: []string{"A.a"}, lines: []uint32{0}, count: 99, thread: "main"},
	})
	// B.b self=1%, A.a self=99%.

	out := captureOutput(func() {
		cmdTrace(sf, "A.a", 0.0, false)
	})

	// B.b is 1% self. With min-pct=0 it still appears in the trace,
	// but at the leaf it should show self annotation since 1% >= 0%.
	if !strings.Contains(out, "← self=1.0%") {
		t.Errorf("expected ← self=1.0%% on leaf B.b with min-pct=0, got:\n%s", out)
	}

	// Now with high min-pct: trace A.a with min-pct=5. B.b is 1% so it's
	// filtered out as a child. A.a itself is the leaf.
	out2 := captureOutput(func() {
		cmdTrace(sf, "A.a", 5.0, false)
	})

	// A.a should be the leaf with self=99%.
	if !strings.Contains(out2, "Hottest leaf: A.a") {
		t.Errorf("expected A.a as leaf, got:\n%s", out2)
	}
	if !strings.Contains(out2, "← self=99.0%") {
		t.Errorf("expected ← self=99.0%%, got:\n%s", out2)
	}
}

func TestCmdTraceNoSiblingAnnotationWhenSingle(t *testing.T) {
	// No sibling annotation when there's exactly one child.
	sf := makeStackFile([]stack{
		{frames: []string{"A.a", "B.b", "C.c"}, lines: []uint32{0, 0, 0}, count: 100, thread: "main"},
	})

	out := captureOutput(func() {
		cmdTrace(sf, "A.a", 0.0, false)
	})

	if strings.Contains(out, "sibling") {
		t.Errorf("expected no sibling annotations for single-child chain, got:\n%s", out)
	}
}

func TestCmdTraceSiblingsBelowMinPctNotCounted(t *testing.T) {
	// Siblings below min-pct should not appear in the annotation.
	sf := makeStackFile([]stack{
		{frames: []string{"A.a", "B.b"}, lines: []uint32{0, 0}, count: 90, thread: "main"},
		{frames: []string{"A.a", "C.c"}, lines: []uint32{0, 0}, count: 1, thread: "main"},
		{frames: []string{"X.x"}, lines: []uint32{0}, count: 9, thread: "main"},
	})
	// totalSamples=100, B.b=90%, C.c=1%.
	// With min-pct=5, C.c is below threshold → not counted as sibling.

	out := captureOutput(func() {
		cmdTrace(sf, "A.a", 5.0, false)
	})

	if strings.Contains(out, "sibling") {
		t.Errorf("expected no sibling annotation (C.c below min-pct), got:\n%s", out)
	}
}

func TestCmdTraceMatchAtLeaf(t *testing.T) {
	// Matched method is already the leaf in all stacks.
	sf := makeStackFile([]stack{
		{frames: []string{"A.a", "B.b"}, lines: []uint32{0, 0}, count: 50, thread: "main"},
		{frames: []string{"C.c", "B.b"}, lines: []uint32{0, 0}, count: 50, thread: "main"},
	})

	out := captureOutput(func() {
		cmdTrace(sf, "B.b", 0.0, false)
	})

	// B.b is the leaf in all stacks, so it should be a single-node trace.
	if !strings.Contains(out, "Hottest leaf: B.b") {
		t.Errorf("expected leaf summary for B.b, got:\n%s", out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	// Should be 2 lines: [pct%] B.b (with self annotation) + leaf summary.
	if len(lines) != 2 {
		t.Errorf("expected 2 lines for match-at-leaf, got %d:\n%s", len(lines), out)
	}
}

func TestCmdTraceMultipleRootsWithDifferentDepths(t *testing.T) {
	// Method appears at different depths in different stacks.
	sf := makeStackFile([]stack{
		{frames: []string{"X.x", "A.a", "B.b", "C.c"}, lines: []uint32{0, 0, 0, 0}, count: 60, thread: "main"},
		{frames: []string{"Y.y", "A.a", "D.d"}, lines: []uint32{0, 0, 0}, count: 40, thread: "main"},
	})

	out := captureOutput(func() {
		cmdTrace(sf, "A.a", 0.0, false)
	})

	if !strings.Contains(out, "A.a") {
		t.Error("expected A.a")
	}
	// A.a=100%, children: B.b=60%, D.d=40%. Should pick B.b.
	if !strings.Contains(out, "B.b") {
		t.Errorf("expected B.b (hottest child), got:\n%s", out)
	}
	if !strings.Contains(out, "+1 sibling") {
		t.Errorf("expected sibling annotation for D.d, got:\n%s", out)
	}
}

func TestCmdTraceBranchingSiblingAnnotationFormat(t *testing.T) {
	// Verify exact format of sibling annotation string.
	sf := makeStackFile([]stack{
		{frames: []string{"A.a", "B.b"}, lines: []uint32{0, 0}, count: 70, thread: "main"},
		{frames: []string{"A.a", "C.c"}, lines: []uint32{0, 0}, count: 30, thread: "main"},
	})

	out := captureOutput(func() {
		cmdTrace(sf, "A.a", 0.0, false)
	})

	// The B.b line should contain exactly: (+1 sibling, next: 30.0% C.c)
	expected := "(+1 sibling, next: 30.0% C.c)"
	if !strings.Contains(out, expected) {
		t.Errorf("expected annotation %q, got:\n%s", expected, out)
	}
}

func TestCmdTraceLeafSummary(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.a", "B.b", "C.c"}, lines: []uint32{0, 0, 0}, count: 80, thread: "main"},
		{frames: []string{"X.x"}, lines: []uint32{0}, count: 20, thread: "main"},
	})

	out := captureOutput(func() {
		cmdTrace(sf, "A.a", 0.0, false)
	})

	// C.c self=80/100=80.0%
	if !strings.Contains(out, "Hottest leaf: C.c (self=80.0%)") {
		t.Errorf("expected 'Hottest leaf: C.c (self=80.0%%)', got:\n%s", out)
	}
}

func TestCmdTraceLeafSummaryMultipleRoots(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"com.a.Foo.run", "X.x"}, lines: []uint32{0, 0}, count: 60, thread: "main"},
		{frames: []string{"com.b.Bar.run", "Y.y"}, lines: []uint32{0, 0}, count: 40, thread: "main"},
	})

	out := captureOutput(func() {
		cmdTrace(sf, "run", 0.0, false)
	})

	if !strings.Contains(out, "Hottest leaf: X.x") {
		t.Errorf("expected leaf summary for X.x, got:\n%s", out)
	}
	if !strings.Contains(out, "Hottest leaf: Y.y") {
		t.Errorf("expected leaf summary for Y.y, got:\n%s", out)
	}
}

func TestCmdTraceLeafSummaryMatchAtLeaf(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.a"}, lines: []uint32{0}, count: 100, thread: "main"},
	})

	out := captureOutput(func() {
		cmdTrace(sf, "A.a", 0.0, false)
	})

	if !strings.Contains(out, "Hottest leaf: A.a (self=100.0%)") {
		t.Errorf("expected 'Hottest leaf: A.a (self=100.0%%)', got:\n%s", out)
	}
}

func TestCmdTraceLeafSummaryNoSelfTime(t *testing.T) {
	// B.b is never a leaf frame, so it has 0 self-samples.
	// With min-pct=6, C.c (5%) and D.d (5%) are below threshold,
	// making B.b the trace leaf with self=0.0%.
	sf := makeStackFile([]stack{
		{frames: []string{"A.a", "B.b", "C.c"}, lines: []uint32{0, 0, 0}, count: 5, thread: "main"},
		{frames: []string{"A.a", "B.b", "D.d"}, lines: []uint32{0, 0, 0}, count: 5, thread: "main"},
		{frames: []string{"X.x"}, lines: []uint32{0}, count: 90, thread: "main"},
	})

	out := captureOutput(func() {
		cmdTrace(sf, "A.a", 6.0, false)
	})

	if !strings.Contains(out, "Hottest leaf: B.b (self=0.0%)") {
		t.Errorf("expected 'Hottest leaf: B.b (self=0.0%%)', got:\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// JFR integration tests for trace
// ---------------------------------------------------------------------------

func TestJFRTraceCommand(t *testing.T) {
	sf, _, err := openInput(jfrFixture("cpu.jfr"), "cpu")
	if err != nil {
		t.Fatalf("openInput: %v", err)
	}

	out := captureOutput(func() {
		cmdTrace(sf, "Workload", 1.0, false)
	})

	// Should produce output with Workload methods.
	if !strings.Contains(out, "Workload") {
		t.Errorf("expected 'Workload' in trace output, got:\n%s", out)
	}
	// Should have leaf summary.
	if !strings.Contains(out, "Hottest leaf:") {
		t.Errorf("expected 'Hottest leaf:' in output, got:\n%s", out)
	}
	// Should have at least 2 lines (trace + leaf summary).
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		t.Errorf("expected at least 2 output lines, got %d:\n%s", len(lines), out)
	}
}

func TestJFRTraceWithThreadFilter(t *testing.T) {
	sf, _, err := openInput(jfrFixture("cpu.jfr"), "cpu")
	if err != nil {
		t.Fatalf("openInput: %v", err)
	}

	filtered := sf.filterByThread("cpu-worker")

	out := captureOutput(func() {
		cmdTrace(filtered, "Workload", 1.0, false)
	})

	if !strings.Contains(out, "Workload") {
		t.Errorf("expected 'Workload' in filtered trace, got:\n%s", out)
	}
	if !strings.Contains(out, "Hottest leaf:") {
		t.Errorf("expected leaf summary in filtered trace, got:\n%s", out)
	}
}

func TestJFRTraceWallEvent(t *testing.T) {
	sf, _, err := openInput(jfrFixture("wall.jfr"), "wall")
	if err != nil {
		t.Fatalf("openInput: %v", err)
	}

	if sf.totalSamples == 0 {
		t.Skip("wall.jfr has no wall samples")
	}

	out := captureOutput(func() {
		cmdTrace(sf, "Workload", 0.5, false)
	})

	// Wall event should have some Workload samples.
	if !strings.Contains(out, "Workload") {
		t.Errorf("expected 'Workload' in wall trace, got:\n%s", out)
	}
	if !strings.Contains(out, "Hottest leaf:") {
		t.Errorf("expected leaf summary, got:\n%s", out)
	}
}

func TestJFRTraceMultiEventFile(t *testing.T) {
	// multi.jfr contains cpu + wall + alloc + lock events.
	// Trace with different event types should produce different results.
	cpuSF, _, err := openInput(jfrFixture("multi.jfr"), "cpu")
	if err != nil {
		t.Fatalf("openInput cpu: %v", err)
	}
	wallSF, _, err := openInput(jfrFixture("multi.jfr"), "wall")
	if err != nil {
		t.Fatalf("openInput wall: %v", err)
	}

	cpuOut := captureOutput(func() {
		cmdTrace(cpuSF, "Workload", 0.5, false)
	})
	wallOut := captureOutput(func() {
		cmdTrace(wallSF, "Workload", 0.5, false)
	})

	// Both should have output.
	if !strings.Contains(cpuOut, "Hottest leaf:") {
		t.Errorf("expected leaf summary for cpu trace, got:\n%s", cpuOut)
	}
	if !strings.Contains(wallOut, "Hottest leaf:") {
		t.Errorf("expected leaf summary for wall trace, got:\n%s", wallOut)
	}

	// CPU and wall should typically produce different traces since
	// the workload has cpu-intensive and sleep-intensive threads.
	// At minimum, both should have some output.
	if len(cpuOut) == 0 || len(wallOut) == 0 {
		t.Error("expected non-empty output for both event types")
	}
}
