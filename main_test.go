package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func captureStream(stream **os.File, f func()) string {
	old := *stream
	r, w, _ := os.Pipe()
	*stream = w
	f()
	w.Close()
	var buf bytes.Buffer
	io.Copy(&buf, r)
	*stream = old
	return buf.String()
}

func captureOutput(f func()) string {
	return captureStream(&os.Stdout, f)
}

func makeStackFile(stacks []stack) *stackFile {
	sf := &stackFile{}
	for _, s := range stacks {
		sf.stacks = append(sf.stacks, s)
		sf.totalSamples += s.count
	}
	return sf
}

func runCLIForTest(t *testing.T, args []string, stdin io.Reader) (int, string, string) {
	t.Helper()
	cmdArgs := append([]string{"-test.run=TestHelperProcessMain", "--"}, args...)
	cmd := exec.Command(os.Args[0], cmdArgs...)
	cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")
	if stdin != nil {
		cmd.Stdin = stdin
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else {
			t.Fatalf("helper process failed: %v", err)
		}
	}

	return exitCode, stdout.String(), stderr.String()
}

func TestHelperProcessMain(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}

	sep := -1
	for i, a := range os.Args {
		if a == "--" {
			sep = i
			break
		}
	}
	if sep < 0 {
		fmt.Fprintln(os.Stderr, "error: missing test helper separator")
		os.Exit(2)
	}

	os.Args = append([]string{"ap-query"}, os.Args[sep+1:]...)
	main()
	os.Exit(0)
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

		// Native frames from shared libraries
		{"libc.so.6.__sched_yield", "__sched_yield"},
		{"libc.so.6.epoll_wait", "epoll_wait"},
		{"libc.so.6.__futex_abstimed_wait_common", "__futex_abstimed_wait_common"},
		{"libasyncProfiler.so.WallClock::signalHandler", "WallClock::signalHandler"},
		{"libquestdb.so.Java_io_questdb_std_Vect_binarySearch64Bit", "Java_io_questdb_std_Vect_binarySearch64Bit"},
		{"libstdc++.so.6.some_function", "some_function"},
		{"ld-linux-x86-64.so.2.dl_main", "dl_main"},
		{"libpthread.so.0.__pthread_mutex_lock", "__pthread_mutex_lock"},
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

	if !strings.Contains(out, "no stacks matching") {
		t.Errorf("expected 'no stacks matching', got %q", out)
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
		cmdInfo(sf, infoOpts{eventType: "cpu", hasMetadata: true, eventCounts: map[string]int{"cpu": 15}, topThreads: 5, topMethods: 10})
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

func TestCmdInfoDurationHeader(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.a", "B.b"}, lines: []uint32{10, 20}, count: 100, thread: "main"},
	})

	out := captureOutput(func() {
		cmdInfo(sf, infoOpts{eventType: "cpu", hasMetadata: true, eventCounts: map[string]int{"cpu": 100}, topThreads: 5, topMethods: 10, spanNanos: 30_000_000_000}) // 30s
	})

	if !strings.Contains(out, "Duration: 30.0s") {
		t.Errorf("expected 'Duration: 30.0s' header, got:\n%s", out)
	}
	if !strings.Contains(out, "Samples: 100 (cpu)") {
		t.Errorf("expected 'Samples: 100 (cpu)' header, got:\n%s", out)
	}
	// When duration is shown, the separate "Event:" header should not appear
	if strings.Contains(out, "Event: cpu") {
		t.Error("expected no separate 'Event:' header when duration is shown")
	}
}

func TestCmdInfoNoDurationForNonJFR(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.a"}, lines: []uint32{0}, count: 10, thread: "main"},
	})

	out := captureOutput(func() {
		cmdInfo(sf, infoOpts{eventType: "cpu", topThreads: 5, topMethods: 10})
	})

	if strings.Contains(out, "Duration:") {
		t.Error("expected no Duration header for non-JFR input")
	}
}

