package main

import (
	"bytes"
	"io"
	"os"
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

	// Self counts: C.c=10, B.b=5, D.d=3
	if ranked[0].name != "C.c" || ranked[0].selfCount != 10 {
		t.Errorf("expected C.c with self=10 at top, got %s with self=%d", ranked[0].name, ranked[0].selfCount)
	}
	if ranked[1].name != "B.b" || ranked[1].selfCount != 5 {
		t.Errorf("expected B.b with self=5 at #2, got %s with self=%d", ranked[1].name, ranked[1].selfCount)
	}

	// Total counts: A.a=18 (all), B.b=15 (10+5), C.c=10, D.d=3
	for _, e := range ranked {
		if e.name == "A.a" && e.totalCount != 18 {
			t.Errorf("A.a total=%d, want 18", e.totalCount)
		}
		if e.name == "B.b" && e.totalCount != 15 {
			t.Errorf("B.b total=%d, want 15", e.totalCount)
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
		cmdDiff(before, after, 0.5, false)
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
		cmdDiff(sf, sf, 0.5, false)
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
		cmdLines(sf, "B.process", false)
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
		cmdLines(sf, "Nonexistent", false)
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

	// cmdInfo calls discoverEvents which needs a real file, but we can test
	// the threads and hot methods sections by passing a non-existent path
	// (discoverEvents will fail silently due to the err check).
	out := captureOutput(func() {
		cmdInfo("/nonexistent.jfr", sf, "cpu", true)
	})

	if !strings.Contains(out, "=== THREADS (top 5) ===") {
		t.Error("expected THREADS section")
	}
	if !strings.Contains(out, "main") {
		t.Error("expected 'main' thread")
	}
	if !strings.Contains(out, "=== HOT METHODS (top 10) ===") {
		t.Error("expected HOT METHODS section")
	}
	if !strings.Contains(out, "Total samples: 15") {
		t.Errorf("expected 'Total samples: 15', got %q", out)
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
// TestFilterByThread
// ---------------------------------------------------------------------------

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