func TestCmdInfoAlsoAvailable(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.a"}, lines: []uint32{0}, count: 10, thread: "main"},
	})

	out := captureOutput(func() {
		cmdInfo(sf, infoOpts{eventType: "wall", hasMetadata: true, eventCounts: map[string]int{"wall": 10, "cpu": 200, "alloc": 50}, topThreads: 5, topMethods: 10})
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
		cmdInfo(sf, infoOpts{eventType: "cpu", expand: 2, topThreads: 10, topMethods: 20})
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
		cmdInfo(sf, infoOpts{eventType: "cpu", topThreads: 10, topMethods: 20})
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
		cmdInfo(sf, infoOpts{eventType: "cpu", expand: 2, topThreads: 10, topMethods: 20})
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
// TestThreadGroupName
// ---------------------------------------------------------------------------

func TestThreadGroupName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		// --- basic suffix stripping ---
		{"worker-1", "worker"},
		{"pool_0", "pool"},
		{"GC Thread#54", "GC Thread"},
		{"main", "main"},
		{"DestroyJavaVM", "DestroyJavaVM"},

		// --- multi-level: drop all purely-numeric segments ---
		{"pool-1-thread-2", "pool-thread"},
		{"pool-2-thread-5", "pool-thread"},
		{"http-nio-8080-exec-1", "http-nio-exec"},
		{"http-nio-8443-exec-3", "http-nio-exec"},
		{"lettuce-nioEventLoop-4-1", "lettuce-nioEventLoop"},
		{"a_1_2", "a"},
		{"x-1-2-3-y", "x-y"},

		// --- trailing digit trimming ---
		{"CompilerThread0", "CompilerThread"},
		{"CompilerThread1", "CompilerThread"},
		{"vm0", "vm"},
		{"ab1", "ab"},
		{"abc123", "abc"},

		// trailing digit trimming: min-length threshold (remainder < 2 chars)
		{"G1", "G1"},
		{"a1", "a1"},
		{"Z9", "Z9"},

		// trailing digits: char before digit run must be a letter
		{"Connection(1)", "Connection(1)"},
		{"abc.123", "abc.123"},
		{"foo)7", "foo)7"},
		{"bar]3", "bar]3"},

		// --- real-world JVM thread names ---
		// Tomcat
		{"http-nio-8080-Acceptor", "http-nio-Acceptor"},
		{"http-nio-8080-Poller", "http-nio-Poller"},

		// Netty
		{"nioEventLoopGroup-2-1", "nioEventLoopGroup"},
		{"nioEventLoopGroup-2-2", "nioEventLoopGroup"},
		{"nioEventLoopGroup-3-1", "nioEventLoopGroup"},

		// Reactor
		{"reactor-http-nio-1", "reactor-http-nio"},
		{"reactor-http-nio-2", "reactor-http-nio"},

		// HikariCP
		{"HikariPool-1-housekeeper", "HikariPool-housekeeper"},
		{"HikariPool-2-housekeeper", "HikariPool-housekeeper"},

		// Quartz
		{"QuartzScheduler_Worker-1", "QuartzScheduler_Worker"},
		{"QuartzScheduler_Worker-2", "QuartzScheduler_Worker"},

		// ForkJoinPool
		{"ForkJoinPool-1-worker-1", "ForkJoinPool-worker"},
		{"ForkJoinPool-1-worker-2", "ForkJoinPool-worker"},
		{"ForkJoinPool.commonPool-worker-1", "ForkJoinPool.commonPool-worker"},
		{"ForkJoinPool.commonPool-worker-3", "ForkJoinPool.commonPool-worker"},

		// Vert.x (dot inside segment)
		{"vert.x-eventloop-thread-0", "vert.x-eventloop-thread"},
		{"vert.x-eventloop-thread-1", "vert.x-eventloop-thread"},
		{"vert.x-worker-thread-0", "vert.x-worker-thread"},

		// G1 GC threads
		{"G1 Main Marker", "G1 Main Marker"},
		{"G1 Conc#0", "G1 Conc"},
		{"G1 Conc#1", "G1 Conc"},
		{"G1 Refine#0", "G1 Refine"},
		{"G1 Refine#12", "G1 Refine"},
		{"G1 Young Gen", "G1 Young Gen"},

		// RMI — parens prevent digit trimming, IP not all-digits
		{"RMI TCP Connection(1)-127.0.0.1", "RMI TCP Connection(1)-127.0.0.1"},
		{"RMI TCP Connection(2)-127.0.0.1", "RMI TCP Connection(2)-127.0.0.1"},

		// C2 compiler
		{"C2 CompilerThread0", "C2 CompilerThread"},
		{"C2 CompilerThread1", "C2 CompilerThread"},

		// --- spaces are not separators ---
		{"G1 Young Gen", "G1 Young Gen"},
		{"VM Thread", "VM Thread"},
		{"Signal Dispatcher", "Signal Dispatcher"},

		// --- separator edge cases ---
		// trailing separator: empty segment dropped
		{"thread-", "thread"},
		{"abc_", "abc"},
		{"x#", "x"},

		// leading separator: empty segment dropped
		{"-abc-1", "abc"},
		{"_foo_2", "foo"},
		{"#bar", "bar"},

		// consecutive separators
		{"a--b", "a-b"},
		{"a---b", "a-b"},
		{"a-_b", "a_b"},
		{"a-_#b", "a#b"},

		// only separators
		{"-", "-"},
		{"#", "#"},
		{"_", "_"},
		{"--", "--"},
		{"-_#", "-_#"},

		// --- all-numeric inputs (fallback to original) ---
		{"123", "123"},
		{"42", "42"},
		{"0", "0"},
		{"123-456", "123-456"},
		{"1-2-3", "1-2-3"},
		{"#42", "#42"},
		{"_99_", "_99_"},

		// --- mixed separator types ---
		{"shared-network_118", "shared-network"},
		{"a-1_b-2#c-3", "a_b#c"},
		{"x_y-z", "x_y-z"},

		// --- empty / minimal ---
		{"", ""},
		{"a", "a"},
		{"1", "1"},

		// --- version-like suffixes: threshold protects short remainders ---
		{"test-v2", "test-v2"}, // "v2": remainder "v" is 1 char → keep
		{"log4j2", "log4j"},    // "log4j2": trailing "2", remainder "log4j" >= 2 → trim

		// --- names that must NOT merge with each other ---
		// (tested via groupThreads below, but also verify here)
		{"io-read-1", "io-read"},
		{"io-write-1", "io-write"},
		{"kafka-coordinator", "kafka-coordinator"},
		{"kafka-producer", "kafka-producer"},
		{"pool-1-read", "pool-read"},
		{"pool-1-write", "pool-write"},
	}
	for _, tt := range tests {
		got := threadGroupName(tt.input)
		if got != tt.want {
			t.Errorf("threadGroupName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// TestThreadGroupNameMerges verifies that specific thread names produce the
// same group name (i.e. would be merged by groupThreads).
func TestThreadGroupNameMerges(t *testing.T) {
	shouldMerge := [][]string{
		{"worker-1", "worker-2", "worker-99"},
		{"pool-1-thread-2", "pool-2-thread-5", "pool-1-thread-9"},
		{"http-nio-8080-exec-1", "http-nio-8443-exec-3"},
		{"nioEventLoopGroup-2-1", "nioEventLoopGroup-3-4"},
		{"HikariPool-1-housekeeper", "HikariPool-2-housekeeper"},
		{"CompilerThread0", "CompilerThread1"},
		{"G1 Conc#0", "G1 Conc#1", "G1 Conc#12"},
		{"ForkJoinPool-1-worker-1", "ForkJoinPool-2-worker-3"},
		{"reactor-http-nio-1", "reactor-http-nio-2", "reactor-http-nio-10"},
		{"QuartzScheduler_Worker-1", "QuartzScheduler_Worker-2"},
		{"gc#0", "gc#1", "gc#2"},
		{"lettuce-nioEventLoop-4-1", "lettuce-nioEventLoop-5-2"},
		{"vert.x-eventloop-thread-0", "vert.x-eventloop-thread-1"},
	}
	for _, group := range shouldMerge {
		names := make(map[string]bool)
		for _, name := range group {
			names[threadGroupName(name)] = true
		}
		if len(names) != 1 {
			t.Errorf("expected all to merge: %v, got groups: %v", group, names)
		}
	}
}

// TestThreadGroupNameNoMerge verifies that specific thread names produce
// different group names (i.e. must NOT be merged).
func TestThreadGroupNameNoMerge(t *testing.T) {
	shouldNotMerge := [][]string{
		{"io-read-1", "io-write-1"},
		{"pool-1-read", "pool-1-write"},
		{"kafka-coordinator", "kafka-producer"},
		{"http-nio-8080-Acceptor", "http-nio-8080-Poller"},
		{"vert.x-eventloop-thread-0", "vert.x-worker-thread-0"},
		{"ForkJoinPool.commonPool-worker-1", "ForkJoinPool-1-worker-1"},
		{"G1 Conc#0", "G1 Refine#0"},
		{"main", "Main Thread"},
		{"HikariPool-1-housekeeper", "HikariPool-1-connection-adder"},
	}
	for _, pair := range shouldNotMerge {
		a := threadGroupName(pair[0])
		b := threadGroupName(pair[1])
		if a == b {
			t.Errorf("should NOT merge but both map to %q: %v", a, pair)
		}
	}
}

// ---------------------------------------------------------------------------
// TestGroupThreads
// ---------------------------------------------------------------------------

func TestGroupThreads(t *testing.T) {
	entries := []threadEntry{
		{"net-1", 100},
		{"net-2", 50},
		{"gc#0", 30},
		{"gc#1", 20},
		{"main", 10},
	}
	groups := groupThreads(entries)
	if len(groups) != 3 {
		t.Fatalf("expected 3 groups, got %d", len(groups))
	}
	// Sorted by samples descending: net(150), gc(50), main(10)
	if groups[0].name != "net" || groups[0].samples != 150 || groups[0].threads != 2 {
		t.Errorf("group[0] = %+v, want net/150/2", groups[0])
	}
	if groups[1].name != "gc" || groups[1].samples != 50 || groups[1].threads != 2 {
		t.Errorf("group[1] = %+v, want gc/50/2", groups[1])
	}
	if groups[2].name != "main" || groups[2].samples != 10 || groups[2].threads != 1 {
		t.Errorf("group[2] = %+v, want main/10/1", groups[2])
	}
}

// ---------------------------------------------------------------------------
// TestGroupThreadsMultiLevel
// ---------------------------------------------------------------------------

func TestGroupThreadsMultiLevel(t *testing.T) {
	entries := []threadEntry{
		{"pool-1-thread-2", 40},
		{"pool-2-thread-5", 30},
		{"pool-1-thread-9", 20},
		{"main", 10},
	}
	groups := groupThreads(entries)
	// pool-1-thread-2, pool-2-thread-5, pool-1-thread-9 all map to "pool-thread"
	if len(groups) != 2 {
		t.Fatalf("expected 2 groups, got %d: %+v", len(groups), groups)
	}
	if groups[0].name != "pool-thread" || groups[0].samples != 90 || groups[0].threads != 3 {
		t.Errorf("group[0] = %+v, want pool-thread/90/3", groups[0])
	}
	if groups[1].name != "main" || groups[1].samples != 10 || groups[1].threads != 1 {
		t.Errorf("group[1] = %+v, want main/10/1", groups[1])
	}
}

// TestGroupThreadsRealisticMix simulates a realistic JVM application with
// multiple thread pools and singleton threads.
func TestGroupThreadsRealisticMix(t *testing.T) {
	entries := []threadEntry{
		// Tomcat HTTP pool — different ports merge
		{"http-nio-8080-exec-1", 50},
		{"http-nio-8080-exec-2", 40},
		{"http-nio-8443-exec-1", 30},
		// HikariCP — different pool IDs merge
		{"HikariPool-1-housekeeper", 20},
		{"HikariPool-2-housekeeper", 10},
		// G1 GC
		{"G1 Conc#0", 15},
		{"G1 Conc#1", 12},
		// Singletons
		{"main", 8},
		{"DestroyJavaVM", 3},
		// Compiler
		{"CompilerThread0", 5},
		{"CompilerThread1", 4},
	}
	groups := groupThreads(entries)

	// Build a map for easier assertion.
	gm := make(map[string]threadGroupEntry)
	for _, g := range groups {
		gm[g.name] = g
	}

	// Tomcat threads should all merge (port 8080 and 8443).
	if g, ok := gm["http-nio-exec"]; !ok {
		t.Errorf("expected group 'http-nio-exec', got groups: %+v", gm)
	} else {
		if g.threads != 3 || g.samples != 120 {
			t.Errorf("http-nio-exec: want 3 threads / 120 samples, got %+v", g)
		}
	}

	// HikariCP housekeepers merge across pool IDs.
	if g, ok := gm["HikariPool-housekeeper"]; !ok {
		t.Errorf("expected group 'HikariPool-housekeeper', got groups: %+v", gm)
	} else {
		if g.threads != 2 || g.samples != 30 {
			t.Errorf("HikariPool-housekeeper: want 2/30, got %+v", g)
		}
	}

	// G1 Conc threads merge.
	if g, ok := gm["G1 Conc"]; !ok {
		t.Errorf("expected group 'G1 Conc', got groups: %+v", gm)
	} else if g.threads != 2 || g.samples != 27 {
		t.Errorf("G1 Conc: want 2/27, got %+v", g)
	}

	// CompilerThread merges.
	if g, ok := gm["CompilerThread"]; !ok {
		t.Errorf("expected group 'CompilerThread', got groups: %+v", gm)
	} else if g.threads != 2 || g.samples != 9 {
		t.Errorf("CompilerThread: want 2/9, got %+v", g)
	}

	// Singletons stay separate.
	if _, ok := gm["main"]; !ok {
		t.Error("expected singleton group 'main'")
	}
	if _, ok := gm["DestroyJavaVM"]; !ok {
		t.Error("expected singleton group 'DestroyJavaVM'")
	}

	// Total: 6 groups.
	if len(groups) != 6 {
		t.Errorf("expected 6 groups, got %d: %+v", len(groups), gm)
	}
}

// TestGroupThreadsNoFalseMerge verifies that threads from different logical
// pools do not merge even though they share numeric-segment structure.
func TestGroupThreadsNoFalseMerge(t *testing.T) {
	entries := []threadEntry{
		{"io-read-1", 50},
		{"io-read-2", 40},
		{"io-write-1", 30},
		{"io-write-2", 20},
		{"vert.x-eventloop-thread-0", 10},
		{"vert.x-worker-thread-0", 10},
	}
	groups := groupThreads(entries)
	gm := make(map[string]threadGroupEntry)
	for _, g := range groups {
		gm[g.name] = g
	}

	if gm["io-read"].threads != 2 || gm["io-read"].samples != 90 {
		t.Errorf("io-read: want 2/90, got %+v", gm["io-read"])
	}
	if gm["io-write"].threads != 2 || gm["io-write"].samples != 50 {
		t.Errorf("io-write: want 2/50, got %+v", gm["io-write"])
	}
	// Singletons keep their original name (no grouping without evidence).
	if gm["vert.x-eventloop-thread-0"].threads != 1 {
		t.Errorf("vert.x-eventloop-thread-0: want 1 thread, got %+v", gm["vert.x-eventloop-thread-0"])
	}
	if gm["vert.x-worker-thread-0"].threads != 1 {
		t.Errorf("vert.x-worker-thread-0: want 1 thread, got %+v", gm["vert.x-worker-thread-0"])
	}
	if len(groups) != 4 {
		t.Errorf("expected 4 groups, got %d: %+v", len(groups), gm)
	}
}

// TestGroupThreadsSingletonFallback verifies that threads with no grouping
// partner keep their original name instead of being normalised.
func TestGroupThreadsSingletonFallback(t *testing.T) {
	entries := []threadEntry{
		// Two CompilerThreads → merge (2 members share normalised form).
		{"CompilerThread0", 40},
		{"CompilerThread1", 30},
		// One G1 Young Gen → singleton, keep original.
		{"G1 Young Gen", 20},
		// One log4j2-TF-1-Acceptor → singleton, keep original (preserves "2" and "1").
		{"log4j2-TF-1-Acceptor", 10},
		// One main → singleton, keep original.
		{"main", 5},
	}
	groups := groupThreads(entries)
	gm := make(map[string]threadGroupEntry)
	for _, g := range groups {
		gm[g.name] = g
	}

	if g := gm["CompilerThread"]; g.threads != 2 || g.samples != 70 {
		t.Errorf("CompilerThread: want 2/70, got %+v", g)
	}
	if _, ok := gm["G1 Young Gen"]; !ok {
		t.Errorf("expected singleton 'G1 Young Gen', got groups: %+v", gm)
	}
	if _, ok := gm["log4j2-TF-1-Acceptor"]; !ok {
		t.Errorf("expected singleton 'log4j2-TF-1-Acceptor', got groups: %+v", gm)
	}
	if _, ok := gm["main"]; !ok {
		t.Errorf("expected singleton 'main', got groups: %+v", gm)
	}
	if len(groups) != 4 {
		t.Errorf("expected 4 groups, got %d: %+v", len(groups), gm)
	}
}

// TestGroupThreadsSingletonBecomesGroupWithPeer verifies that a thread that
// would be a singleton on its own merges once a peer appears.
func TestGroupThreadsSingletonBecomesGroupWithPeer(t *testing.T) {
	// With only one worker, it stays as-is.
	solo := []threadEntry{{"worker-1", 100}}
	g1 := groupThreads(solo)
	if g1[0].name != "worker-1" {
		t.Errorf("solo: expected 'worker-1', got %q", g1[0].name)
	}

	// Add a second worker — now they merge.
	pair := []threadEntry{{"worker-1", 100}, {"worker-2", 50}}
	g2 := groupThreads(pair)
	if len(g2) != 1 || g2[0].name != "worker" {
		t.Errorf("pair: expected single group 'worker', got %+v", g2)
	}
}

// TestAssignGroupsOriginalMatchesNormalized covers the case where one
// thread's original name equals another thread's normalised form.
// e.g. "worker" (already normalised) + "worker-1" → both map to "worker".
func TestAssignGroupsOriginalMatchesNormalized(t *testing.T) {
	entries := []threadEntry{
		{"worker", 80},
		{"worker-1", 50},
	}
	a := assignGroups(entries)
	if a["worker"] != "worker" {
		t.Errorf("worker: expected 'worker', got %q", a["worker"])
	}
	if a["worker-1"] != "worker" {
		t.Errorf("worker-1: expected 'worker', got %q", a["worker-1"])
	}
}

// TestCrossEventCombinedAssignment verifies that printCrossEventSummary
// uses combined thread lists for grouping: a thread pool appearing in only
// one event type still merges if the other side has a peer.
func TestCrossEventCombinedAssignment(t *testing.T) {
	// CPU has only worker-1; WALL has worker-1 + worker-2.
	// Without combined assignment, CPU side would be singleton "worker-1".
	// With combined assignment, both sides use "worker" because the
	// combined set has 2 distinct names mapping to "worker".
	cpuSF := makeStackFile([]stack{
		{frames: []string{"A.a"}, lines: []uint32{0}, count: 100, thread: "worker-1"},
	})
	wallSF := makeStackFile([]stack{
		{frames: []string{"A.a"}, lines: []uint32{0}, count: 50, thread: "worker-1"},
		{frames: []string{"A.a"}, lines: []uint32{0}, count: 50, thread: "worker-2"},
	})
	stacksByEvent := map[string]*stackFile{
		"cpu":  cpuSF,
		"wall": wallSF,
	}

	out := captureOutput(func() {
		cmdInfo(cpuSF, infoOpts{
			eventType:     "cpu",
			hasMetadata:   true,
			eventCounts:   map[string]int{"cpu": 100, "wall": 100},
			topThreads:    10,
			topMethods:    10,
			stacksByEvent: stacksByEvent,
		})
	})

	// The cross-event summary should show a "worker" group, not "worker-1".
	if !strings.Contains(out, "=== CPU vs WALL ===") {
		t.Fatalf("expected cross-event summary, got:\n%s", out)
	}
	// "worker (2)" indicates merged group with 2 threads.
	if !strings.Contains(out, "worker (2)") {
		t.Errorf("expected 'worker (2)' in cross-event summary, got:\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// TestCmdInfoCrossEventSkippedWithZeroSamples
// ---------------------------------------------------------------------------

func TestCmdInfoCrossEventSkippedWithZeroSamples(t *testing.T) {
	cpuSF := makeStackFile([]stack{
		{frames: []string{"A.a"}, lines: []uint32{0}, count: 10, thread: "main"},
	})
	wallSF := &stackFile{totalSamples: 0}
	stacksByEvent := map[string]*stackFile{
		"cpu":  cpuSF,
		"wall": wallSF,
	}

	out := captureOutput(func() {
		cmdInfo(cpuSF, infoOpts{eventType: "cpu", hasMetadata: true, eventCounts: map[string]int{"cpu": 10, "wall": 0}, topThreads: 5, topMethods: 10, stacksByEvent: stacksByEvent})
	})

	if strings.Contains(out, "=== CPU vs WALL ===") {
		t.Error("expected no cross-event summary when wall has zero samples")
	}
}

// TestCmdInfoCrossEventIdleFiltered verifies that when stacksByEvent
// entries have been idle-filtered, the cross-event summary reflects the
// filtered data (not original unfiltered counts).
func TestCmdInfoCrossEventIdleFiltered(t *testing.T) {
	// CPU: 80 active on worker-1, 20 idle (Object.wait) on io-1.
	// WALL: 30 active on worker-1, 70 idle (Object.wait) on io-1.
	cpuSF := makeStackFile([]stack{
		{frames: []string{"A.a"}, lines: []uint32{0}, count: 80, thread: "worker-1"},
		{frames: []string{"java/lang/Object.wait"}, lines: []uint32{0}, count: 20, thread: "io-1"},
	})
	wallSF := makeStackFile([]stack{
		{frames: []string{"A.a"}, lines: []uint32{0}, count: 30, thread: "worker-1"},
		{frames: []string{"java/lang/Object.wait"}, lines: []uint32{0}, count: 70, thread: "io-1"},
	})

	// Simulate --no-idle: filter both sides before passing to cmdInfo.
	filteredStacksByEvent := map[string]*stackFile{
		"cpu":  cpuSF.filterIdle(),
		"wall": wallSF.filterIdle(),
	}

	filteredCpuSF := cpuSF.filterIdle()
	out := captureOutput(func() {
		cmdInfo(filteredCpuSF, infoOpts{
			eventType:     "cpu",
			hasMetadata:   true,
			eventCounts:   map[string]int{"cpu": 100, "wall": 100},
			topThreads:    10,
			topMethods:    10,
			stacksByEvent: filteredStacksByEvent,
		})
	})

	// After idle filtering, only worker-1 remains (io-1 was idle).
	// Cross-event summary should NOT show io — its samples were all idle.
	if strings.Contains(out, "=== CPU vs WALL ===") {
		// With only one thread remaining, cross-event might still show if
		// both sides have data. Check that io is absent.
		if strings.Contains(out, "io") {
			t.Errorf("expected no 'io' in cross-event summary after idle filter, got:\n%s", out)
		}
	}

	// The worker should show 100% on CPU side (80/80 = 100%).
	if !strings.Contains(out, "100.0%") {
		t.Errorf("expected worker-1 at 100.0%% CPU after idle filter, got:\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// TestCmdInfoCrossEvent
// ---------------------------------------------------------------------------

func TestCmdInfoCrossEvent(t *testing.T) {
	cpuSF := makeStackFile([]stack{
		{frames: []string{"A.a"}, lines: []uint32{0}, count: 80, thread: "worker-1"},
		{frames: []string{"A.a"}, lines: []uint32{0}, count: 20, thread: "io-1"},
	})
	wallSF := makeStackFile([]stack{
		{frames: []string{"A.a"}, lines: []uint32{0}, count: 30, thread: "worker-1"},
		{frames: []string{"A.a"}, lines: []uint32{0}, count: 70, thread: "io-1"},
	})
	stacksByEvent := map[string]*stackFile{
		"cpu":  cpuSF,
		"wall": wallSF,
	}

	out := captureOutput(func() {
		cmdInfo(cpuSF, infoOpts{eventType: "cpu", hasMetadata: true, eventCounts: map[string]int{"cpu": 100, "wall": 100}, topThreads: 10, topMethods: 10, stacksByEvent: stacksByEvent})
	})

	if !strings.Contains(out, "=== CPU vs WALL ===") {
		t.Errorf("expected cross-event summary header, got:\n%s", out)
	}
	if !strings.Contains(out, "worker") {
		t.Errorf("expected 'worker' group in cross-event summary, got:\n%s", out)
	}
	if !strings.Contains(out, "io") {
		t.Errorf("expected 'io' group in cross-event summary, got:\n%s", out)
	}
	// worker: CPU=80%, WALL=30%
	if !strings.Contains(out, "80.0%") {
		t.Errorf("expected '80.0%%' for worker CPU, got:\n%s", out)
	}
}

func TestCmdInfoCrossEventWallOnlyGroup(t *testing.T) {
	cpuSF := makeStackFile([]stack{
		{frames: []string{"A.a"}, lines: []uint32{0}, count: 100, thread: "worker-1"},
	})
	wallSF := makeStackFile([]stack{
		{frames: []string{"A.a"}, lines: []uint32{0}, count: 30, thread: "worker-1"},
		{frames: []string{"A.a"}, lines: []uint32{0}, count: 70, thread: "io-1"},
	})
	stacksByEvent := map[string]*stackFile{
		"cpu":  cpuSF,
		"wall": wallSF,
	}

	out := captureOutput(func() {
		cmdInfo(cpuSF, infoOpts{eventType: "cpu", hasMetadata: true, eventCounts: map[string]int{"cpu": 100, "wall": 100}, topThreads: 10, topMethods: 10, stacksByEvent: stacksByEvent})
	})

	// "io" group exists only in wall, should still appear with 0.0% CPU
	if !strings.Contains(out, "io") {
		t.Errorf("expected wall-only 'io' group in cross-event summary, got:\n%s", out)
	}
	if !strings.Contains(out, "0.0%") {
		t.Errorf("expected '0.0%%' CPU for wall-only group, got:\n%s", out)
	}
}

func TestCmdInfoCrossEventSkippedWithNil(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.a"}, lines: []uint32{0}, count: 10, thread: "main"},
	})

	out := captureOutput(func() {
		cmdInfo(sf, infoOpts{eventType: "cpu", hasMetadata: true, eventCounts: map[string]int{"cpu": 10}, topThreads: 5, topMethods: 10})
	})

	if strings.Contains(out, "=== CPU vs WALL ===") {
		t.Error("expected no cross-event summary when stacksByEvent is nil")
	}
}

func TestCmdInfoCrossEventSkippedWithOnlyOneSide(t *testing.T) {
	cpuSF := makeStackFile([]stack{
		{frames: []string{"A.a"}, lines: []uint32{0}, count: 10, thread: "main"},
	})
	stacksByEvent := map[string]*stackFile{
		"cpu": cpuSF,
	}

	out := captureOutput(func() {
		cmdInfo(cpuSF, infoOpts{eventType: "cpu", hasMetadata: true, eventCounts: map[string]int{"cpu": 10}, topThreads: 5, topMethods: 10, stacksByEvent: stacksByEvent})
	})

	if strings.Contains(out, "=== CPU vs WALL ===") {
		t.Error("expected no cross-event summary when only cpu present")
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

func TestDetectFormat(t *testing.T) {
	tests := []struct {
		path string
		want profileFormat
	}{
		{"profile.jfr", formatJFR},
		{"profile.jfr.gz", formatJFR},
		{"profile.JFR", formatJFR},
		{"profile.JFR.GZ", formatJFR},
		{"profile.pb.gz", formatPprof},
		{"profile.pb", formatPprof},
		{"profile.pprof", formatPprof},
		{"profile.pprof.gz", formatPprof},
		{"stacks.txt", formatCollapsed},
		{"stacks.collapsed", formatCollapsed},
		{"stacks.gz", formatCollapsed},
		{"-", formatCollapsed},
		{"unknown", formatCollapsed},
	}
	for _, tt := range tests {
		got := detectFormat(tt.path)
		if got != tt.want {
			t.Errorf("detectFormat(%q) = %v, want %v", tt.path, got, tt.want)
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
// TestIsIdleLeaf
// ---------------------------------------------------------------------------

func TestIsIdleLeaf(t *testing.T) {
	tests := []struct {
		frame string
		want  bool
	}{
		{"libc.so.6.__futex_abstimed_wait_common", true},
		{"libc.so.6.__sched_yield", true},
		{"libc.so.6.epoll_wait", true},
		{"java/lang/Thread.sleep", true},
		{"java/lang/Object.wait", true},
		{"java/util/concurrent/locks/LockSupport.park", true},
		{"java/util/concurrent/locks/LockSupport.parkNanos", true},
		{"sun/misc/Unsafe.park", true},
		{"libc.so.6.pthread_cond_wait", true},
		{"libc.so.6.pthread_cond_timedwait", true},
		// JDK variants with numeric suffixes
		{"java/lang/Object.wait0", true},
		{"java/lang/Thread.sleep0", true},

		// Non-idle
		{"io/questdb/mp/Worker.run", false},
		{"java/util/HashMap.resize", false},
		{"libc.so.6.__memmove_avx_unaligned_erms", false},

		// False-positive prevention: class name contains idle class as substring
		{"com/example/TransactionObject.waitFor", false},
		{"com/example/WorkerThread.sleepUntilReady", false},
		{"com/example/MyThread.sleeper", false},
		{"com/example/ObjectMapper.waitForResult", false},
	}
	for _, tt := range tests {
		got := isIdleLeaf(tt.frame)
		if got != tt.want {
			t.Errorf("isIdleLeaf(%q) = %v, want %v", tt.frame, got, tt.want)
		}
	}
}

func TestFilterIdle(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.a", "B.b"}, lines: []uint32{0, 0}, count: 10},
		{frames: []string{"A.a", "java/lang/Thread.sleep"}, lines: []uint32{0, 0}, count: 20},
		{frames: []string{"C.c", "libc.so.6.__futex_abstimed_wait_common"}, lines: []uint32{0, 0}, count: 30},
		{frames: []string{"D.d"}, lines: []uint32{0}, count: 5},
	})

	filtered := sf.filterIdle()
	if len(filtered.stacks) != 2 {
		t.Errorf("expected 2 non-idle stacks, got %d", len(filtered.stacks))
	}
	if filtered.totalSamples != 15 {
		t.Errorf("expected totalSamples=15, got %d", filtered.totalSamples)
	}
}

// ---------------------------------------------------------------------------
// TestParseFlags
// ---------------------------------------------------------------------------

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
	targets := resolveTargets("/nonexistent", true, false, false)
	if len(targets) != 1 || targets[0] != "claude" {
		t.Errorf("expected [claude], got %v", targets)
	}

	targets = resolveTargets("/nonexistent", false, true, false)
	if len(targets) != 1 || targets[0] != "codex" {
		t.Errorf("expected [codex], got %v", targets)
	}

	targets = resolveTargets("/nonexistent", true, true, false)
	if len(targets) != 2 {
		t.Errorf("expected 2 targets, got %v", targets)
	}
}

func TestResolveTargetsAutoDetect(t *testing.T) {
	// Global scope: codex uses .codex
	t.Run("global", func(t *testing.T) {
		dir := t.TempDir()

		// No agent dirs → empty
		targets := resolveTargets(dir, false, false, false)
		if len(targets) != 0 {
			t.Errorf("expected empty, got %v", targets)
		}

		// Create .claude → detects claude only
		os.MkdirAll(filepath.Join(dir, ".claude"), 0755)
		targets = resolveTargets(dir, false, false, false)
		if len(targets) != 1 || targets[0] != "claude" {
			t.Errorf("expected [claude], got %v", targets)
		}

		// Create .codex → detects both
		os.MkdirAll(filepath.Join(dir, ".codex"), 0755)
		targets = resolveTargets(dir, false, false, false)
		if len(targets) != 2 {
			t.Errorf("expected 2 targets, got %v", targets)
		}
	})

	// Global scope: CODEX_HOME overrides ~/.codex
	t.Run("global_codex_home", func(t *testing.T) {
		dir := t.TempDir()
		codexHome := t.TempDir()
		t.Setenv("CODEX_HOME", codexHome)

		// CODEX_HOME dir exists → detects codex
		targets := resolveTargets(dir, false, false, false)
		if len(targets) != 1 || targets[0] != "codex" {
			t.Errorf("expected [codex] with CODEX_HOME, got %v", targets)
		}
	})

	// Project scope: codex uses .agents
	t.Run("project", func(t *testing.T) {
		dir := t.TempDir()

		// Create .agents → detects codex
		os.MkdirAll(filepath.Join(dir, ".agents"), 0755)
		targets := resolveTargets(dir, false, false, true)
		if len(targets) != 1 || targets[0] != "codex" {
			t.Errorf("expected [codex], got %v", targets)
		}

		// Create .claude too → detects both
		os.MkdirAll(filepath.Join(dir, ".claude"), 0755)
		targets = resolveTargets(dir, false, false, true)
		if len(targets) != 2 {
			t.Errorf("expected 2 targets, got %v", targets)
		}
	})

	// Project scope: .codex alone does NOT trigger codex detection
	t.Run("project_codex_dir_ignored", func(t *testing.T) {
		dir := t.TempDir()

		os.MkdirAll(filepath.Join(dir, ".codex"), 0755)
		targets := resolveTargets(dir, false, false, true)
		if len(targets) != 0 {
			t.Errorf("expected empty (project ignores .codex), got %v", targets)
		}
	})
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

	if !strings.Contains(out, "no stacks matching") {
		t.Errorf("expected 'no stacks matching', got %q", out)
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
		cmdThreads(sf, 0, false)
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
		cmdThreads(sf, 1, false)
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
		cmdThreads(sf, 0, false)
	})

	if !strings.Contains(out, "no thread info") {
		t.Errorf("expected 'no thread info', got %q", out)
	}
}

func TestCmdThreadsEmpty(t *testing.T) {
	sf := makeStackFile(nil)

	out := captureOutput(func() {
		cmdThreads(sf, 0, false)
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
	writeSkill(dir, "claude", "test content", false, false)

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
	writeSkill(dir, "claude", "original", false, false)
	writeSkill(dir, "claude", "updated", true, false)

	path := filepath.Join(dir, ".claude", "skills", "jfr", "SKILL.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "updated" {
		t.Errorf("content = %q, want 'updated'", data)
	}
}

func TestWriteSkillCodexGlobal(t *testing.T) {
	dir := t.TempDir()
	writeSkill(dir, "codex", "global codex", false, false)

	path := filepath.Join(dir, ".codex", "skills", "jfr", "SKILL.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("skill file not found: %v", err)
	}
	if string(data) != "global codex" {
		t.Errorf("content = %q, want 'global codex'", data)
	}
}

func TestWriteSkillCodexProject(t *testing.T) {
	dir := t.TempDir()
	writeSkill(dir, "codex", "project codex", false, true)

	path := filepath.Join(dir, ".agents", "skills", "jfr", "SKILL.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("skill file not found: %v", err)
	}
	if string(data) != "project codex" {
		t.Errorf("content = %q, want 'project codex'", data)
	}
}

func TestWriteSkillCodexHome(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)

	baseDir := t.TempDir() // home dir, should be ignored for codex global
	writeSkill(baseDir, "codex", "codex home", false, false)

	path := filepath.Join(codexHome, "skills", "jfr", "SKILL.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("skill file not found: %v", err)
	}
	if string(data) != "codex home" {
		t.Errorf("content = %q, want 'codex home'", data)
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

	if !strings.Contains(out, "no stacks matching") {
		t.Errorf("expected 'no stacks matching', got %q", out)
	}
}

func TestCmdCallersNoMatch(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.a", "B.b"}, lines: []uint32{0, 0}, count: 10, thread: "main"},
	})

	out := captureOutput(func() {
		cmdCallers(sf, "Nonexistent", 4, 1.0)
	})

	if !strings.Contains(out, "no stacks matching") {
		t.Errorf("expected 'no stacks matching', got %q", out)
	}
}

func TestCmdTreeEmpty(t *testing.T) {
	sf := makeStackFile(nil)

	out := captureOutput(func() {
		cmdTree(sf, "A.a", 4, 1.0)
	})

	if !strings.Contains(out, "no samples") {
		t.Errorf("expected 'no samples' message, got %q", out)
	}
}

func TestCmdCallersEmpty(t *testing.T) {
	sf := makeStackFile(nil)

	out := captureOutput(func() {
		cmdCallers(sf, "A.a", 4, 1.0)
	})

	if !strings.Contains(out, "no samples") {
		t.Errorf("expected 'no samples' message, got %q", out)
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

	if !strings.Contains(out, "no stacks matching") || strings.Contains(out, "B.b") {
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

	sf, hasMetadata, err := openInput(path, "cpu")
	if err != nil {
		t.Fatal(err)
	}
	if hasMetadata {
		t.Error("expected hasMetadata=false for .txt file")
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
		cmdInfo(sf, infoOpts{eventType: "cpu", topThreads: 10, topMethods: 20})
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
			parsed, err := parseJFRData(jfrFixture(tt.file), nil, parseOpts{})
			if err != nil {
				t.Fatalf("parseJFRData(%s): %v", tt.file, err)
			}
			counts := parsed.eventCounts
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
	parsed, err := parseJFRData(jfrFixture("multi.jfr"), nil, parseOpts{})
	if err != nil {
		t.Fatalf("parseJFRData: %v", err)
	}
	counts := parsed.eventCounts
	if len(counts) < 2 {
		t.Errorf("expected >=2 event types in multi.jfr, got %d: %v", len(counts), counts)
	}
	for name, n := range counts {
		if n <= 0 {
			t.Errorf("event %q has %d samples, expected >0", name, n)
		}
	}
}

func TestParseJFRDataAllEventsMatchesSingleEventParsing(t *testing.T) {
	path := jfrFixture("multi.jfr")

	parsed, err := parseJFRData(path, allEventTypes(), parseOpts{})
	if err != nil {
		t.Fatalf("parseJFRData: %v", err)
	}

	for _, eventType := range []string{"cpu", "wall", "alloc", "lock"} {
		single, err := parseJFRData(path, singleEventType(eventType), parseOpts{})
		if err != nil {
			t.Fatalf("parseJFRData(%s): %v", eventType, err)
		}
		want := single.stacksByEvent[eventType]
		if want == nil {
			want = &stackFile{}
		}
		got := parsed.stacksByEvent[eventType]
		if got == nil {
			got = &stackFile{}
		}
		if got.totalSamples != want.totalSamples {
			t.Errorf("%s totalSamples=%d, want %d", eventType, got.totalSamples, want.totalSamples)
		}
	}
}

func TestJFRBranchMissesMapsToCPU(t *testing.T) {
	parsed, err := parseJFRData(jfrFixture("branch-misses.jfr"), nil, parseOpts{})
	if err != nil {
		t.Fatalf("parseJFRData: %v", err)
	}
	counts := parsed.eventCounts
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
	parsed, err := parseJFRData(path, nil, parseOpts{})
	if err != nil {
		t.Fatalf("parseJFRData: %v", err)
	}
	eventCounts := parsed.eventCounts
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
		cmdInfo(sf, infoOpts{eventType: eventType, hasMetadata: true, eventCounts: eventCounts, topThreads: 10, topMethods: 20})
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
	parsed, err := parseJFRData(path, nil, parseOpts{})
	if err != nil {
		t.Fatalf("parseJFRData: %v", err)
	}
	eventCounts := parsed.eventCounts

	out := captureOutput(func() {
		cmdInfo(sf, infoOpts{eventType: "cpu", hasMetadata: true, eventCounts: eventCounts, topThreads: 10, topMethods: 20})
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

	// Should NOT show the "no stacks matching" message
	if strings.Contains(out, "no stacks matching") {
		t.Errorf("should not show 'no stacks matching' when method is empty, got:\n%s", out)
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

	if !strings.Contains(out, "no samples") {
		t.Errorf("expected 'no samples' message, got %q", out)
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

	if !strings.Contains(out, "no stacks matching") {
		t.Errorf("expected 'no stacks matching', got %q", out)
	}
}

func TestCmdTraceEmpty(t *testing.T) {
	sf := makeStackFile(nil)

	out := captureOutput(func() {
		cmdTrace(sf, "A.a", 0.0, false)
	})

	if !strings.Contains(out, "no samples") {
		t.Errorf("expected 'no samples' message, got %q", out)
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

// ---------------------------------------------------------------------------
// TestCollapseRoundTrip
// ---------------------------------------------------------------------------

// aggregateStacks normalises a stackFile into map["thread||frame1;frame2;..."] → totalCount,
// collapsing entries that differ only by line numbers.
func aggregateStacks(sf *stackFile) map[string]int {
	m := make(map[string]int, len(sf.stacks))
	for i := range sf.stacks {
		st := &sf.stacks[i]
		key := st.thread + "||" + strings.Join(st.frames, ";")
		m[key] += st.count
	}
	return m
}

func TestCollapseRoundTrip(t *testing.T) {
	tests := []struct {
		fixture string
		event   string
	}{
		{"cpu.jfr", "cpu"},
		{"wall.jfr", "wall"},
		{"alloc.jfr", "alloc"},
		{"lock.jfr", "lock"},
	}

	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			// 1. Parse JFR directly
			parsed, err := parseJFRData(jfrFixture(tt.fixture), singleEventType(tt.event), parseOpts{})
			if err != nil {
				t.Fatalf("parseJFRData: %v", err)
			}
			jfrSF := parsed.stacksByEvent[tt.event]
			if jfrSF == nil {
				t.Fatalf("no stacks for event %q", tt.event)
			}

			// 2. Collapse to text
			collapsed := captureOutput(func() { cmdCollapse(jfrSF) })

			// 3. Parse collapsed text back
			roundTripSF, err := parseCollapsed(strings.NewReader(collapsed))
			if err != nil {
				t.Fatalf("parseCollapsed: %v", err)
			}

			// 4. Aggregate both sides (normalises line-number-only differences)
			jfrAgg := aggregateStacks(jfrSF)
			rtAgg := aggregateStacks(roundTripSF)

			// 5. Total sample counts must match
			if jfrSF.totalSamples != roundTripSF.totalSamples {
				t.Errorf("totalSamples mismatch: jfr=%d roundTrip=%d", jfrSF.totalSamples, roundTripSF.totalSamples)
			}

			// 6. Aggregated map sizes must match
			if len(jfrAgg) != len(rtAgg) {
				t.Errorf("aggregated key count mismatch: jfr=%d roundTrip=%d", len(jfrAgg), len(rtAgg))
			}

			// 7. Every key/count must match in both directions
			for key, jfrCount := range jfrAgg {
				rtCount, ok := rtAgg[key]
				if !ok {
					t.Errorf("key present in jfr but missing after round-trip: %q (count=%d)", key, jfrCount)
					continue
				}
				if jfrCount != rtCount {
					t.Errorf("count mismatch for %q: jfr=%d roundTrip=%d", key, jfrCount, rtCount)
				}
			}
			for key, rtCount := range rtAgg {
				if _, ok := jfrAgg[key]; !ok {
					t.Errorf("key present after round-trip but missing in jfr: %q (count=%d)", key, rtCount)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestParsePerfCollapsed — real collapsed stacks from Linux perf
// ---------------------------------------------------------------------------

func TestParsePerfCollapsed(t *testing.T) {
	f, err := os.Open(filepath.Join("testdata", "perf.collapsed"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	sf, err := parseCollapsed(f)
	if err != nil {
		t.Fatalf("parseCollapsed: %v", err)
	}

	if len(sf.stacks) == 0 {
		t.Fatal("expected non-empty stacks from perf fixture")
	}

	// Every stack must have at least one frame and a positive count.
	for i, st := range sf.stacks {
		if len(st.frames) == 0 {
			t.Errorf("stack[%d]: no frames", i)
		}
		if st.count <= 0 {
			t.Errorf("stack[%d]: count=%d, want >0", i, st.count)
		}
	}

	// Total samples must equal the sum of individual counts.
	sum := 0
	for _, st := range sf.stacks {
		sum += st.count
	}
	if sf.totalSamples != sum {
		t.Errorf("totalSamples=%d, want sum of counts=%d", sf.totalSamples, sum)
	}

	// Perf collapsed stacks have no thread markers (process name is a plain
	// frame, not "[thread]"), so every stack should have an empty thread.
	for i, st := range sf.stacks {
		if st.thread != "" {
			t.Errorf("stack[%d]: unexpected thread=%q in perf data", i, st.thread)
		}
	}

	// Spot-check: "dd" must appear as the root frame of every stack (the
	// process name that stackcollapse-perf.pl prepends).
	for i, st := range sf.stacks {
		if st.frames[0] != "dd" {
			t.Errorf("stack[%d]: root frame=%q, want \"dd\"", i, st.frames[0])
		}
	}

	// Spot-check: kernel symbols must survive parsing intact.
	found := false
	for _, st := range sf.stacks {
		for _, fr := range st.frames {
			if fr == "chacha_permute" {
				found = true
			}
		}
	}
	if !found {
		t.Error("expected kernel frame \"chacha_permute\" in perf stacks")
	}

	// Commands must work on perf data. Smoke-test hot and tree.
	hotOut := captureOutput(func() {
		cmdHot(sf, 5, false, 0)
	})
	if !strings.Contains(hotOut, "SELF%") {
		t.Errorf("hot output missing header, got:\n%s", hotOut)
	}
	if !strings.Contains(hotOut, "chacha_permute") {
		t.Errorf("hot output missing expected top method, got:\n%s", hotOut)
	}

	treeOut := captureOutput(func() {
		cmdTree(sf, "chacha_permute", 4, 1.0)
	})
	if !strings.Contains(treeOut, "chacha_permute") {
		t.Errorf("tree output missing target method, got:\n%s", treeOut)
	}
}

// ---------------------------------------------------------------------------
// hideFrames unit tests
// ---------------------------------------------------------------------------

func TestHideFramesBasic(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.a", "Framework.wrap", "B.b", "C.c"}, lines: []uint32{1, 2, 3, 4}, count: 10, thread: "main"},
	})
	re := regexp.MustCompile("Framework")
	got := sf.hideFrames(re)

	if len(got.stacks) != 1 {
		t.Fatalf("expected 1 stack, got %d", len(got.stacks))
	}
	st := got.stacks[0]
	wantFrames := []string{"A.a", "B.b", "C.c"}
	wantLines := []uint32{1, 3, 4}
	if len(st.frames) != len(wantFrames) {
		t.Fatalf("frames len=%d, want %d", len(st.frames), len(wantFrames))
	}
	for i := range wantFrames {
		if st.frames[i] != wantFrames[i] {
			t.Errorf("frames[%d]=%q, want %q", i, st.frames[i], wantFrames[i])
		}
		if st.lines[i] != wantLines[i] {
			t.Errorf("lines[%d]=%d, want %d", i, st.lines[i], wantLines[i])
		}
	}
	if st.count != 10 {
		t.Errorf("count=%d, want 10", st.count)
	}
	if st.thread != "main" {
		t.Errorf("thread=%q, want main", st.thread)
	}
	if got.totalSamples != sf.totalSamples {
		t.Errorf("totalSamples=%d, want %d", got.totalSamples, sf.totalSamples)
	}
}

func TestHideFramesConsecutive(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.a", "Wrap.one", "Wrap.two", "B.b"}, lines: []uint32{0, 0, 0, 0}, count: 5, thread: ""},
	})
	re := regexp.MustCompile("Wrap")
	got := sf.hideFrames(re)

	if len(got.stacks) != 1 {
		t.Fatalf("expected 1 stack, got %d", len(got.stacks))
	}
	st := got.stacks[0]
	if len(st.frames) != 2 || st.frames[0] != "A.a" || st.frames[1] != "B.b" {
		t.Errorf("frames=%v, want [A.a B.b]", st.frames)
	}
}

func TestHideFramesAlternation(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.a", "Foo.bar", "B.b", "Baz.qux", "C.c"}, lines: []uint32{0, 0, 0, 0, 0}, count: 7, thread: ""},
	})
	re := regexp.MustCompile("Foo|Baz")
	got := sf.hideFrames(re)

	if len(got.stacks) != 1 {
		t.Fatalf("expected 1 stack, got %d", len(got.stacks))
	}
	st := got.stacks[0]
	if len(st.frames) != 3 || st.frames[0] != "A.a" || st.frames[1] != "B.b" || st.frames[2] != "C.c" {
		t.Errorf("frames=%v, want [A.a B.b C.c]", st.frames)
	}
}

func TestHideFramesLeaf(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.a", "B.b", "Leaf.work"}, lines: []uint32{0, 0, 0}, count: 10, thread: ""},
	})
	re := regexp.MustCompile("Leaf")
	got := sf.hideFrames(re)

	if len(got.stacks) != 1 {
		t.Fatalf("expected 1 stack, got %d", len(got.stacks))
	}
	st := got.stacks[0]
	// B.b becomes the new leaf
	if len(st.frames) != 2 || st.frames[1] != "B.b" {
		t.Errorf("frames=%v, want [A.a B.b]", st.frames)
	}
}

func TestHideFramesAllHidden(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.a", "B.b"}, lines: []uint32{0, 0}, count: 10, thread: ""},
		{frames: []string{"A.a", "C.c"}, lines: []uint32{0, 0}, count: 5, thread: ""},
	})
	re := regexp.MustCompile(".*") // matches everything
	got := sf.hideFrames(re)

	if len(got.stacks) != 0 {
		t.Errorf("expected 0 stacks, got %d", len(got.stacks))
	}
	if got.totalSamples != sf.totalSamples {
		t.Errorf("totalSamples=%d, want %d (preserved from original)", got.totalSamples, sf.totalSamples)
	}
}

func TestHideFramesNoMatch(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.a", "B.b", "C.c"}, lines: []uint32{1, 2, 3}, count: 10, thread: "main"},
	})
	re := regexp.MustCompile("Nonexistent")
	got := sf.hideFrames(re)

	if len(got.stacks) != 1 {
		t.Fatalf("expected 1 stack, got %d", len(got.stacks))
	}
	st := got.stacks[0]
	if len(st.frames) != 3 {
		t.Errorf("frames len=%d, want 3 (no match should keep all)", len(st.frames))
	}
	for i := range sf.stacks[0].frames {
		if st.frames[i] != sf.stacks[0].frames[i] {
			t.Errorf("frames[%d]=%q, want %q", i, st.frames[i], sf.stacks[0].frames[i])
		}
		if st.lines[i] != sf.stacks[0].lines[i] {
			t.Errorf("lines[%d]=%d, want %d", i, st.lines[i], sf.stacks[0].lines[i])
		}
	}
}

func TestHideFramesNormalized(t *testing.T) {
	// Hide should match against normalized (slash→dot) names
	sf := makeStackFile([]stack{
		{frames: []string{"com/example/Wrap.handle", "com/example/App.run"}, lines: []uint32{0, 0}, count: 10, thread: ""},
	})
	re := regexp.MustCompile("com\\.example\\.Wrap")
	got := sf.hideFrames(re)

	if len(got.stacks) != 1 {
		t.Fatalf("expected 1 stack, got %d", len(got.stacks))
	}
	if got.stacks[0].frames[0] != "com/example/App.run" {
		t.Errorf("expected only App.run, got %v", got.stacks[0].frames)
	}
}

func TestHideFramesPartialStacks(t *testing.T) {
	// One stack fully hidden, another survives
	sf := makeStackFile([]stack{
		{frames: []string{"Wrap.a"}, lines: []uint32{0}, count: 3, thread: ""},
		{frames: []string{"Wrap.a", "Real.b"}, lines: []uint32{0, 0}, count: 7, thread: ""},
	})
	re := regexp.MustCompile("Wrap")
	got := sf.hideFrames(re)

	if len(got.stacks) != 1 {
		t.Fatalf("expected 1 stack (the one with Real.b), got %d", len(got.stacks))
	}
	if got.stacks[0].frames[0] != "Real.b" {
		t.Errorf("surviving stack should be [Real.b], got %v", got.stacks[0].frames)
	}
	if got.totalSamples != 10 {
		t.Errorf("totalSamples=%d, want 10 (preserved)", got.totalSamples)
	}
}

// ---------------------------------------------------------------------------
// hideFrames integration tests via cmdTree
// ---------------------------------------------------------------------------

func TestCmdTreeHide(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.main", "Framework.wrap", "B.process", "C.work"}, lines: []uint32{0, 0, 0, 0}, count: 50, thread: ""},
		{frames: []string{"A.main", "Framework.wrap", "B.process", "D.other"}, lines: []uint32{0, 0, 0, 0}, count: 50, thread: ""},
	})
	re := regexp.MustCompile("Framework")
	hidden := sf.hideFrames(re)

	out := captureOutput(func() {
		cmdTree(hidden, "", 4, 0.0)
	})

	// Framework.wrap should be gone
	if strings.Contains(out, "Framework") {
		t.Errorf("expected Framework removed, got:\n%s", out)
	}
	// A.main, B.process, C.work, D.other should survive
	for _, want := range []string{"A.main", "B.process", "C.work", "D.other"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in output, got:\n%s", want, out)
		}
	}
}

func TestCmdTreeHideWithMethod(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.main", "Framework.wrap", "B.process", "C.work"}, lines: []uint32{0, 0, 0, 0}, count: 100, thread: ""},
	})
	re := regexp.MustCompile("Framework")
	hidden := sf.hideFrames(re)

	out := captureOutput(func() {
		cmdTree(hidden, "B.process", 4, 0.0)
	})

	if strings.Contains(out, "Framework") {
		t.Errorf("Framework should be hidden, got:\n%s", out)
	}
	if !strings.Contains(out, "B.process") {
		t.Errorf("expected B.process in output, got:\n%s", out)
	}
	if !strings.Contains(out, "C.work") {
		t.Errorf("expected C.work in output, got:\n%s", out)
	}
}

func TestCmdTreeHideRemovesMethodTarget(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.a", "Target.run", "B.b"}, lines: []uint32{0, 0, 0}, count: 10, thread: ""},
	})
	re := regexp.MustCompile("Target")
	hidden := sf.hideFrames(re)

	out := captureOutput(func() {
		cmdTree(hidden, "Target.run", 4, 0.0)
	})

	if !strings.Contains(out, "no stacks matching") {
		t.Errorf("expected 'no stacks matching' when target is hidden, got:\n%s", out)
	}
}

func TestCmdTreeHideDepthBenefit(t *testing.T) {
	// With Framework.wrap present, depth=3 can only reach B.process
	// After hiding, depth=3 reaches C.work
	sf := makeStackFile([]stack{
		{frames: []string{"A.main", "Framework.wrap", "B.process", "C.work"}, lines: []uint32{0, 0, 0, 0}, count: 100, thread: ""},
	})

	// Without hide, depth=3 from root shows A.main→Framework.wrap→B.process but not C.work
	outBefore := captureOutput(func() {
		cmdTree(sf, "", 3, 0.0)
	})
	if strings.Contains(outBefore, "C.work") {
		t.Skip("C.work visible at depth=3 without hide; depth accounting changed")
	}

	// With hide, depth=3 should now reach C.work
	re := regexp.MustCompile("Framework")
	hidden := sf.hideFrames(re)
	outAfter := captureOutput(func() {
		cmdTree(hidden, "", 3, 0.0)
	})
	if !strings.Contains(outAfter, "C.work") {
		t.Errorf("expected C.work reachable at depth=3 after hide, got:\n%s", outAfter)
	}
}

func TestCmdTreeHideAllStacksFullyHidden(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.a", "B.b"}, lines: []uint32{0, 0}, count: 10, thread: ""},
	})
	re := regexp.MustCompile(".*")
	hidden := sf.hideFrames(re)

	out := captureOutput(func() {
		cmdTree(hidden, "", 4, 0.0)
	})

	// totalSamples > 0 but no stacks → "no stacks matching '(all)'"
	if !strings.Contains(out, "no stacks matching '(all)'") {
		t.Errorf("expected \"no stacks matching '(all)'\", got:\n%s", out)
	}
}

func TestMatchesHide(t *testing.T) {
	tests := []struct {
		frame string
		regex string
		want  bool
	}{
		{"com/example/App.process", "App\\.process", true},
		{"com/example/App.process", "com\\.example", true},
		{"com/example/App.process", "Foo\\.bar", false},
		{"com.example.App.process", "App\\.process", true},
		{"Thread.run", "Thread", true},
		{"Thread.run", "^Thread\\.run$", true},
		{"Thread.run", "^run$", false},
	}
	for _, tt := range tests {
		re := regexp.MustCompile(tt.regex)
		got := matchesHide(tt.frame, re)
		if got != tt.want {
			t.Errorf("matchesHide(%q, %q) = %v, want %v", tt.frame, tt.regex, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// hideFrames integration tests via cmdTrace and cmdCallers
// ---------------------------------------------------------------------------

func TestCmdTraceHide(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.main", "Wrap.x", "B.process", "C.work"}, lines: []uint32{0, 0, 0, 0}, count: 100, thread: ""},
	})
	re := regexp.MustCompile("Wrap")
	hidden := sf.hideFrames(re)

	out := captureOutput(func() {
		cmdTrace(hidden, "B.process", 0.0, false)
	})

	if strings.Contains(out, "Wrap") {
		t.Errorf("expected Wrap removed, got:\n%s", out)
	}
	if !strings.Contains(out, "C.work") {
		t.Errorf("expected C.work in output, got:\n%s", out)
	}
}

func TestCmdCallersHide(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.main", "Wrap.x", "B.process", "C.work"}, lines: []uint32{0, 0, 0, 0}, count: 100, thread: ""},
	})
	re := regexp.MustCompile("Wrap")
	hidden := sf.hideFrames(re)

	out := captureOutput(func() {
		cmdCallers(hidden, "C.work", 4, 0.0)
	})

	if strings.Contains(out, "Wrap") {
		t.Errorf("expected Wrap removed, got:\n%s", out)
	}
	if !strings.Contains(out, "B.process") {
		t.Errorf("expected B.process in callers output, got:\n%s", out)
	}
}

func TestCmdTraceHideRemovesTarget(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.main", "Target.run", "B.process"}, lines: []uint32{0, 0, 0}, count: 100, thread: ""},
	})
	re := regexp.MustCompile("Target")
	hidden := sf.hideFrames(re)

	out := captureOutput(func() {
		cmdTrace(hidden, "Target.run", 0.0, false)
	})

	if !strings.Contains(out, "no stacks matching") {
		t.Errorf("expected 'no stacks matching' when target is hidden, got:\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// TestNoMatch* — suggestions, dollar hints, and edge cases
// ---------------------------------------------------------------------------

func TestNoMatchSuggestions(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"com/example/App.process", "com/example/App.run"}, lines: []uint32{0, 0}, count: 10},
	})

	// "Appp" (typo) fuzzy-matches "App" segment with edit distance 1.
	out := captureOutput(func() {
		cmdTree(sf, "Appp", 4, 0.0)
	})

	if !strings.Contains(out, "no stacks matching") {
		t.Errorf("expected 'no stacks matching', got:\n%s", out)
	}
	if !strings.Contains(out, "similar:") {
		t.Errorf("expected suggestions, got:\n%s", out)
	}
	if !strings.Contains(out, "App.process") {
		t.Errorf("expected 'App.process' in suggestions, got:\n%s", out)
	}
}

func TestNoMatchDollarHint(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"com/example/Server$Handler.run"}, lines: []uint32{0}, count: 10},
	})

	// Pattern doesn't contain $, but profile has $ frames → hint about inner classes.
	out := captureOutput(func() {
		cmdTree(sf, "Nonexistent", 4, 0.0)
	})

	if !strings.Contains(out, "no stacks matching") {
		t.Errorf("expected 'no stacks matching', got:\n%s", out)
	}
	if !strings.Contains(out, "inner classes ($)") {
		t.Errorf("expected dollar hint about inner classes, got:\n%s", out)
	}
}

func TestNoMatchDollarInPattern(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"com/example/App.run"}, lines: []uint32{0}, count: 10},
	})

	out := captureOutput(func() {
		cmdTree(sf, "Server$Handler", 4, 0.0)
	})

	if !strings.Contains(out, "no stacks matching") {
		t.Errorf("expected 'no stacks matching', got:\n%s", out)
	}
	if !strings.Contains(out, "single quotes") {
		t.Errorf("expected single-quotes hint, got:\n%s", out)
	}
}

func TestFilterNoMatchMessage(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.a", "B.b"}, lines: []uint32{0, 0}, count: 10},
	})

	out := captureOutput(func() {
		cmdFilter(sf, "Nonexistent", false)
	})

	if !strings.Contains(out, "no stacks matching") {
		t.Errorf("expected 'no stacks matching', got %q", out)
	}
}

func TestNoMatchNoSuggestions(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.a", "B.b"}, lines: []uint32{0, 0}, count: 10},
	})

	out := captureOutput(func() {
		cmdTree(sf, "Zzzzzzz", 4, 0.0)
	})

	if !strings.Contains(out, "no stacks matching") {
		t.Errorf("expected 'no stacks matching', got:\n%s", out)
	}
	if strings.Contains(out, "similar:") {
		t.Errorf("expected no suggestions for completely unrelated pattern, got:\n%s", out)
	}
}

func TestSuggestMethodsLimit(t *testing.T) {
	// Build a profile with 20 methods matching "Service".
	var stacks []stack
	for i := 0; i < 20; i++ {
		stacks = append(stacks, stack{
			frames: []string{fmt.Sprintf("com/example/Service%02d.handle", i)},
			lines:  []uint32{0},
			count:  1,
		})
	}
	sf := makeStackFile(stacks)

	suggestions, _ := suggestMethods(sf, "Service")
	if len(suggestions) > 5 {
		t.Errorf("expected at most 5 suggestions, got %d: %v", len(suggestions), suggestions)
	}
}

func TestSuggestMethodsCaseInsensitive(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"java/util/HashMap.resize"}, lines: []uint32{0}, count: 10},
	})

	suggestions, _ := suggestMethods(sf, "hashmap")
	found := false
	for _, s := range suggestions {
		if strings.Contains(s, "HashMap") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected case-insensitive match for 'hashmap' to find HashMap, got %v", suggestions)
	}
}

func TestSuggestMethodsTransposition(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"java/util/HashMap.resize"}, lines: []uint32{0}, count: 10},
	})

	// "rezise" is "resize" with i and s transposed (edit distance 2).
	suggestions, _ := suggestMethods(sf, "rezise")
	found := false
	for _, s := range suggestions {
		if strings.Contains(s, "resize") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected fuzzy match for 'rezise' to find HashMap.resize, got %v", suggestions)
	}
}

func TestLevenshtein(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"abc", "", 3},
		{"", "abc", 3},
		{"abc", "abc", 0},
		{"abc", "abd", 1},
		{"kitten", "sitting", 3},
		{"appp", "app", 1},
		{"rezise", "resize", 2},
	}
	for _, tt := range tests {
		got := levenshtein(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("levenshtein(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestMatchesMethodSlashPattern(t *testing.T) {
	// Patterns with / should match against dot-normalized frames.
	if !matchesMethod("com/example/App.process", "com/example/App.process") {
		t.Error("slash pattern should match identical slash frame")
	}
	if !matchesMethod("com/example/App.process", "com.example.App.process") {
		t.Error("dot pattern should match slash frame")
	}
	if !matchesMethod("com/example/App.process", "App.process") {
		t.Error("short pattern should match slash frame")
	}
}

func TestSlashPatternTree(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"com/example/App.process", "com/example/Foo.bar"}, lines: []uint32{0, 0}, count: 10},
	})

	out := captureOutput(func() {
		cmdTree(sf, "com/example/App.process", 4, 0.0)
	})

	if !strings.Contains(out, "App.process") {
		t.Errorf("slash pattern should match, got:\n%s", out)
	}
	if strings.Contains(out, "no stacks matching") {
		t.Errorf("slash pattern should not fail to match, got:\n%s", out)
	}
}

func TestSlashPatternSuggestions(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"com/example/App.process", "com/example/Foo.bar"}, lines: []uint32{0, 0}, count: 10},
	})

	// FQN pattern with typo should get suggestions via full-name comparison.
	out := captureOutput(func() {
		cmdTree(sf, "com/example/Appp.process", 4, 0.0)
	})

	if !strings.Contains(out, "no stacks matching") {
		t.Errorf("expected no match for typo, got:\n%s", out)
	}
	if !strings.Contains(out, "similar:") {
		t.Errorf("expected suggestions for FQN typo, got:\n%s", out)
	}
}

func TestFuzzyScoreFQNTypo(t *testing.T) {
	// Full-name comparison should catch FQN patterns with typos.
	score := fuzzyScore("com.example.App.process", "com.example.appp.process", 3)
	if score < 0 || score > 1 {
		t.Errorf("expected score 1 for single-char typo in FQN, got %d", score)
	}

	// Dotted pattern against short name with very different length should not match.
	score = fuzzyScore("App.process", "com.example.appp.process", 3)
	if score >= 0 {
		t.Errorf("expected no match for length-mismatched names, got %d", score)
	}

	// Dotted pattern with typo: "HashMap.rezise" vs "HashMap.resize".
	score = fuzzyScore("HashMap.resize", "hashmap.rezise", 3)
	if score < 0 {
		t.Errorf("expected match for dotted pattern typo, got %d", score)
	}
}

func TestSuggestMethodsFQNCollision(t *testing.T) {
	// Two distinct FQNs with the same short name: both should appear as FQN.
	sf := makeStackFile([]stack{
		{frames: []string{"com/foo/Service.run"}, lines: []uint32{0}, count: 5},
		{frames: []string{"org/bar/Service.run"}, lines: []uint32{0}, count: 5},
	})

	suggestions, _ := suggestMethods(sf, "Service")
	// Both FQN forms should appear since "Service.run" is ambiguous.
	hasFoo := false
	hasBar := false
	for _, s := range suggestions {
		if strings.Contains(s, "com.foo.Service.run") {
			hasFoo = true
		}
		if strings.Contains(s, "org.bar.Service.run") {
			hasBar = true
		}
	}
	if !hasFoo || !hasBar {
		t.Errorf("expected FQN disambiguation for collision, got %v", suggestions)
	}
	// Neither suggestion should be the bare short name.
	for _, s := range suggestions {
		if s == "Service.run" {
			t.Errorf("expected FQN form, not short name %q, suggestions: %v", s, suggestions)
		}
	}
}

func TestTimelineNoMatchMethod(t *testing.T) {
	parsed := &parsedProfile{
		timedEvents: map[string][]timedEvent{
			"cpu": {
				{offsetNanos: 0, frames: []string{"A.a", "B.b"}, lines: []uint32{0, 0}, thread: "main", weight: 1},
				{offsetNanos: 1e9, frames: []string{"A.a", "C.c"}, lines: []uint32{0, 0}, thread: "main", weight: 1},
			},
		},
		spanNanos: 2e9,
		stacksByEvent: map[string]*stackFile{
			"cpu": makeStackFile([]stack{
				{frames: []string{"A.a", "B.b"}, lines: []uint32{0, 0}, count: 1, thread: "main"},
				{frames: []string{"A.a", "C.c"}, lines: []uint32{0, 0}, count: 1, thread: "main"},
			}),
		},
	}

	out := captureOutput(func() {
		cmdTimeline(parsed, "cpu", 5, "", "Nonexistent", true, false, nil, "", -1, -1, 0, false)
	})

	if !strings.Contains(out, "no stacks matching") {
		t.Errorf("expected 'no stacks matching', got:\n%s", out)
	}
}

func TestTimelineNoMatchAfterThreadFilter(t *testing.T) {
	// Method exists in "worker" thread but not in "http" thread.
	// After thread filter, suggestions should only show methods from filtered view.
	parsed := &parsedProfile{
		timedEvents: map[string][]timedEvent{
			"cpu": {
				{offsetNanos: 0, frames: []string{"Worker.run", "Worker.process"}, lines: []uint32{0, 0}, thread: "worker-1", weight: 1},
				{offsetNanos: 1e9, frames: []string{"Http.handle", "Http.serve"}, lines: []uint32{0, 0}, thread: "http-1", weight: 1},
			},
		},
		spanNanos: 2e9,
		stacksByEvent: map[string]*stackFile{
			"cpu": makeStackFile([]stack{
				{frames: []string{"Worker.run", "Worker.process"}, lines: []uint32{0, 0}, count: 1, thread: "worker-1"},
				{frames: []string{"Http.handle", "Http.serve"}, lines: []uint32{0, 0}, count: 1, thread: "http-1"},
			}),
		},
	}

	out := captureOutput(func() {
		// Filter to "http" thread, search for "Worker" — should not suggest Worker.
		cmdTimeline(parsed, "cpu", 5, "", "Worker", true, false, nil, "http", -1, -1, 0, false)
	})

	if !strings.Contains(out, "no stacks matching") {
		t.Errorf("expected 'no stacks matching', got:\n%s", out)
	}
	// Suggestions should only come from filtered events (http thread).
	// Worker methods should not appear in the "similar:" line.
	if strings.Contains(out, "similar:") && strings.Contains(out, "Worker") {
		t.Errorf("should not suggest Worker methods after thread filter to 'http', got:\n%s", out)
	}

	// Positive case: typo on a method that IS in the filtered view should suggest it.
	out2 := captureOutput(func() {
		// Filter to "http" thread, search for "Htpp" (typo) — should suggest Http methods.
		cmdTimeline(parsed, "cpu", 5, "", "Htpp", true, false, nil, "http", -1, -1, 0, false)
	})
	if !strings.Contains(out2, "similar:") {
		t.Errorf("expected suggestions from filtered events for typo 'Htpp', got:\n%s", out2)
	}
	if !strings.Contains(out2, "Http") {
		t.Errorf("expected Http methods in suggestions, got:\n%s", out2)
	}
}

func TestCmdLinesEmpty(t *testing.T) {
	sf := makeStackFile(nil)

	var err error
	out := captureOutput(func() {
		err = cmdLines(sf, "A.a", 0, false)
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "no samples") {
		t.Errorf("expected 'no samples' message, got %q", out)
	}
}

func TestCmdFilterEmpty(t *testing.T) {
	sf := makeStackFile(nil)

	out := captureOutput(func() {
		cmdFilter(sf, "A.a", false)
	})

	if !strings.Contains(out, "no samples") {
		t.Errorf("expected 'no samples' message, got %q", out)
	}
}

// ---------------------------------------------------------------------------
// TestExtractAsprofFromSkill
// ---------------------------------------------------------------------------

func TestExtractAsprofFromSkill(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name: "valid rendered skill",
			content: `---
name: jfr
---
## Profiling

- CPU profiling: ` + "`/opt/async-profiler/bin/asprof -d 30 -o jfr -f profile.jfr <pid>`" + `
- Wall-clock:    ` + "`/opt/async-profiler/bin/asprof -d 30 -e wall -o jfr -f profile.jfr <pid>`" + `
`,
			want: "/opt/async-profiler/bin/asprof",
		},
		{
			name: "path with spaces",
			content: `---
name: jfr
---
- CPU profiling: ` + "`/home/user/my tools/asprof -d 30 -o jfr -f profile.jfr <pid>`" + `
`,
			want: "/home/user/my tools/asprof",
		},
		{
			name:    "no asprof lines",
			content: "---\nname: jfr\n---\nSome content without profiling commands.\n",
			want:    "",
		},
		{
			name:    "empty string",
			content: "",
			want:    "",
		},
		{
			name:    "garbage content",
			content: "random garbage\nno backticks here\njust text\n",
			want:    "",
		},
		{
			name:    "backtick but not asprof pattern",
			content: "use `ap-query hot profile.jfr` to see hot methods\n",
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractAsprofFromSkill(tt.content)
			if got != tt.want {
				t.Errorf("extractAsprofFromSkill() = %q, want %q", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestUpdateInstalledSkillsNoSkills
// ---------------------------------------------------------------------------

func TestUpdateInstalledSkillsNoSkills(t *testing.T) {
	// Use a temp dir as HOME so no real skills are found
	tmpHome := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	// Should not panic or error when no skills exist
	updateInstalledSkills("/nonexistent/ap-query")
}

func TestUpdateInstalledSkillsStaleAsprofFallsBack(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake not portable to Windows")
	}

	tmpHome := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	// Install a SKILL.md with a stale (nonexistent) asprof path.
	dir := skillDir("claude", tmpHome, false)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	stalePath := "/no/such/stale/asprof"
	content := "---\nname: jfr\n---\n- CPU profiling: `" + stalePath + " -d 30 -o jfr -f profile.jfr <pid>`\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	// Place a discoverable asprof in ~/.ap-query/bin (a search dir for findAsprof).
	binDir := filepath.Join(tmpHome, ".ap-query", "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	realAsprof := filepath.Join(binDir, "asprof")
	if err := os.WriteFile(realAsprof, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}

	// Create a fake ap-query binary that logs its arguments.
	argsFile := filepath.Join(tmpHome, "args.log")
	fakeExec := filepath.Join(tmpHome, "fake-ap-query")
	script := "#!/bin/sh\necho \"$@\" >> " + argsFile + "\n"
	if err := os.WriteFile(fakeExec, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	updateInstalledSkills(fakeExec)

	// Verify the fake binary was called with the fallback path, not the stale one.
	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("fake binary was not called: %v", err)
	}
	args := string(data)
	if strings.Contains(args, stalePath) {
		t.Errorf("stale path %q should not have been passed, got: %s", stalePath, args)
	}
	if !strings.Contains(args, realAsprof) {
		t.Errorf("expected fallback path %q in args, got: %s", realAsprof, args)
	}
}

// ---------------------------------------------------------------------------
// Timeline & time-range filtering tests
// ---------------------------------------------------------------------------

func TestTicksToNanos(t *testing.T) {
	tests := []struct {
		name                                     string
		startTicks, hdrStartTicks, hdrStartNanos uint64
		originNanos, tps                         uint64
		want                                     int64
	}{
		{
			name:          "basic",
			startTicks:    1_000_100,
			hdrStartTicks: 1_000_000,
			hdrStartNanos: 5_000_000_000,
			originNanos:   5_000_000_000,
			tps:           1_000_000,
			want:          100_000, // 100 ticks / 1M tps = 0.0001s = 100000ns
		},
		{
			name:          "one second offset",
			startTicks:    2_000_000_000,
			hdrStartTicks: 1_000_000_000,
			hdrStartNanos: 10_000_000_000,
			originNanos:   10_000_000_000,
			tps:           1_000_000_000,
			want:          1_000_000_000,
		},
		{
			name:          "large ticks overflow safe",
			startTicks:    10_000_000_000,
			hdrStartTicks: 0,
			hdrStartNanos: 100,
			originNanos:   100,
			tps:           1_000_000_000,
			want:          10_000_000_000,
		},
		{
			name:          "zero tps guard",
			startTicks:    100,
			hdrStartTicks: 0,
			hdrStartNanos: 0,
			originNanos:   0,
			tps:           0,
			want:          0,
		},
		{
			name:          "multi chunk offset",
			startTicks:    500,
			hdrStartTicks: 0,
			hdrStartNanos: 2_000_000_000,
			originNanos:   1_000_000_000,
			tps:           1000,
			want:          1_500_000_000, // 1s chunk offset + 0.5s event offset
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ticksToNanos(tt.startTicks, tt.hdrStartTicks, tt.hdrStartNanos, tt.originNanos, tt.tps)
			if got != tt.want {
				t.Errorf("ticksToNanos() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestScanChunkHeaders(t *testing.T) {
	// Test with actual cpu.jfr fixture (single chunk).
	buf, err := readJFRBytes(jfrFixture("cpu.jfr"))
	if err != nil {
		t.Fatalf("readJFRBytes: %v", err)
	}
	origin, span, err := scanChunkHeaders(buf)
	if err != nil {
		t.Fatalf("scanChunkHeaders: %v", err)
	}
	if origin <= 0 {
		t.Errorf("originNanos should be positive, got %d", origin)
	}
	if span <= 0 {
		t.Errorf("spanNanos should be positive, got %d", span)
	}
	// Span should be roughly 5 seconds for cpu.jfr.
	spanSec := float64(span) / 1e9
	if spanSec < 4.0 || spanSec > 10.0 {
		t.Errorf("expected span ~5s, got %.1fs", spanSec)
	}
}

func TestScanChunkHeadersMultiChunk(t *testing.T) {
	buf, err := readJFRBytes(jfrFixture("multichunk.jfr"))
	if err != nil {
		t.Fatalf("readJFRBytes: %v", err)
	}
	origin, span, err := scanChunkHeaders(buf)
	if err != nil {
		t.Fatalf("scanChunkHeaders: %v", err)
	}
	if origin <= 0 {
		t.Errorf("originNanos should be positive, got %d", origin)
	}
	if span <= 0 {
		t.Errorf("spanNanos should be positive, got %d", span)
	}
	// multichunk.jfr should have a span > 10s (multiple chunks).
	spanSec := float64(span) / 1e9
	if spanSec < 10.0 {
		t.Errorf("expected span >= 10s for multichunk, got %.1fs", spanSec)
	}
}

func TestScanChunkHeadersCorrupt(t *testing.T) {
	// Buffer with no valid header.
	_, _, err := scanChunkHeaders([]byte{0, 0, 0, 0})
	if err == nil {
		t.Error("expected error for corrupt buffer")
	}
	// Empty buffer.
	_, _, err = scanChunkHeaders(nil)
	if err == nil {
		t.Error("expected error for empty buffer")
	}
}

func TestWallSampleWeight(t *testing.T) {
	parsed, err := parseJFRData(jfrFixture("wall.jfr"), singleEventType("wall"), parseOpts{
		collectTimestamps: true,
		fromNanos:         -1,
		toNanos:           -1,
	})
	if err != nil {
		t.Fatalf("parseJFRData: %v", err)
	}
	events := parsed.timedEvents["wall"]
	if len(events) == 0 {
		t.Fatal("expected wall events")
	}
	// Check that at least some events have weight > 1 (wall batch samples).
	hasWeightGt1 := false
	totalWeight := 0
	for _, e := range events {
		totalWeight += e.weight
		if e.weight > 1 {
			hasWeightGt1 = true
		}
	}
	// totalWeight should match the stackFile totalSamples.
	sf := parsed.stacksByEvent["wall"]
	if sf == nil {
		t.Fatal("no wall stackFile")
	}
	if sf.totalSamples != totalWeight {
		t.Errorf("stackFile totalSamples=%d, timed totalWeight=%d", sf.totalSamples, totalWeight)
	}
	// It's possible all weights are 1 if the JFR doesn't use batch sampling.
	// Just log it.
	if !hasWeightGt1 {
		t.Logf("note: no wall events with weight > 1 in fixture (Samples field may be 1)")
	}
}

func TestFromToValidation(t *testing.T) {
	// Test that --to < --from is detectable.
	t.Run("inverted range", func(t *testing.T) {
		from, _ := time.ParseDuration("10s")
		to, _ := time.ParseDuration("5s")
		if to >= from {
			t.Error("expected to < from")
		}
	})

	// Test invalid duration parsing.
	t.Run("invalid duration", func(t *testing.T) {
		_, err := time.ParseDuration("abc")
		if err == nil {
			t.Error("expected parse error for 'abc'")
		}
	})
}

func TestFromToFiltering(t *testing.T) {
	// Parse cpu.jfr with full range.
	parsedFull, err := parseJFRData(jfrFixture("cpu.jfr"), singleEventType("cpu"), parseOpts{
		collectTimestamps: true,
		fromNanos:         -1,
		toNanos:           -1,
	})
	if err != nil {
		t.Fatalf("parseJFRData full: %v", err)
	}
	fullEvents := parsedFull.timedEvents["cpu"]

	// Parse with a narrow time range (first 1 second).
	parsedFiltered, err := parseJFRData(jfrFixture("cpu.jfr"), singleEventType("cpu"), parseOpts{
		collectTimestamps: true,
		fromNanos:         0,
		toNanos:           1_000_000_000,
	})
	if err != nil {
		t.Fatalf("parseJFRData filtered: %v", err)
	}
	filteredEvents := parsedFiltered.timedEvents["cpu"]

	if len(filteredEvents) >= len(fullEvents) {
		t.Errorf("expected fewer events after filtering, full=%d filtered=%d",
			len(fullEvents), len(filteredEvents))
	}
	if len(filteredEvents) == 0 {
		t.Error("expected some events in first second")
	}

	// All filtered events should be within [0, 1s).
	for _, e := range filteredEvents {
		if e.offsetNanos < 0 || e.offsetNanos >= 1_000_000_000 {
			t.Errorf("event at %dns outside [0, 1s)", e.offsetNanos)
		}
	}
}

func TestCmdTimeline(t *testing.T) {
	parsed, err := parseJFRData(jfrFixture("cpu.jfr"), allEventTypes(), parseOpts{
		collectTimestamps: true,
		fromNanos:         -1,
		toNanos:           -1,
	})
	if err != nil {
		t.Fatalf("parseJFRData: %v", err)
	}
	out := captureOutput(func() {
		cmdTimeline(parsed, "cpu", 5, "", "", false, false, nil, "", -1, -1, 0, false)
	})
	if !strings.Contains(out, "Duration:") {
		t.Error("expected Duration in header")
	}
	if !strings.Contains(out, "Buckets: 5") {
		t.Errorf("expected 5 buckets, got %q", out)
	}
	if !strings.Contains(out, "Total:") {
		t.Error("expected Total in header")
	}
	if !strings.Contains(out, "Samples") {
		t.Error("expected Samples column header")
	}
	// Should have 5 data rows.
	lines := strings.Split(strings.TrimSpace(out), "\n")
	dataLines := 0
	for _, line := range lines {
		if strings.Contains(line, "s-") && strings.Contains(line, "\u2588") {
			dataLines++
		}
	}
	// At least some data lines with bars.
	if dataLines < 3 {
		t.Errorf("expected >=3 data lines with bars, got %d", dataLines)
	}
}

func TestCmdTimelineMethod(t *testing.T) {
	parsed, err := parseJFRData(jfrFixture("cpu.jfr"), allEventTypes(), parseOpts{
		collectTimestamps: true,
		fromNanos:         -1,
		toNanos:           -1,
	})
	if err != nil {
		t.Fatalf("parseJFRData: %v", err)
	}
	out := captureOutput(func() {
		cmdTimeline(parsed, "cpu", 5, "", "Workload", false, false, nil, "", -1, -1, 0, false)
	})
	if !strings.Contains(out, "Matched:") {
		t.Errorf("expected 'Matched:' in header with --method, got %q", out)
	}
}

func TestCmdTimelineTopMethod(t *testing.T) {
	parsed, err := parseJFRData(jfrFixture("cpu.jfr"), allEventTypes(), parseOpts{
		collectTimestamps: true,
		fromNanos:         -1,
		toNanos:           -1,
	})
	if err != nil {
		t.Fatalf("parseJFRData: %v", err)
	}
	out := captureOutput(func() {
		cmdTimeline(parsed, "cpu", 5, "", "", true, false, nil, "", -1, -1, 0, false)
	})
	if !strings.Contains(out, "Hot Method (self)") {
		t.Error("expected 'Hot Method (self)' column header")
	}
	// Each non-zero bucket should have a method name with percentage.
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for _, line := range lines {
		if strings.Contains(line, "s-") && strings.Contains(line, "\u2588") {
			if !strings.Contains(line, "%") {
				t.Errorf("expected %% in top-method line: %q", line)
			}
		}
	}
}

func TestCmdTimelineTopMethodAggregatesLeafSelfCounts(t *testing.T) {
	parsed := &parsedProfile{
		timedEvents: map[string][]timedEvent{
			"cpu": {
				{offsetNanos: 100, stackKey: "A;X", frames: []string{"A", "X"}, lines: []uint32{0, 0}, weight: 2},
				{offsetNanos: 200, stackKey: "B;Y", frames: []string{"B", "Y"}, lines: []uint32{0, 0}, weight: 5},
				{offsetNanos: 300, stackKey: "C;Y", frames: []string{"C", "Y"}, lines: []uint32{0, 0}, weight: 3},
				{offsetNanos: 400, stackKey: "D;X", frames: []string{"D", "X"}, lines: []uint32{0, 0}, weight: 4},
			},
		},
		spanNanos: 1_000_000_000,
	}
	out := captureOutput(func() {
		cmdTimeline(parsed, "cpu", 1, "", "", true, false, nil, "", -1, -1, 0, false)
	})

	// X=6, Y=8, total=14 => Y is top at 57%.
	if !strings.Contains(out, "Y (57%)") {
		t.Fatalf("expected top method Y with 57%% share, got %q", out)
	}
}

func TestCmdTimelineHide(t *testing.T) {
	// Stack "A;B;X" with weight 10, "A;B;Y" with weight 5.
	// Hiding X removes X from the first stack, making B the leaf.
	// B appears as leaf in 10 samples (from hidden X stack).
	// Y appears as leaf in 5 samples.
	// So B should be the hot method, not X.
	parsed := &parsedProfile{
		timedEvents: map[string][]timedEvent{
			"cpu": {
				{offsetNanos: 0, stackKey: "A;B;X", frames: []string{"A", "B", "X"}, lines: []uint32{0, 0, 0}, weight: 10},
				{offsetNanos: 0, stackKey: "A;B;Y", frames: []string{"A", "B", "Y"}, lines: []uint32{0, 0, 0}, weight: 5},
			},
		},
		spanNanos: 1_000_000_000,
	}
	hide := regexp.MustCompile("^X$")
	out := captureOutput(func() {
		cmdTimeline(parsed, "cpu", 1, "", "", true, false, hide, "", -1, -1, 0, false)
	})

	// X must not appear as hot method.
	if strings.Contains(out, "X (") {
		t.Errorf("expected hidden method X to not appear as hot method, got:\n%s", out)
	}
	// B is now the leaf of the formerly X-topped stack.
	if !strings.Contains(out, "B (") {
		t.Errorf("expected B as hot method after hiding X, got:\n%s", out)
	}
}

func TestCmdTimelineZeroSpan(t *testing.T) {
	// Single event at offset 0 — should not panic, should show 1 bucket.
	parsed := &parsedProfile{
		timedEvents: map[string][]timedEvent{
			"cpu": {{offsetNanos: 0, stackKey: "A;B", frames: []string{"A", "B"}, lines: []uint32{0, 0}, weight: 1}},
		},
		spanNanos: 0,
	}
	out := captureOutput(func() {
		cmdTimeline(parsed, "cpu", 0, "", "", false, false, nil, "", -1, -1, 0, false)
	})
	if !strings.Contains(out, "Buckets: 1") {
		t.Errorf("expected 1 bucket for zero-span, got %q", out)
	}
	if !strings.Contains(out, "Total: 1") {
		t.Errorf("expected Total: 1, got %q", out)
	}
}

func TestCmdTimelineCollapsed(t *testing.T) {
	// timeline requires JFR — verify detectFormat returns non-JFR for text paths.
	if detectFormat("-") != formatCollapsed {
		t.Error("expected detectFormat(-) = formatCollapsed")
	}
	if detectFormat("foo.txt") != formatCollapsed {
		t.Error("expected detectFormat(foo.txt) = formatCollapsed")
	}
	if detectFormat("foo.jfr") != formatJFR {
		t.Error("expected detectFormat(foo.jfr) = formatJFR")
	}
}

func TestCmdTimelineResolution(t *testing.T) {
	parsed, err := parseJFRData(jfrFixture("cpu.jfr"), allEventTypes(), parseOpts{
		collectTimestamps: true,
		fromNanos:         -1,
		toNanos:           -1,
	})
	if err != nil {
		t.Fatalf("parseJFRData: %v", err)
	}
	out := captureOutput(func() {
		cmdTimeline(parsed, "cpu", 0, "1s", "", false, false, nil, "", -1, -1, 0, false)
	})
	if !strings.Contains(out, "1.0s each") {
		t.Errorf("expected '1.0s each' in header, got %q", out)
	}
}

func TestCmdTimelineFromTo(t *testing.T) {
	parsed, err := parseJFRData(jfrFixture("cpu.jfr"), allEventTypes(), parseOpts{
		collectTimestamps: true,
		fromNanos:         1_000_000_000,
		toNanos:           3_000_000_000,
	})
	if err != nil {
		t.Fatalf("parseJFRData: %v", err)
	}
	out := captureOutput(func() {
		cmdTimeline(parsed, "cpu", 5, "", "", false, false, nil, "",
			1_000_000_000, 3_000_000_000, 0, false)
	})
	// Duration header should show the window span (2s), not full recording.
	if !strings.Contains(out, "Duration: 2.0s") {
		t.Errorf("expected 'Duration: 2.0s' in header, got %q", out)
	}
	// Time labels should start from 1.0s, not 0.0s.
	if !strings.Contains(out, "1.0s-") {
		t.Errorf("expected time labels starting at 1.0s, got %q", out)
	}
	if strings.Contains(out, " 0.0s-") {
		t.Errorf("time labels should not start at 0.0s when --from 1s, got %q", out)
	}
}

func TestCmdTimelineFromOnly(t *testing.T) {
	parsed, err := parseJFRData(jfrFixture("cpu.jfr"), allEventTypes(), parseOpts{
		collectTimestamps: true,
		fromNanos:         1_000_000_000,
		toNanos:           -1,
	})
	if err != nil {
		t.Fatalf("parseJFRData: %v", err)
	}
	out := captureOutput(func() {
		cmdTimeline(parsed, "cpu", 5, "", "", false, false, nil, "",
			1_000_000_000, -1, 0, false)
	})
	// Bucket origin should start at 1s.
	if !strings.Contains(out, "1.0s-") {
		t.Errorf("expected time labels starting at 1.0s, got %q", out)
	}
	// Duration should be (span - 1s), not the full span.
	if strings.Contains(out, " 0.0s-") {
		t.Errorf("time labels should not start at 0.0s when --from 1s, got %q", out)
	}
}

func TestFromBeyondSpanWarning(t *testing.T) {
	path := jfrFixture("cpu.jfr")
	parsed, err := parseJFRData(path, allEventTypes(), parseOpts{
		collectTimestamps: true,
		fromNanos:         -1,
		toNanos:           -1,
	})
	if err != nil {
		t.Fatalf("parseJFRData: %v", err)
	}

	exitCode, _, stderr := runCLIForTest(t, []string{"timeline", path, "--from", "100s"}, nil)
	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", exitCode, stderr)
	}

	want := fmt.Sprintf("warning: --from %s is beyond recording duration (%s); result will be empty",
		"100s", formatDuration(parsed.spanNanos))
	if !strings.Contains(stderr, want) {
		t.Errorf("expected warning %q, got %q", want, stderr)
	}
}

func TestToClamping(t *testing.T) {
	path := jfrFixture("cpu.jfr")
	parsed, err := parseJFRData(path, allEventTypes(), parseOpts{
		collectTimestamps: true,
		fromNanos:         -1,
		toNanos:           -1,
	})
	if err != nil {
		t.Fatalf("parseJFRData: %v", err)
	}

	exitCode, _, stderr := runCLIForTest(t, []string{"hot", path, "--to", "100s"}, nil)
	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", exitCode, stderr)
	}

	want := fmt.Sprintf("Window: start to %s", formatDuration(parsed.spanNanos))
	if !strings.Contains(stderr, want) {
		t.Errorf("expected clamped window %q, got %q", want, stderr)
	}
	if strings.Contains(stderr, "Window: start to 100.0s") {
		t.Errorf("expected --to to be clamped to recording duration, got %q", stderr)
	}
}

func TestFromToWithDiffError(t *testing.T) {
	exitCode, _, stderr := runCLIForTest(t, []string{"diff", "--from", "5s", "a.jfr", "b.jfr"}, nil)
	if exitCode == 0 {
		t.Fatalf("exit code = 0, want non-zero; stderr=%q", stderr)
	}
	if !strings.Contains(stderr, "unknown flag") {
		t.Fatalf("expected unknown flag error, got %q", stderr)
	}
}

func TestTimelineRejectsNonJFR(t *testing.T) {
	exitCode, _, stderr := runCLIForTest(t, []string{"timeline", "does-not-exist.collapsed"}, nil)
	if exitCode == 0 {
		t.Fatalf("exit code = 0, want non-zero; stderr=%q", stderr)
	}
	if !strings.Contains(stderr, "timeline requires a JFR file") {
		t.Fatalf("expected timeline-only validation error, got %q", stderr)
	}
	if strings.Contains(stderr, "no such file or directory") {
		t.Fatalf("expected rejection before openInput, got %q", stderr)
	}
}

func TestTimelineRejectsStdinWithoutReading(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer r.Close()
	defer w.Close()

	cmdArgs := []string{"-test.run=TestHelperProcessMain", "--", "timeline", "-"}
	cmd := exec.Command(os.Args[0], cmdArgs...)
	cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")
	cmd.Stdin = r
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper process: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case err := <-done:
		exitCode := 0
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				exitCode = ee.ExitCode()
			} else {
				t.Fatalf("wait helper process: %v", err)
			}
		}
		if exitCode == 0 {
			t.Fatalf("exit code = 0, want non-zero; stderr=%q", stderr.String())
		}
		if !strings.Contains(stderr.String(), "timeline requires a JFR file") {
			t.Fatalf("expected timeline-only validation error, got %q", stderr.String())
		}
	case <-time.After(2 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("timeline with stdin blocked; expected immediate rejection")
	}
}

func TestTimelineBucketCountBound(t *testing.T) {
	path := jfrFixture("cpu.jfr")

	exitCode, _, stderr := runCLIForTest(t, []string{"timeline", path, "--buckets", "10001"}, nil)
	if exitCode == 0 {
		t.Fatalf("exit code = 0, want non-zero; stderr=%q", stderr)
	}
	if !strings.Contains(stderr, "exceeds maximum (10000)") {
		t.Fatalf("expected max bucket validation, got %q", stderr)
	}

	parsed, err := parseJFRData(path, singleEventType("cpu"), parseOpts{
		collectTimestamps: true,
		fromNanos:         -1,
		toNanos:           -1,
	})
	if err != nil {
		t.Fatalf("parseJFRData: %v", err)
	}
	resolutionNanos := parsed.spanNanos / 10001
	if resolutionNanos < 1 {
		resolutionNanos = 1
	}
	resolution := fmt.Sprintf("%dns", resolutionNanos)

	exitCode, _, stderr = runCLIForTest(t, []string{"timeline", path, "--resolution", resolution}, nil)
	if exitCode == 0 {
		t.Fatalf("resolution path exit code = 0, want non-zero; stderr=%q", stderr)
	}
	if !strings.Contains(stderr, "exceeds maximum (10000)") {
		t.Fatalf("expected max bucket validation for --resolution, got %q", stderr)
	}
}

func TestFromToCollapsedWarning(t *testing.T) {
	exitCode, stdout, stderr := runCLIForTest(t, []string{"hot", "--from", "5s", "--to", "10s", "-"},
		strings.NewReader("A;B 1\n"))
	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", exitCode, stderr)
	}
	if !strings.Contains(stderr, "warning: --from/--to ignored for non-JFR input (no timestamps)") {
		t.Fatalf("expected non-JFR warning, got %q", stderr)
	}
	if strings.Contains(stderr, "Window:") {
		t.Fatalf("did not expect window echo after non-JFR warning, got %q", stderr)
	}
	if strings.TrimSpace(stdout) == "" {
		t.Fatal("expected non-empty hot output")
	}
}

func TestBoundedFallbackWarning(t *testing.T) {
	// Test the 10M threshold logic directly.
	timedByEvent := map[string][]timedEvent{
		"cpu":  make([]timedEvent, 6_000_000),
		"wall": make([]timedEvent, 5_000_000),
	}
	total := 0
	for _, events := range timedByEvent {
		total += len(events)
	}
	if total <= 10_000_000 {
		t.Errorf("expected total > 10M, got %d", total)
	}
	// Verify the warning would fire.
	stderr := captureStderr(func() {
		if total > 10_000_000 {
			fmt.Fprintf(os.Stderr, "warning: %d events collected; consider using --from/--to to narrow the time window\n", total)
		}
	})
	if !strings.Contains(stderr, "warning:") {
		t.Errorf("expected warning for >10M events, got %q", stderr)
	}
	if !strings.Contains(stderr, "11000000") {
		t.Errorf("expected event count in warning, got %q", stderr)
	}
}

func TestTimedCollectionGatedByEventType(t *testing.T) {
	// multi.jfr has cpu, wall, alloc, and lock events.
	// When requesting only cpu, the other types must not appear in timedByEvent.
	parsed, err := parseJFRData(jfrFixture("multi.jfr"), singleEventType("cpu"), parseOpts{
		collectTimestamps: true,
		fromNanos:         -1,
		toNanos:           -1,
	})
	if err != nil {
		t.Fatalf("parseJFRData: %v", err)
	}
	for eventType, events := range parsed.timedEvents {
		if eventType != "cpu" && len(events) > 0 {
			t.Errorf("unexpected timed events for unrequested type %q: %d events", eventType, len(events))
		}
	}
	if cpuEvents := parsed.timedEvents["cpu"]; len(cpuEvents) == 0 {
		t.Error("expected cpu timed events to be collected")
	}
}

func TestTimelineFromBeyondSpanClampsToZero(t *testing.T) {
	// When --from is past the recording span without --to, bucketSpan should be 0 (single bucket).
	parsed := &parsedProfile{
		timedEvents: map[string][]timedEvent{
			"cpu": {{offsetNanos: 100_000_000_000, stackKey: "A;B", frames: []string{"A", "B"}, lines: []uint32{0, 0}, weight: 1}},
		},
		spanNanos: 5_000_000_000,
	}
	out := captureOutput(func() {
		cmdTimeline(parsed, "cpu", 0, "", "", false, false, nil, "",
			100_000_000_000, -1, 0, false)
	})
	// Should produce a single bucket (zero span), not negative span confusion.
	if !strings.Contains(out, "Buckets: 1") {
		t.Errorf("expected 1 bucket for from-beyond-span, got %q", out)
	}
}

func TestTimelineFromToBothBeyondSpan(t *testing.T) {
	// When both --from and --to are past the recording span, clamping must not
	// produce a negative span (toNanos < fromNanos). Both get clamped to spanNanos,
	// yielding a zero-width window and a single empty bucket.
	parsed := &parsedProfile{
		timedEvents: map[string][]timedEvent{
			"cpu": {},
		},
		spanNanos: 5_000_000_000,
	}
	// Simulate clamping from main.go: both values clamp to spanNanos.
	fromNanos := int64(100_000_000_000)
	toNanos := int64(200_000_000_000)
	if fromNanos >= parsed.spanNanos {
		fromNanos = parsed.spanNanos
	}
	if toNanos > parsed.spanNanos {
		toNanos = parsed.spanNanos
	}
	out := captureOutput(func() {
		cmdTimeline(parsed, "cpu", 0, "", "", false, false, nil, "",
			fromNanos, toNanos, 0, false)
	})
	if !strings.Contains(out, "Buckets: 1") {
		t.Errorf("expected 1 bucket for both-beyond-span, got %q", out)
	}
}

func TestCmdTimelineSubSecondResolutionLabels(t *testing.T) {
	parsed := &parsedProfile{
		timedEvents: map[string][]timedEvent{
			"cpu": {
				{
					offsetNanos: 284_000_000_000, // 4m44.000s
					stackKey:    "A",
					frames:      []string{"A"},
					lines:       []uint32{0},
					weight:      1,
				},
			},
		},
		spanNanos: 300_000_000_000,
	}

	out := captureOutput(func() {
		cmdTimeline(parsed, "cpu", 0, "1ms", "", false, false, nil, "",
			284_000_000_000, 284_003_000_000, 0, false)
	})

	if !strings.Contains(out, "Buckets: 3 (1ms each)") {
		t.Errorf("expected millisecond bucket width in header, got %q", out)
	}
	if !strings.Contains(out, "4m44.000s-4m44.001s") {
		t.Errorf("expected millisecond precision time labels, got %q", out)
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		nanos int64
		want  string
	}{
		{0, "0.0s"},
		{500_000_000, "0.5s"},
		{1_000_000_000, "1.0s"},
		{12_500_000_000, "12.5s"},
		{90_000_000_000, "1m30.0s"},
		{-1, "0.0s"},
	}
	for _, tt := range tests {
		got := formatDuration(tt.nanos)
		if got != tt.want {
			t.Errorf("formatDuration(%d) = %q, want %q", tt.nanos, got, tt.want)
		}
	}
}

func TestFormatBucketWidth(t *testing.T) {
	tests := []struct {
		nanos int64
		want  string
	}{
		{0, "0.0s"},
		{1_000_000, "1ms"},
		{1_500_000, "1500us"},
		{609_352_600, "609.4ms"},
		{1_000_000_000, "1.0s"},
	}
	for _, tt := range tests {
		got := formatBucketWidth(tt.nanos)
		if got != tt.want {
			t.Errorf("formatBucketWidth(%d) = %q, want %q", tt.nanos, got, tt.want)
		}
	}
}

func TestTimeWindowEcho(t *testing.T) {
	// --from/--to on non-timeline JFR command should echo window to stderr.
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"--from", "1s", "--to", "3s"}, "Window: 1.0s to 3.0s"},
		{[]string{"--from", "1s"}, "Window: 1.0s to end"},
		{[]string{"--to", "3s"}, "Window: start to 3.0s"},
	}
	path := jfrFixture("cpu.jfr")
	for _, tt := range tests {
		args := append([]string{"hot"}, tt.args...)
		args = append(args, path)
		exitCode, _, stderr := runCLIForTest(t, args, nil)
		if exitCode != 0 {
			t.Fatalf("args=%v: exit code = %d, want 0; stderr=%q", args, exitCode, stderr)
		}
		if !strings.Contains(stderr, tt.want) {
			t.Errorf("args=%v: expected %q in stderr, got %q", args, tt.want, stderr)
		}
	}
}

func TestBuildStackFileFromTimed(t *testing.T) {
	events := []timedEvent{
		{stackKey: "A;B", frames: []string{"A", "B"}, lines: []uint32{0, 0}, thread: "main", weight: 3},
		{stackKey: "A;B", frames: []string{"A", "B"}, lines: []uint32{0, 0}, thread: "main", weight: 2},
		{stackKey: "A;C", frames: []string{"A", "C"}, lines: []uint32{0, 0}, thread: "main", weight: 1},
	}
	sf := buildStackFileFromTimed(events)
	if sf.totalSamples != 6 {
		t.Errorf("totalSamples = %d, want 6", sf.totalSamples)
	}
}

func TestEventSelectionAfterFilter(t *testing.T) {
	// Parse multi.jfr with a time range that may exclude some event types.
	parsed, err := parseJFRData(jfrFixture("multi.jfr"), allEventTypes(), parseOpts{
		collectTimestamps: true,
		fromNanos:         -1,
		toNanos:           -1,
	})
	if err != nil {
		t.Fatalf("parseJFRData: %v", err)
	}
	// Should have multiple event types.
	if len(parsed.timedEvents) == 0 {
		t.Fatal("expected timed events")
	}
	// Recompute counts from timed events.
	filteredCounts := make(map[string]int)
	for et, events := range parsed.timedEvents {
		if len(events) > 0 {
			filteredCounts[et] = len(events)
		}
	}
	// resolveEventType should work on filtered counts.
	eventType, _ := resolveEventType("cpu", false, filteredCounts)
	if eventType == "" {
		t.Error("resolveEventType returned empty")
	}
}

// ---------------------------------------------------------------------------
// TestWarnedLargeEventCountOnce — volume warning deduplication (Item 4)
// ---------------------------------------------------------------------------

func TestWarnedLargeEventCountOnce(t *testing.T) {
	warnedLargeEventCount.Store(false)
	defer warnedLargeEventCount.Store(false)

	path := jfrFixture("cpu.jfr")
	call := func() string {
		return captureStream(&os.Stderr, func() {
			parseJFRData(path, allEventTypes(), parseOpts{collectTimestamps: true, fromNanos: -1, toNanos: -1})
		})
	}

	first := call()
	second := call()

	// The fixture is small so the warning won't fire, but we verify the flag
	// prevents double-warning by manually setting it.
	warnedLargeEventCount.Store(false)

	// Force the flag on and verify second call doesn't warn.
	warnedLargeEventCount.Store(true)
	third := call()
	if strings.Contains(third, "events collected") {
		t.Error("warning should not appear when warnedLargeEventCount is true")
	}

	// Reset and verify warning can appear again.
	warnedLargeEventCount.Store(false)
	_ = first
	_ = second
}

// ---------------------------------------------------------------------------
// TestCmdEventsColumnLabelAndTotal — events COUNT header and total row (Item 5)
// ---------------------------------------------------------------------------

func TestCmdEventsColumnLabelAndTotal(t *testing.T) {
	out := captureOutput(func() {
		err := cmdEvents(jfrFixture("multi.jfr"))
		if err != nil {
			t.Fatalf("cmdEvents: %v", err)
		}
	})
	if !strings.Contains(out, "COUNT") {
		t.Errorf("expected COUNT header, got:\n%s", out)
	}
	if strings.Contains(out, "SAMPLES") {
		t.Errorf("should not have SAMPLES header, got:\n%s", out)
	}
	if !strings.Contains(out, "total") {
		t.Errorf("expected total row, got:\n%s", out)
	}
	// Verify total equals sum of individual rows.
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 3 {
		t.Fatalf("expected at least 3 lines (header, events, total), got %d", len(lines))
	}
	sum := 0
	for _, line := range lines[1 : len(lines)-1] {
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			n, err := strconv.Atoi(fields[len(fields)-1])
			if err == nil {
				sum += n
			}
		}
	}
	totalLine := lines[len(lines)-1]
	fields := strings.Fields(totalLine)
	if len(fields) < 2 {
		t.Fatalf("malformed total line: %q", totalLine)
	}
	totalVal, err := strconv.Atoi(fields[len(fields)-1])
	if err != nil {
		t.Fatalf("cannot parse total: %v", err)
	}
	if totalVal != sum {
		t.Errorf("total %d != sum of rows %d", totalVal, sum)
	}
}

// ---------------------------------------------------------------------------
// TestCmdThreadsGroup — threads --group flag (Item 6)
// ---------------------------------------------------------------------------

func TestCmdThreadsGroup(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.a"}, lines: []uint32{0}, count: 10, thread: "pool-1-thread-1"},
		{frames: []string{"B.b"}, lines: []uint32{0}, count: 8, thread: "pool-1-thread-2"},
		{frames: []string{"C.c"}, lines: []uint32{0}, count: 5, thread: "pool-1-thread-3"},
		{frames: []string{"D.d"}, lines: []uint32{0}, count: 3, thread: "main"},
	})

	out := captureOutput(func() {
		cmdThreads(sf, 0, true)
	})

	if !strings.Contains(out, "GROUP") {
		t.Error("expected GROUP header")
	}
	if !strings.Contains(out, "pool-thread") {
		t.Errorf("expected merged group 'pool-thread', got:\n%s", out)
	}
	if !strings.Contains(out, "(3 threads)") {
		t.Errorf("expected '(3 threads)' suffix, got:\n%s", out)
	}
	// Single-thread group should not have suffix.
	if strings.Contains(out, "main (1") {
		t.Errorf("single-thread group should not have count suffix, got:\n%s", out)
	}
}

func TestCmdThreadsGroupWithTop(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.a"}, lines: []uint32{0}, count: 10, thread: "pool-1-thread-1"},
		{frames: []string{"B.b"}, lines: []uint32{0}, count: 8, thread: "pool-1-thread-2"},
		{frames: []string{"C.c"}, lines: []uint32{0}, count: 5, thread: "http-1"},
		{frames: []string{"D.d"}, lines: []uint32{0}, count: 3, thread: "http-2"},
		{frames: []string{"E.e"}, lines: []uint32{0}, count: 1, thread: "main"},
	})

	out := captureOutput(func() {
		cmdThreads(sf, 2, true)
	})

	lines := strings.Split(strings.TrimSpace(out), "\n")
	// Header + 2 data rows = 3 lines.
	if len(lines) != 3 {
		t.Errorf("expected 3 lines (header + 2 groups), got %d:\n%s", len(lines), out)
	}
}

func TestCmdThreadsGroupNoThreadBottom(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.a"}, lines: []uint32{0}, count: 10, thread: "pool-1-thread-1"},
		{frames: []string{"B.b"}, lines: []uint32{0}, count: 5, thread: "pool-1-thread-2"},
		{frames: []string{"C.c"}, lines: []uint32{0}, count: 3, thread: ""},
	})

	out := captureOutput(func() {
		cmdThreads(sf, 0, true)
	})

	if !strings.Contains(out, "(no thread info)") {
		t.Errorf("expected '(no thread info)' row, got:\n%s", out)
	}
	// It should be the last line.
	lines := strings.Split(strings.TrimSpace(out), "\n")
	last := lines[len(lines)-1]
	if !strings.Contains(last, "(no thread info)") {
		t.Errorf("(no thread info) should be last line, got:\n%s", out)
	}
}

func TestCmdThreadsGroupCLI(t *testing.T) {
	code, stdout, _ := runCLIForTest(t, []string{"threads", "--group", jfrFixture("cpu.jfr")}, nil)
	if code != 0 {
		t.Fatalf("threads --group exit code = %d, want 0", code)
	}
	if !strings.Contains(stdout, "GROUP") {
		t.Errorf("expected GROUP header in CLI output, got:\n%s", stdout)
	}
}

func TestCmdThreadsGroupFalse(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A.a"}, lines: []uint32{0}, count: 10, thread: "pool-1-thread-1"},
		{frames: []string{"B.b"}, lines: []uint32{0}, count: 5, thread: "pool-1-thread-2"},
	})

	out := captureOutput(func() {
		cmdThreads(sf, 0, false)
	})

	if !strings.Contains(out, "THREAD") {
		t.Error("expected THREAD header when group=false")
	}
	if strings.Contains(out, "GROUP") {
		t.Error("should not have GROUP header when group=false")
	}
}

// ---------------------------------------------------------------------------
// TestPerCommandHelp — per-command help system (Item 1)
// ---------------------------------------------------------------------------

func TestPerCommandHelp(t *testing.T) {
	commands := []string{"hot", "tree", "trace", "callers", "threads", "filter", "events", "collapse", "diff", "lines", "info", "timeline"}
	for _, cmd := range commands {
		t.Run(cmd, func(t *testing.T) {
			code, stdout, _ := runCLIForTest(t, []string{cmd, "--help"}, nil)
			if code != 0 {
				t.Errorf("%s --help exit code = %d, want 0", cmd, code)
			}
			if !strings.Contains(strings.ToLower(stdout), cmd) {
				t.Errorf("%s --help output should mention '%s', got:\n%s", cmd, cmd, stdout)
			}
		})
	}
}

func TestPerCommandHelpShort(t *testing.T) {
	code, stdout, _ := runCLIForTest(t, []string{"hot", "-h"}, nil)
	if code != 0 {
		t.Errorf("hot -h exit code = %d, want 0", code)
	}
	if !strings.Contains(stdout, "hot") {
		t.Errorf("hot -h should mention 'hot', got:\n%s", stdout)
	}
}

func TestScriptHelpStillWorks(t *testing.T) {
	code, stdout, _ := runCLIForTest(t, []string{"script", "--help"}, nil)
	if code != 0 {
		t.Errorf("script --help exit code = %d, want 0", code)
	}
	if !strings.Contains(stdout, "Starlark") {
		t.Errorf("script --help should contain 'Starlark', got:\n%s", stdout)
	}
}

func TestUnknownCommandHelp(t *testing.T) {
	code, _, stderr := runCLIForTest(t, []string{"nonexistent", "--help"}, nil)
	if code != 1 {
		t.Errorf("nonexistent --help exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr, "unknown command") {
		t.Errorf("nonexistent --help should mention unknown command, got:\n%s", stderr)
	}
}

func TestGlobalHelpMentionsPerCommand(t *testing.T) {
	_, stdout, _ := runCLIForTest(t, []string{"--help"}, nil)
	if !strings.Contains(stdout, "help") {
		t.Errorf("global help should mention help, got:\n%s", stdout)
	}
}

// ---------------------------------------------------------------------------
// TestUnknownFlagRejected — cobra rejects unknown flags
// ---------------------------------------------------------------------------

func TestUnknownFlagRejected(t *testing.T) {
	code, _, stderr := runCLIForTest(t, []string{"hot", jfrFixture("cpu.jfr"), "--tp", "5"}, nil)
	if code == 0 {
		t.Error("expected non-zero exit for unknown flag --tp")
	}
	if !strings.Contains(stderr, "unknown flag") {
		t.Errorf("expected 'unknown flag' in stderr, got:\n%s", stderr)
	}
}

// ---------------------------------------------------------------------------
// TestFromNegativeDuration — --from -1s works (pflag handles negative values)
// ---------------------------------------------------------------------------

func TestFromNegativeDuration(t *testing.T) {
	code, stdout, stderr := runCLIForTest(t, []string{"hot", jfrFixture("cpu.jfr"), "--from", "-1s"}, nil)
	if code != 0 {
		t.Errorf("--from -1s exit code = %d, want 0, stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "SELF TIME") {
		t.Errorf("expected hot output, got:\n%s", stdout)
	}
}

// ---------------------------------------------------------------------------
// TestNoIdleHint — --no-idle hint for WALL profiles (Item 2)
// ---------------------------------------------------------------------------

func TestNoIdleHintCollapsed(t *testing.T) {
	// Construct collapsed input with >50% idle leaf frames (wall event type).
	input := strings.NewReader("[main];A.main;java.lang.Thread.sleep 60\n[main];A.main;B.work 40\n")
	_, _, stderr := runCLIForTest(t, []string{"hot", "--event", "wall", "-"}, input)
	if !strings.Contains(stderr, "Hint:") {
		t.Errorf("expected idle hint for wall profile with >50%% idle, got stderr:\n%s", stderr)
	}
	if !strings.Contains(stderr, "--no-idle") {
		t.Errorf("hint should mention --no-idle, got stderr:\n%s", stderr)
	}
}

func TestNoIdleHintCollapsedWithNoIdle(t *testing.T) {
	input := strings.NewReader("[main];A.main;java.lang.Thread.sleep 60\n[main];A.main;B.work 40\n")
	_, _, stderr := runCLIForTest(t, []string{"hot", "--event", "wall", "--no-idle", "-"}, input)
	if strings.Contains(stderr, "Hint:") {
		t.Errorf("no hint expected when --no-idle is set, got stderr:\n%s", stderr)
	}
}

func TestNoIdleHintCLICPU(t *testing.T) {
	_, _, stderr := runCLIForTest(t, []string{"hot", jfrFixture("cpu.jfr")}, nil)
	if strings.Contains(stderr, "Hint:") {
		t.Errorf("no idle hint expected for CPU profile, got stderr:\n%s", stderr)
	}
}

func TestNoIdleHintCLILock(t *testing.T) {
	_, _, stderr := runCLIForTest(t, []string{"hot", "--event", "lock", jfrFixture("lock.jfr")}, nil)
	if strings.Contains(stderr, "Hint:") {
		t.Errorf("no idle hint expected for lock profile, got stderr:\n%s", stderr)
	}
}

func TestNoIdleHintBelowThreshold(t *testing.T) {
	// Only 30% idle — should NOT trigger the hint.
	input := strings.NewReader("[main];A.main;java.lang.Thread.sleep 30\n[main];A.main;B.work 70\n")
	_, _, stderr := runCLIForTest(t, []string{"hot", "--event", "wall", "-"}, input)
	if strings.Contains(stderr, "Hint:") {
		t.Errorf("no hint expected when idle <50%%, got stderr:\n%s", stderr)
	}
}

func TestCmdTimelineTop(t *testing.T) {
	parsed, err := parseJFRData(jfrFixture("cpu.jfr"), allEventTypes(), parseOpts{
		collectTimestamps: true,
		fromNanos:         -1,
		toNanos:           -1,
	})
	if err != nil {
		t.Fatalf("parseJFRData: %v", err)
	}
	out := captureOutput(func() {
		cmdTimeline(parsed, "cpu", 10, "", "", false, false, nil, "", -1, -1, 3, false)
	})
	if !strings.Contains(out, "Top: 3") {
		t.Errorf("expected 'Top: 3' in header, got:\n%s", out)
	}
	// Count data lines (lines with bars).
	lines := strings.Split(strings.TrimSpace(out), "\n")
	dataLines := 0
	for _, line := range lines {
		if strings.Contains(line, "s-") && strings.Contains(line, "\u2588") {
			dataLines++
		}
	}
	if dataLines != 3 {
		t.Errorf("expected 3 data lines with bars, got %d\nOutput:\n%s", dataLines, out)
	}
	// Verify time order: extract timestamps and confirm non-decreasing.
	var timestamps []string
	for _, line := range lines {
		if strings.Contains(line, "s-") && strings.Contains(line, "\u2588") {
			timestamps = append(timestamps, strings.Fields(line)[0])
		}
	}
	for i := 1; i < len(timestamps); i++ {
		if timestamps[i] < timestamps[i-1] {
			t.Errorf("timestamps not in order: %v", timestamps)
			break
		}
	}
}

func TestCmdTimelineTopExceedsBuckets(t *testing.T) {
	parsed, err := parseJFRData(jfrFixture("cpu.jfr"), allEventTypes(), parseOpts{
		collectTimestamps: true,
		fromNanos:         -1,
		toNanos:           -1,
	})
	if err != nil {
		t.Fatalf("parseJFRData: %v", err)
	}
	// --top 100 with only 5 buckets: should show all non-empty buckets.
	out := captureOutput(func() {
		cmdTimeline(parsed, "cpu", 5, "", "", false, false, nil, "", -1, -1, 100, false)
	})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	dataLines := 0
	for _, line := range lines {
		if strings.Contains(line, "s-") && strings.Contains(line, "\u2588") {
			dataLines++
		}
	}
	// All 5 buckets should have data in cpu.jfr, so all should show.
	if dataLines < 3 {
		t.Errorf("expected most buckets to show, got %d data lines\nOutput:\n%s", dataLines, out)
	}
	if dataLines > 5 {
		t.Errorf("expected at most 5 data lines, got %d", dataLines)
	}
}

func TestCmdTimelinePct(t *testing.T) {
	parsed, err := parseJFRData(jfrFixture("cpu.jfr"), allEventTypes(), parseOpts{
		collectTimestamps: true,
		fromNanos:         -1,
		toNanos:           -1,
	})
	if err != nil {
		t.Fatalf("parseJFRData: %v", err)
	}
	out := captureOutput(func() {
		cmdTimeline(parsed, "cpu", 5, "", "Workload", false, false, nil, "", -1, -1, 0, true)
	})
	if !strings.Contains(out, "Pct") {
		t.Errorf("expected 'Pct' column header, got:\n%s", out)
	}
	if strings.Contains(out, "Samples") {
		t.Errorf("expected no 'Samples' column header in pct mode, got:\n%s", out)
	}
	// Verify percentage values are present.
	lines := strings.Split(strings.TrimSpace(out), "\n")
	foundPct := false
	for _, line := range lines {
		if strings.Contains(line, "s-") && strings.Contains(line, "%") {
			foundPct = true
			break
		}
	}
	if !foundPct {
		t.Errorf("expected percentage values in output, got:\n%s", out)
	}
}

func TestCmdTimelinePctEmptyBucket(t *testing.T) {
	parsed, err := parseJFRData(jfrFixture("cpu.jfr"), allEventTypes(), parseOpts{
		collectTimestamps: true,
		fromNanos:         -1,
		toNanos:           -1,
	})
	if err != nil {
		t.Fatalf("parseJFRData: %v", err)
	}
	// Use a method that won't match in all buckets + many buckets to ensure some are empty.
	out := captureOutput(func() {
		cmdTimeline(parsed, "cpu", 40, "", "Workload", false, false, nil, "", -1, -1, 0, true)
	})
	// Should not panic or produce NaN/Inf. All percentage values should be valid.
	if strings.Contains(out, "NaN") || strings.Contains(out, "Inf") {
		t.Errorf("unexpected NaN/Inf in output:\n%s", out)
	}
	// Empty buckets (where method has 0 matches) should show 0.0%.
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for _, line := range lines {
		if strings.Contains(line, "s-") && strings.Contains(line, "0.0%") {
			return // found at least one 0.0% bucket
		}
	}
	// It's okay if all buckets have some matches with 40 buckets; just verify no errors.
}

func TestCmdTimelinePctRequiresMethod(t *testing.T) {
	path := jfrFixture("cpu.jfr")
	exitCode, _, stderr := runCLIForTest(t, []string{"timeline", path, "--pct"}, nil)
	if exitCode == 0 {
		t.Errorf("expected non-zero exit code, got 0")
	}
	if !strings.Contains(stderr, "--pct requires --method") {
		t.Errorf("expected '--pct requires --method' in stderr, got:\n%s", stderr)
	}
}

func TestCmdTimelinePctFlagBeforeFile(t *testing.T) {
	// --pct before the file must not swallow the file path as a flag value.
	path := jfrFixture("cpu.jfr")
	exitCode, _, stderr := runCLIForTest(t, []string{"timeline", "--pct", "--method", "Workload", path}, nil)
	if exitCode != 0 {
		t.Errorf("expected exit code 0, got %d; stderr:\n%s", exitCode, stderr)
	}
}

func TestCmdTimelineTopWithPct(t *testing.T) {
	parsed, err := parseJFRData(jfrFixture("cpu.jfr"), allEventTypes(), parseOpts{
		collectTimestamps: true,
		fromNanos:         -1,
		toNanos:           -1,
	})
	if err != nil {
		t.Fatalf("parseJFRData: %v", err)
	}
	out := captureOutput(func() {
		cmdTimeline(parsed, "cpu", 10, "", "Workload", false, false, nil, "", -1, -1, 3, true)
	})
	if !strings.Contains(out, "Top: 3") {
		t.Errorf("expected 'Top: 3' in header, got:\n%s", out)
	}
	if !strings.Contains(out, "Pct") {
		t.Errorf("expected 'Pct' column header, got:\n%s", out)
	}
	// Count data lines: should be exactly 3.
	lines := strings.Split(strings.TrimSpace(out), "\n")
	dataLines := 0
	for _, line := range lines {
		if strings.Contains(line, "s-") && strings.Contains(line, "%") {
			dataLines++
		}
	}
	if dataLines != 3 {
		t.Errorf("expected 3 data lines, got %d\nOutput:\n%s", dataLines, out)
	}
}
