package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testTimeoutUnit = time.Millisecond
const testTimeout = 5 * time.Second

func scriptFixture(name string) string {
	return filepath.Join("testdata", name)
}

// ---------------------------------------------------------------------------
// Increment 1 — Script execution skeleton
// ---------------------------------------------------------------------------

func TestScriptInlinePrint(t *testing.T) {
	out := captureOutput(func() {
		code := runScript(`print("hello")`, "", nil, testTimeout)
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	if strings.TrimSpace(out) != "hello" {
		t.Fatalf("expected 'hello', got %q", out)
	}
}

func TestScriptInlineFail(t *testing.T) {
	stderr := captureStream(&os.Stderr, func() {
		code := runScript(`fail("boom")`, "", nil, testTimeout)
		if code != 1 {
			t.Fatalf("expected exit 1, got %d", code)
		}
	})
	if !strings.Contains(stderr, "boom") {
		t.Fatalf("expected stderr to contain 'boom', got %q", stderr)
	}
}

func TestScriptInlineWarn(t *testing.T) {
	stderr := captureStream(&os.Stderr, func() {
		code := runScript(`warn("note")`, "", nil, testTimeout)
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	if !strings.Contains(stderr, "note") {
		t.Fatalf("expected stderr to contain 'note', got %q", stderr)
	}
}

func TestScriptArgs(t *testing.T) {
	out := captureOutput(func() {
		code := runScript(`print(ARGS)`, "", []string{"a", "b", "c"}, testTimeout)
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	if !strings.Contains(out, `"a"`) || !strings.Contains(out, `"b"`) || !strings.Contains(out, `"c"`) {
		t.Fatalf("expected ARGS to contain a, b, c, got %q", out)
	}
}

func TestScriptArgsEmpty(t *testing.T) {
	out := captureOutput(func() {
		code := runScript(`print(len(ARGS))`, "", nil, testTimeout)
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	if strings.TrimSpace(out) != "0" {
		t.Fatalf("expected empty ARGS, got %q", out)
	}
}

func TestScriptSyntaxError(t *testing.T) {
	stderr := captureStream(&os.Stderr, func() {
		code := runScript(`def(`, "", nil, testTimeout)
		if code != 2 {
			t.Fatalf("expected exit 2, got %d", code)
		}
	})
	if !strings.Contains(stderr, "error") {
		t.Fatalf("expected error in stderr, got %q", stderr)
	}
}

func TestScriptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.star")
	os.WriteFile(path, []byte(`print("from file")`), 0644)

	out := captureOutput(func() {
		code := runScript("", path, nil, testTimeout)
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	if !strings.Contains(out, "from file") {
		t.Fatalf("expected 'from file', got %q", out)
	}
}

func TestScriptTimeout(t *testing.T) {
	stderr := captureStream(&os.Stderr, func() {
		code := runScript(`
x = 0
while True:
    x += 1
`, "", nil, 50*testTimeoutUnit)
		if code == 0 {
			t.Fatalf("expected non-zero exit code")
		}
	})
	if !strings.Contains(stderr, "timed out") {
		t.Fatalf("expected timeout error, got %q", stderr)
	}
}

func TestScriptTopLevelControl(t *testing.T) {
	out := captureOutput(func() {
		code := runScript(`
if True:
    print("ok")
`, "", nil, testTimeout)
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	if strings.TrimSpace(out) != "ok" {
		t.Fatalf("expected 'ok', got %q", out)
	}
}

func TestScriptSets(t *testing.T) {
	out := captureOutput(func() {
		code := runScript(`s = set([1, 2, 3]); print(len(s))`, "", nil, testTimeout)
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	if strings.TrimSpace(out) != "3" {
		t.Fatalf("expected '3', got %q", out)
	}
}

func TestScriptWhile(t *testing.T) {
	out := captureOutput(func() {
		code := runScript(`
x = 0
while x < 5:
    x += 1
print(x)
`, "", nil, testTimeout)
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	if strings.TrimSpace(out) != "5" {
		t.Fatalf("expected '5', got %q", out)
	}
}

// ---------------------------------------------------------------------------
// Increment 2 — open() + Profile + Stack + Frame
// ---------------------------------------------------------------------------

func TestOpenJFR(t *testing.T) {
	out := captureOutput(func() {
		code := runScript(fmt.Sprintf(`
p = open(%q)
print(p.samples)
print(p.event)
`, scriptFixture("cpu.jfr")), "", nil, testTimeout)
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected 2 lines, got %q", out)
	}
	samples := lines[0]
	if samples == "0" {
		t.Fatalf("expected samples > 0, got %s", samples)
	}
	if strings.TrimSpace(lines[1]) != "cpu" {
		t.Fatalf("expected event 'cpu', got %q", lines[1])
	}
}

func TestOpenJFRWall(t *testing.T) {
	out := captureOutput(func() {
		code := runScript(fmt.Sprintf(`
p = open(%q, event="wall")
print(p.event)
print(p.samples)
`, scriptFixture("wall.jfr")), "", nil, testTimeout)
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected 2 lines, got %q", out)
	}
	if strings.TrimSpace(lines[0]) != "wall" {
		t.Fatalf("expected event 'wall', got %q", lines[0])
	}
	if strings.TrimSpace(lines[1]) == "0" {
		t.Fatalf("expected wall samples > 0")
	}
}

func TestOpenJFRMultiEvents(t *testing.T) {
	out := captureOutput(func() {
		code := runScript(fmt.Sprintf(`
p = open(%q)
print(len(p.events))
for e in p.events:
    print(e)
`, scriptFixture("multi.jfr")), "", nil, testTimeout)
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected multiple events, got %q", out)
	}
}

func TestOpenCollapsed(t *testing.T) {
	out := captureOutput(func() {
		code := runScript(fmt.Sprintf(`
p = open(%q)
print(p.samples)
print(p.duration)
`, scriptFixture("perf.collapsed")), "", nil, testTimeout)
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected 2 lines, got %q", out)
	}
	if strings.TrimSpace(lines[0]) == "0" {
		t.Fatalf("expected samples > 0")
	}
	if strings.TrimSpace(lines[1]) != "0.0" && strings.TrimSpace(lines[1]) != "0" {
		t.Fatalf("expected duration 0 for collapsed, got %q", lines[1])
	}
}

func TestOpenStartEnd(t *testing.T) {
	// Open the full profile first.
	var fullSamples string
	out := captureOutput(func() {
		code := runScript(fmt.Sprintf(`p = open(%q); print(p.samples)`, scriptFixture("cpu.jfr")), "", nil, testTimeout)
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	fullSamples = strings.TrimSpace(out)

	// Open with a time window.
	out = captureOutput(func() {
		code := runScript(fmt.Sprintf(`p = open(%q, start="1s", end="5s"); print(p.samples)`, scriptFixture("cpu.jfr")), "", nil, testTimeout)
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	windowSamples := strings.TrimSpace(out)
	if windowSamples == fullSamples {
		t.Logf("warning: time window did not reduce samples (recording may be too short)")
	}
}

func TestOpenNotFound(t *testing.T) {
	stderr := captureStream(&os.Stderr, func() {
		code := runScript(`p = open("nonexistent.jfr")`, "", nil, testTimeout)
		if code == 0 {
			t.Fatalf("expected error for nonexistent file")
		}
	})
	if !strings.Contains(stderr, "error") {
		t.Fatalf("expected error in stderr, got %q", stderr)
	}
}

func TestOpenBadEvent(t *testing.T) {
	stderr := captureStream(&os.Stderr, func() {
		code := runScript(fmt.Sprintf(`p = open(%q, event="bogus")`, scriptFixture("cpu.jfr")), "", nil, testTimeout)
		if code == 0 {
			t.Fatalf("expected error for bad event type")
		}
	})
	if !strings.Contains(stderr, "bogus") || !strings.Contains(stderr, "error") {
		t.Fatalf("expected error mentioning bogus, got %q", stderr)
	}
}

func TestProfileFields(t *testing.T) {
	out := captureOutput(func() {
		code := runScript(fmt.Sprintf(`
p = open(%q)
print(type(p.stacks))
print(type(p.samples))
print(type(p.duration))
print(type(p.event))
print(type(p.events))
print(type(p.path))
`, scriptFixture("cpu.jfr")), "", nil, testTimeout)
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	expected := []string{"list", "int", "float", "string", "list", "string"}
	for i, exp := range expected {
		if i >= len(lines) {
			t.Fatalf("expected %d lines, got %d", len(expected), len(lines))
		}
		if strings.TrimSpace(lines[i]) != exp {
			t.Fatalf("field %d: expected type %q, got %q", i, exp, lines[i])
		}
	}
}

func TestStackFields(t *testing.T) {
	out := captureOutput(func() {
		code := runScript(fmt.Sprintf(`
p = open(%q)
s = p.stacks[0]
print(type(s.frames))
print(type(s.thread))
print(type(s.samples))
print(s.depth)
print(type(s.leaf))
print(type(s.root))
`, scriptFixture("cpu.jfr")), "", nil, testTimeout)
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 6 {
		t.Fatalf("expected 6 lines, got %q", out)
	}
	if strings.TrimSpace(lines[0]) != "list" {
		t.Fatalf("frames should be list, got %q", lines[0])
	}
	if strings.TrimSpace(lines[1]) != "string" {
		t.Fatalf("thread should be string, got %q", lines[1])
	}
	if strings.TrimSpace(lines[2]) != "int" {
		t.Fatalf("samples should be int, got %q", lines[2])
	}
	if strings.TrimSpace(lines[4]) != "Frame" {
		t.Fatalf("leaf should be Frame, got %q", lines[4])
	}
}

func TestFrameFieldsJava(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"com/example/App.process"}, lines: []uint32{42}, count: 10},
	})
	p := newStarlarkProfile(sf, nil, "cpu", "test")

	out := captureOutput(func() {
		code := runScript(`
s = p.stacks[0]
f = s.frames[0]
print(f.name)
print(f.fqn)
print(f.pkg)
print(f.cls)
print(f.method)
print(f.line)
`, "", nil, testTimeout, withPredeclared("p", p))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	expects := []string{"App.process", "com.example.App.process", "com.example", "App", "process", "42"}
	for i, exp := range expects {
		if i >= len(lines) {
			t.Fatalf("expected %d lines, got %d", len(expects), len(lines))
		}
		if strings.TrimSpace(lines[i]) != exp {
			t.Fatalf("field %d: expected %q, got %q", i, exp, lines[i])
		}
	}
}

func TestFrameFieldsNative(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"libc.so.6.__sched_yield"}, lines: []uint32{0}, count: 5},
	})
	p := newStarlarkProfile(sf, nil, "cpu", "test")

	out := captureOutput(func() {
		code := runScript(`
f = p.stacks[0].frames[0]
print("name=" + f.name)
print("pkg=" + f.pkg)
print("cls=" + f.cls)
`, "", nil, testTimeout, withPredeclared("p", p))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 3 {
		t.Fatalf("expected 3 lines, got %d: %q", len(lines), out)
	}
	if strings.TrimSpace(lines[0]) != "name=__sched_yield" {
		t.Fatalf("expected name '__sched_yield', got %q", lines[0])
	}
	if strings.TrimSpace(lines[1]) != "pkg=" {
		t.Fatalf("expected empty pkg, got %q", lines[1])
	}
	if strings.TrimSpace(lines[2]) != "cls=" {
		t.Fatalf("expected empty cls, got %q", lines[2])
	}
}

func TestFrameFieldsKernel(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"__do_softirq"}, lines: []uint32{0}, count: 5},
	})
	p := newStarlarkProfile(sf, nil, "cpu", "test")

	out := captureOutput(func() {
		code := runScript(`
f = p.stacks[0].frames[0]
print("name=" + f.name)
print("pkg=" + f.pkg)
print("cls=" + f.cls)
print("method=" + f.method)
`, "", nil, testTimeout, withPredeclared("p", p))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 4 {
		t.Fatalf("expected 4 lines, got %d: %q", len(lines), out)
	}
	if strings.TrimSpace(lines[0]) != "name=__do_softirq" {
		t.Fatalf("expected '__do_softirq', got %q", lines[0])
	}
	if strings.TrimSpace(lines[1]) != "pkg=" {
		t.Fatalf("expected empty pkg, got %q", lines[1])
	}
	if strings.TrimSpace(lines[2]) != "cls=" {
		t.Fatalf("expected empty cls, got %q", lines[2])
	}
	if strings.TrimSpace(lines[3]) != "method=__do_softirq" {
		t.Fatalf("expected method '__do_softirq', got %q", lines[3])
	}
}

func TestFrameLine(t *testing.T) {
	out := captureOutput(func() {
		code := runScript(fmt.Sprintf(`
p = open(%q)
found = False
for s in p.stacks:
    if found:
        break
    for f in s.frames:
        if f.line > 0:
            print(f.name, f.line)
            found = True
            break
`, scriptFixture("cpu.jfr")), "", nil, testTimeout)
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	out = strings.TrimSpace(out)
	if out == "" {
		t.Log("no frames with line numbers in cpu.jfr (this is OK if profiler didn't capture lines)")
	}
}

func TestProfileStacksImmutable(t *testing.T) {
	stderr := captureStream(&os.Stderr, func() {
		code := runScript(fmt.Sprintf(`
p = open(%q)
p.stacks.append("bad")
`, scriptFixture("cpu.jfr")), "", nil, testTimeout)
		if code == 0 {
			t.Fatalf("expected error when modifying frozen stacks")
		}
	})
	_ = stderr // error is expected
}

// ---------------------------------------------------------------------------
// Increment 3 — Stack methods
// ---------------------------------------------------------------------------

func testProfile() *starlarkProfile {
	sf := makeStackFile([]stack{
		{frames: []string{"java/lang/Thread.run", "com/example/Server.handle", "com/example/Service.process", "java/util/HashMap.put"}, lines: []uint32{0, 10, 20, 30}, count: 10, thread: "worker-1"},
		{frames: []string{"java/lang/Thread.run", "com/example/Server.handle", "com/example/Encoder.encode"}, lines: []uint32{0, 10, 40}, count: 5, thread: "worker-2"},
		{frames: []string{"__do_softirq"}, lines: []uint32{0}, count: 3, thread: ""},
	})
	return newStarlarkProfile(sf, nil, "cpu", "test")
}

func TestStackHas(t *testing.T) {
	p := testProfile()
	out := captureOutput(func() {
		code := runScript(`
s = p.stacks[0]
print(s.has("HashMap.put"))
print(s.has("NonExistent"))
`, "", nil, testTimeout, withPredeclared("p", p))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if strings.TrimSpace(lines[0]) != "True" {
		t.Fatalf("expected True for HashMap.put, got %q", lines[0])
	}
	if strings.TrimSpace(lines[1]) != "False" {
		t.Fatalf("expected False for NonExistent, got %q", lines[1])
	}
}

func TestStackHasShortName(t *testing.T) {
	p := testProfile()
	out := captureOutput(func() {
		code := runScript(`print(p.stacks[0].has("HashMap"))`, "", nil, testTimeout, withPredeclared("p", p))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	if strings.TrimSpace(out) != "True" {
		t.Fatalf("expected True, got %q", out)
	}
}

func TestStackHasSeq(t *testing.T) {
	p := testProfile()
	out := captureOutput(func() {
		code := runScript(`
s = p.stacks[0]
print(s.has_seq("Server.handle", "HashMap.put"))
`, "", nil, testTimeout, withPredeclared("p", p))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	if strings.TrimSpace(out) != "True" {
		t.Fatalf("expected True, got %q", out)
	}
}

func TestStackHasSeqNonAdjacent(t *testing.T) {
	p := testProfile()
	out := captureOutput(func() {
		code := runScript(`
s = p.stacks[0]
print(s.has_seq("Thread.run", "HashMap.put"))
`, "", nil, testTimeout, withPredeclared("p", p))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	if strings.TrimSpace(out) != "True" {
		t.Fatalf("expected True, got %q", out)
	}
}

func TestStackHasSeqWrongOrder(t *testing.T) {
	p := testProfile()
	out := captureOutput(func() {
		code := runScript(`
s = p.stacks[0]
print(s.has_seq("HashMap.put", "Server.handle"))
`, "", nil, testTimeout, withPredeclared("p", p))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	if strings.TrimSpace(out) != "False" {
		t.Fatalf("expected False, got %q", out)
	}
}

func TestStackHasSeqSingle(t *testing.T) {
	p := testProfile()
	out := captureOutput(func() {
		code := runScript(`print(p.stacks[0].has_seq("HashMap.put"))`, "", nil, testTimeout, withPredeclared("p", p))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	if strings.TrimSpace(out) != "True" {
		t.Fatalf("expected True, got %q", out)
	}
}

func TestStackAbove(t *testing.T) {
	p := testProfile()
	out := captureOutput(func() {
		code := runScript(`
s = p.stacks[0]
above = s.above("Server.handle")
print(len(above))
for f in above:
    print(f.name)
`, "", nil, testTimeout, withPredeclared("p", p))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if strings.TrimSpace(lines[0]) != "2" {
		t.Fatalf("expected 2 frames above, got %q", lines[0])
	}
	if strings.TrimSpace(lines[1]) != "Service.process" {
		t.Fatalf("expected Service.process, got %q", lines[1])
	}
	if strings.TrimSpace(lines[2]) != "HashMap.put" {
		t.Fatalf("expected HashMap.put, got %q", lines[2])
	}
}

func TestStackAboveNoMatch(t *testing.T) {
	p := testProfile()
	out := captureOutput(func() {
		code := runScript(`print(len(p.stacks[0].above("NonExistent")))`, "", nil, testTimeout, withPredeclared("p", p))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	if strings.TrimSpace(out) != "0" {
		t.Fatalf("expected 0, got %q", out)
	}
}

func TestStackBelow(t *testing.T) {
	p := testProfile()
	out := captureOutput(func() {
		code := runScript(`
s = p.stacks[0]
below = s.below("HashMap.put")
print(len(below))
for f in below:
    print(f.name)
`, "", nil, testTimeout, withPredeclared("p", p))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if strings.TrimSpace(lines[0]) != "3" {
		t.Fatalf("expected 3 frames below, got %q", lines[0])
	}
}

func TestStackBelowNoMatch(t *testing.T) {
	p := testProfile()
	out := captureOutput(func() {
		code := runScript(`print(len(p.stacks[0].below("NonExistent")))`, "", nil, testTimeout, withPredeclared("p", p))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	if strings.TrimSpace(out) != "0" {
		t.Fatalf("expected 0, got %q", out)
	}
}

func TestStackAboveBelowEdge(t *testing.T) {
	p := testProfile()
	out := captureOutput(func() {
		code := runScript(`
s = p.stacks[0]
# Match at leaf — above is empty
print(len(s.above("HashMap.put")))
# Match at root — below is empty
print(len(s.below("Thread.run")))
`, "", nil, testTimeout, withPredeclared("p", p))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if strings.TrimSpace(lines[0]) != "0" {
		t.Fatalf("expected 0 frames above leaf, got %q", lines[0])
	}
	if strings.TrimSpace(lines[1]) != "0" {
		t.Fatalf("expected 0 frames below root, got %q", lines[1])
	}
}

func TestStackMethodsEmptyStack(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{}, lines: []uint32{}, count: 1},
	})
	p := newStarlarkProfile(sf, nil, "cpu", "test")

	out := captureOutput(func() {
		code := runScript(`
s = p.stacks[0]
print(s.has("anything"))
print(len(s.above("anything")))
print(len(s.below("anything")))
`, "", nil, testTimeout, withPredeclared("p", p))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if strings.TrimSpace(lines[0]) != "False" {
		t.Fatalf("expected False for has() on empty stack, got %q", lines[0])
	}
	if strings.TrimSpace(lines[1]) != "0" {
		t.Fatalf("expected 0 for above on empty stack, got %q", lines[1])
	}
	if strings.TrimSpace(lines[2]) != "0" {
		t.Fatalf("expected 0 for below on empty stack, got %q", lines[2])
	}
}

// ---------------------------------------------------------------------------
// Increment 4 — Profile methods (hot, threads, filter, group_by)
// ---------------------------------------------------------------------------

func TestProfileHot(t *testing.T) {
	p := testProfile()
	out := captureOutput(func() {
		code := runScript(`
methods = p.hot()
print(len(methods))
print(type(methods[0]))
`, "", nil, testTimeout, withPredeclared("p", p))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if strings.TrimSpace(lines[1]) != "Method" {
		t.Fatalf("expected Method type, got %q", lines[1])
	}
}

func TestProfileHotLimit(t *testing.T) {
	p := testProfile()
	out := captureOutput(func() {
		code := runScript(`print(len(p.hot(2)))`, "", nil, testTimeout, withPredeclared("p", p))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	if strings.TrimSpace(out) != "2" {
		t.Fatalf("expected 2, got %q", out)
	}
}

func TestProfileHotFQN(t *testing.T) {
	p := testProfile()
	out := captureOutput(func() {
		code := runScript(`
m = p.hot(fqn=True)[0]
print(m.name)
`, "", nil, testTimeout, withPredeclared("p", p))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	name := strings.TrimSpace(out)
	// FQN should contain dots (package separator).
	if !strings.Contains(name, ".") {
		t.Fatalf("expected FQN with dots, got %q", name)
	}
}

func TestProfileHotEmpty(t *testing.T) {
	sf := makeStackFile(nil)
	p := newStarlarkProfile(sf, nil, "cpu", "test")
	out := captureOutput(func() {
		code := runScript(`print(len(p.hot()))`, "", nil, testTimeout, withPredeclared("p", p))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	if strings.TrimSpace(out) != "0" {
		t.Fatalf("expected 0, got %q", out)
	}
}

func TestMethodFields(t *testing.T) {
	p := testProfile()
	out := captureOutput(func() {
		code := runScript(`
m = p.hot()[0]
print(type(m.name))
print(type(m.fqn))
print(type(m.self))
print(type(m.self_pct))
print(type(m.total))
print(type(m.total_pct))
print(m.self > 0)
print(m.self_pct > 0.0)
`, "", nil, testTimeout, withPredeclared("p", p))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	expected := []string{"string", "string", "int", "float", "int", "float", "True", "True"}
	for i, exp := range expected {
		if i >= len(lines) {
			t.Fatalf("expected %d lines, got %d", len(expected), len(lines))
		}
		if strings.TrimSpace(lines[i]) != exp {
			t.Fatalf("line %d: expected %q, got %q", i, exp, lines[i])
		}
	}
}

func TestProfileThreads(t *testing.T) {
	p := testProfile()
	out := captureOutput(func() {
		code := runScript(`
threads = p.threads()
for th in threads:
    print(th.name, th.samples)
`, "", nil, testTimeout, withPredeclared("p", p))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	if !strings.Contains(out, "worker-1") || !strings.Contains(out, "worker-2") {
		t.Fatalf("expected worker threads, got %q", out)
	}
}

func TestProfileThreadsLimit(t *testing.T) {
	p := testProfile()
	out := captureOutput(func() {
		code := runScript(`print(len(p.threads(1)))`, "", nil, testTimeout, withPredeclared("p", p))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	if strings.TrimSpace(out) != "1" {
		t.Fatalf("expected 1, got %q", out)
	}
}

func TestThreadFields(t *testing.T) {
	p := testProfile()
	out := captureOutput(func() {
		code := runScript(`
th = p.threads()[0]
print(type(th.name))
print(type(th.samples))
print(type(th.pct))
`, "", nil, testTimeout, withPredeclared("p", p))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	expected := []string{"string", "int", "float"}
	for i, exp := range expected {
		if i >= len(lines) {
			t.Fatalf("expected %d lines, got %d", len(expected), len(lines))
		}
		if strings.TrimSpace(lines[i]) != exp {
			t.Fatalf("line %d: expected %q, got %q", i, exp, lines[i])
		}
	}
}

func TestProfileFilter(t *testing.T) {
	p := testProfile()
	out := captureOutput(func() {
		code := runScript(`
filtered = p.filter(lambda s: s.has("HashMap"))
print(filtered.samples)
print(p.samples)
`, "", nil, testTimeout, withPredeclared("p", p))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if strings.TrimSpace(lines[0]) != "10" {
		t.Fatalf("expected filtered samples 10, got %q", lines[0])
	}
	if strings.TrimSpace(lines[1]) != "18" {
		t.Fatalf("expected original samples 18, got %q", lines[1])
	}
}

func TestProfileFilterChain(t *testing.T) {
	p := testProfile()
	out := captureOutput(func() {
		code := runScript(`
result = p.filter(lambda s: "worker" in s.thread).filter(lambda s: s.has("Server"))
print(result.samples)
`, "", nil, testTimeout, withPredeclared("p", p))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	// Both worker stacks have Server.handle.
	if strings.TrimSpace(out) != "15" {
		t.Fatalf("expected 15, got %q", out)
	}
}

func TestProfileFilterAll(t *testing.T) {
	p := testProfile()
	out := captureOutput(func() {
		code := runScript(`
result = p.filter(lambda s: s.has("NonExistent"))
print(result.samples)
`, "", nil, testTimeout, withPredeclared("p", p))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	if strings.TrimSpace(out) != "0" {
		t.Fatalf("expected 0, got %q", out)
	}
}

func TestProfileFilterPreserves(t *testing.T) {
	p := testProfile()
	out := captureOutput(func() {
		code := runScript(`
_ = p.filter(lambda s: s.has("HashMap"))
print(p.samples)
`, "", nil, testTimeout, withPredeclared("p", p))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	if strings.TrimSpace(out) != "18" {
		t.Fatalf("expected original unchanged at 18, got %q", out)
	}
}

func TestProfileGroupBy(t *testing.T) {
	p := testProfile()
	out := captureOutput(func() {
		code := runScript(`
groups = p.group_by(lambda s: s.thread if s.thread else None)
for name in sorted(groups.keys()):
    print(name, groups[name].samples)
`, "", nil, testTimeout, withPredeclared("p", p))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	if !strings.Contains(out, "worker-1 10") {
		t.Fatalf("expected worker-1 with 10 samples, got %q", out)
	}
	if !strings.Contains(out, "worker-2 5") {
		t.Fatalf("expected worker-2 with 5 samples, got %q", out)
	}
}

func TestProfileGroupByNone(t *testing.T) {
	p := testProfile()
	out := captureOutput(func() {
		code := runScript(`
groups = p.group_by(lambda s: s.thread if s.thread else None)
print(len(groups))
`, "", nil, testTimeout, withPredeclared("p", p))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	// __do_softirq has empty thread → should be excluded
	if strings.TrimSpace(out) != "2" {
		t.Fatalf("expected 2 groups (no empty thread), got %q", out)
	}
}

// ---------------------------------------------------------------------------
// Increment 5 — emit(), match(), emit_all()
// ---------------------------------------------------------------------------

func TestEmit(t *testing.T) {
	p := testProfile()
	out := captureOutput(func() {
		code := runScript(`emit(p.stacks[0])`, "", nil, testTimeout, withPredeclared("p", p))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	line := strings.TrimSpace(out)
	if !strings.HasPrefix(line, "[worker-1];") {
		t.Fatalf("expected thread prefix, got %q", line)
	}
	if !strings.HasSuffix(line, " 10") {
		t.Fatalf("expected count 10, got %q", line)
	}
	if !strings.Contains(line, "HashMap.put") {
		t.Fatalf("expected HashMap.put in output, got %q", line)
	}
}

func TestEmitThread(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A", "B"}, lines: []uint32{0, 0}, count: 3, thread: "my-thread"},
	})
	p := newStarlarkProfile(sf, nil, "cpu", "test")
	out := captureOutput(func() {
		code := runScript(`emit(p.stacks[0])`, "", nil, testTimeout, withPredeclared("p", p))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	if strings.TrimSpace(out) != "[my-thread];A;B 3" {
		t.Fatalf("expected '[my-thread];A;B 3', got %q", out)
	}
}

func TestEmitNoThread(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A", "B"}, lines: []uint32{0, 0}, count: 7, thread: ""},
	})
	p := newStarlarkProfile(sf, nil, "cpu", "test")
	out := captureOutput(func() {
		code := runScript(`emit(p.stacks[0])`, "", nil, testTimeout, withPredeclared("p", p))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	if strings.TrimSpace(out) != "A;B 7" {
		t.Fatalf("expected 'A;B 7', got %q", out)
	}
}

func TestEmitAll(t *testing.T) {
	p := testProfile()
	out := captureOutput(func() {
		code := runScript(`emit_all(p)`, "", nil, testTimeout, withPredeclared("p", p))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines (3 stacks), got %d: %q", len(lines), out)
	}
}

func TestMatch(t *testing.T) {
	out := captureOutput(func() {
		code := runScript(`print(match("com.example.Service", "example\\..*"))`, "", nil, testTimeout)
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	if strings.TrimSpace(out) != "True" {
		t.Fatalf("expected True, got %q", out)
	}
}

func TestMatchNoMatch(t *testing.T) {
	out := captureOutput(func() {
		code := runScript(`print(match("com.example.Service", "^foo"))`, "", nil, testTimeout)
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	if strings.TrimSpace(out) != "False" {
		t.Fatalf("expected False, got %q", out)
	}
}

func TestMatchInvalidRegex(t *testing.T) {
	stderr := captureStream(&os.Stderr, func() {
		code := runScript(`match("test", "[invalid")`, "", nil, testTimeout)
		if code == 0 {
			t.Fatalf("expected error for invalid regex")
		}
	})
	if !strings.Contains(stderr, "error") {
		t.Fatalf("expected error in stderr, got %q", stderr)
	}
}

func TestPipeline(t *testing.T) {
	p := testProfile()
	out := captureOutput(func() {
		code := runScript(`
for s in p.stacks:
    if s.has("HashMap"):
        emit(s)
`, "", nil, testTimeout, withPredeclared("p", p))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	// Parse the emitted collapsed output.
	line := strings.TrimSpace(out)
	if !strings.Contains(line, "HashMap.put") {
		t.Fatalf("expected HashMap.put in emitted output, got %q", line)
	}
	if !strings.HasSuffix(line, " 10") {
		t.Fatalf("expected count suffix, got %q", line)
	}
}

// ---------------------------------------------------------------------------
// Increment 6 — diff()
// ---------------------------------------------------------------------------

func TestDiff(t *testing.T) {
	before := makeStackFile([]stack{
		{frames: []string{"A", "B"}, lines: []uint32{0, 0}, count: 80, thread: "t1"},
		{frames: []string{"A", "C"}, lines: []uint32{0, 0}, count: 20, thread: "t1"},
	})
	after := makeStackFile([]stack{
		{frames: []string{"A", "B"}, lines: []uint32{0, 0}, count: 60, thread: "t1"},
		{frames: []string{"A", "C"}, lines: []uint32{0, 0}, count: 40, thread: "t1"},
	})
	bProf := newStarlarkProfile(before, nil, "cpu", "before")
	aProf := newStarlarkProfile(after, nil, "cpu", "after")
	out := captureOutput(func() {
		code := runScript(`
d = diff(a, b)
print(len(d.all))
`, "", nil, testTimeout, withPredeclared("a", bProf), withPredeclared("b", aProf))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	n := strings.TrimSpace(out)
	if n == "0" {
		t.Fatalf("expected some diff entries, got 0")
	}
}

func TestDiffRegressions(t *testing.T) {
	before := makeStackFile([]stack{
		{frames: []string{"A"}, lines: []uint32{0}, count: 90},
		{frames: []string{"B"}, lines: []uint32{0}, count: 10},
	})
	after := makeStackFile([]stack{
		{frames: []string{"A"}, lines: []uint32{0}, count: 70},
		{frames: []string{"B"}, lines: []uint32{0}, count: 30},
	})
	bProf := newStarlarkProfile(before, nil, "cpu", "before")
	aProf := newStarlarkProfile(after, nil, "cpu", "after")
	out := captureOutput(func() {
		code := runScript(`
d = diff(a, b)
for e in d.regressions:
    print(e.name, e.delta > 0)
`, "", nil, testTimeout, withPredeclared("a", bProf), withPredeclared("b", aProf))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	if !strings.Contains(out, "True") {
		t.Fatalf("expected regression with positive delta, got %q", out)
	}
}

func TestDiffImprovements(t *testing.T) {
	before := makeStackFile([]stack{
		{frames: []string{"A"}, lines: []uint32{0}, count: 90},
		{frames: []string{"B"}, lines: []uint32{0}, count: 10},
	})
	after := makeStackFile([]stack{
		{frames: []string{"A"}, lines: []uint32{0}, count: 70},
		{frames: []string{"B"}, lines: []uint32{0}, count: 30},
	})
	bProf := newStarlarkProfile(before, nil, "cpu", "before")
	aProf := newStarlarkProfile(after, nil, "cpu", "after")
	out := captureOutput(func() {
		code := runScript(`
d = diff(a, b)
for e in d.improvements:
    print(e.name, e.delta < 0)
`, "", nil, testTimeout, withPredeclared("a", bProf), withPredeclared("b", aProf))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	if !strings.Contains(out, "True") {
		t.Fatalf("expected improvement with negative delta, got %q", out)
	}
}

func TestDiffAdded(t *testing.T) {
	before := makeStackFile([]stack{
		{frames: []string{"A"}, lines: []uint32{0}, count: 100},
	})
	after := makeStackFile([]stack{
		{frames: []string{"A"}, lines: []uint32{0}, count: 90},
		{frames: []string{"NewMethod"}, lines: []uint32{0}, count: 10},
	})
	bProf := newStarlarkProfile(before, nil, "cpu", "before")
	aProf := newStarlarkProfile(after, nil, "cpu", "after")
	out := captureOutput(func() {
		code := runScript(`
d = diff(a, b)
for e in d.added:
    print(e.name)
`, "", nil, testTimeout, withPredeclared("a", bProf), withPredeclared("b", aProf))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	if !strings.Contains(out, "NewMethod") {
		t.Fatalf("expected NewMethod in added, got %q", out)
	}
}

func TestDiffRemoved(t *testing.T) {
	before := makeStackFile([]stack{
		{frames: []string{"A"}, lines: []uint32{0}, count: 90},
		{frames: []string{"OldMethod"}, lines: []uint32{0}, count: 10},
	})
	after := makeStackFile([]stack{
		{frames: []string{"A"}, lines: []uint32{0}, count: 100},
	})
	bProf := newStarlarkProfile(before, nil, "cpu", "before")
	aProf := newStarlarkProfile(after, nil, "cpu", "after")
	out := captureOutput(func() {
		code := runScript(`
d = diff(a, b)
for e in d.removed:
    print(e.name)
`, "", nil, testTimeout, withPredeclared("a", bProf), withPredeclared("b", aProf))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	if !strings.Contains(out, "OldMethod") {
		t.Fatalf("expected OldMethod in removed, got %q", out)
	}
}

func TestDiffAll(t *testing.T) {
	before := makeStackFile([]stack{
		{frames: []string{"A"}, lines: []uint32{0}, count: 90},
		{frames: []string{"B"}, lines: []uint32{0}, count: 10},
	})
	after := makeStackFile([]stack{
		{frames: []string{"A"}, lines: []uint32{0}, count: 70},
		{frames: []string{"B"}, lines: []uint32{0}, count: 30},
	})
	bProf := newStarlarkProfile(before, nil, "cpu", "before")
	aProf := newStarlarkProfile(after, nil, "cpu", "after")
	out := captureOutput(func() {
		code := runScript(`
d = diff(a, b)
for e in d.all:
    print(e.name)
`, "", nil, testTimeout, withPredeclared("a", bProf), withPredeclared("b", aProf))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected at least 2 entries in all, got %d", len(lines))
	}
}

func TestDiffMinDelta(t *testing.T) {
	before := makeStackFile([]stack{
		{frames: []string{"A"}, lines: []uint32{0}, count: 50},
		{frames: []string{"B"}, lines: []uint32{0}, count: 50},
	})
	after := makeStackFile([]stack{
		{frames: []string{"A"}, lines: []uint32{0}, count: 49},
		{frames: []string{"B"}, lines: []uint32{0}, count: 51},
	})
	bProf := newStarlarkProfile(before, nil, "cpu", "before")
	aProf := newStarlarkProfile(after, nil, "cpu", "after")
	out := captureOutput(func() {
		code := runScript(`
d = diff(a, b, min_delta=5.0)
print(len(d.all))
`, "", nil, testTimeout, withPredeclared("a", bProf), withPredeclared("b", aProf))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	if strings.TrimSpace(out) != "0" {
		t.Fatalf("expected 0 entries with high min_delta, got %q", out)
	}
}

func TestDiffEntryFields(t *testing.T) {
	before := makeStackFile([]stack{
		{frames: []string{"A"}, lines: []uint32{0}, count: 90},
		{frames: []string{"B"}, lines: []uint32{0}, count: 10},
	})
	after := makeStackFile([]stack{
		{frames: []string{"A"}, lines: []uint32{0}, count: 70},
		{frames: []string{"B"}, lines: []uint32{0}, count: 30},
	})
	bProf := newStarlarkProfile(before, nil, "cpu", "before")
	aProf := newStarlarkProfile(after, nil, "cpu", "after")
	out := captureOutput(func() {
		code := runScript(`
d = diff(a, b)
e = d.all[0]
print(type(e.name))
print(type(e.fqn))
print(type(e.before))
print(type(e.after))
print(type(e.delta))
`, "", nil, testTimeout, withPredeclared("a", bProf), withPredeclared("b", aProf))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	expected := []string{"string", "string", "float", "float", "float"}
	for i, exp := range expected {
		if i >= len(lines) {
			t.Fatalf("expected %d lines, got %d", len(expected), len(lines))
		}
		if strings.TrimSpace(lines[i]) != exp {
			t.Fatalf("field %d: expected %q, got %q", i, exp, lines[i])
		}
	}
}

func TestDiffSameProfile(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A"}, lines: []uint32{0}, count: 50},
		{frames: []string{"B"}, lines: []uint32{0}, count: 50},
	})
	p := newStarlarkProfile(sf, nil, "cpu", "same")
	out := captureOutput(func() {
		code := runScript(`
d = diff(a, b)
print(len(d.all))
`, "", nil, testTimeout, withPredeclared("a", p), withPredeclared("b", p))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	if strings.TrimSpace(out) != "0" {
		t.Fatalf("expected 0 changes for same profile, got %q", out)
	}
}

// ---------------------------------------------------------------------------
// Increment 7 — timeline(), split(), Bucket
// ---------------------------------------------------------------------------

func TestTimeline(t *testing.T) {
	out := captureOutput(func() {
		code := runScript(fmt.Sprintf(`
p = open(%q)
buckets = p.timeline()
print(len(buckets))
if len(buckets) > 0:
    b = buckets[0]
    print(type(b))
    print(b.start >= 0)
    print(b.end > b.start)
    print(b.samples >= 0)
`, scriptFixture("cpu.jfr")), "", nil, testTimeout)
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 1 {
		t.Fatalf("expected output, got %q", out)
	}
	n := strings.TrimSpace(lines[0])
	if n == "0" {
		t.Fatalf("expected buckets > 0")
	}
	if len(lines) >= 2 && strings.TrimSpace(lines[1]) != "Bucket" {
		t.Fatalf("expected Bucket type, got %q", lines[1])
	}
}

func TestTimelineResolution(t *testing.T) {
	out := captureOutput(func() {
		code := runScript(fmt.Sprintf(`
p = open(%q)
buckets = p.timeline(resolution="1s")
print(len(buckets))
`, scriptFixture("cpu.jfr")), "", nil, testTimeout)
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	n := strings.TrimSpace(out)
	if n == "0" {
		t.Fatalf("expected buckets > 0")
	}
}

func TestTimelineBuckets(t *testing.T) {
	out := captureOutput(func() {
		code := runScript(fmt.Sprintf(`
p = open(%q)
buckets = p.timeline(buckets=5)
print(len(buckets))
`, scriptFixture("cpu.jfr")), "", nil, testTimeout)
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	if strings.TrimSpace(out) != "5" {
		t.Fatalf("expected 5 buckets, got %q", out)
	}
}

func TestTimelineCollapsed(t *testing.T) {
	out := captureOutput(func() {
		code := runScript(fmt.Sprintf(`
p = open(%q)
buckets = p.timeline()
print(len(buckets))
`, scriptFixture("perf.collapsed")), "", nil, testTimeout)
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	if strings.TrimSpace(out) != "0" {
		t.Fatalf("expected 0 buckets for collapsed, got %q", out)
	}
}

func TestBucketHot(t *testing.T) {
	out := captureOutput(func() {
		code := runScript(fmt.Sprintf(`
p = open(%q)
buckets = p.timeline(buckets=5)
for b in buckets:
    if b.samples > 0:
        top = b.hot(1)
        if len(top) > 0:
            print(top[0].name)
            break
`, scriptFixture("cpu.jfr")), "", nil, testTimeout)
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	if strings.TrimSpace(out) == "" {
		t.Fatalf("expected a hot method name, got empty")
	}
}

func TestBucketStacks(t *testing.T) {
	out := captureOutput(func() {
		code := runScript(fmt.Sprintf(`
p = open(%q)
buckets = p.timeline(buckets=5)
for b in buckets:
    if b.samples > 0:
        print(len(b.stacks))
        break
`, scriptFixture("cpu.jfr")), "", nil, testTimeout)
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	n := strings.TrimSpace(out)
	if n == "" || n == "0" {
		t.Fatalf("expected stacks > 0 in non-empty bucket, got %q", n)
	}
}

func TestBucketFields(t *testing.T) {
	out := captureOutput(func() {
		code := runScript(fmt.Sprintf(`
p = open(%q)
buckets = p.timeline(buckets=5)
b = buckets[0]
print(type(b.start))
print(type(b.end))
print(type(b.samples))
print(type(b.stacks))
`, scriptFixture("cpu.jfr")), "", nil, testTimeout)
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	expected := []string{"float", "float", "int", "list"}
	for i, exp := range expected {
		if i >= len(lines) {
			t.Fatalf("expected %d lines, got %d", len(expected), len(lines))
		}
		if strings.TrimSpace(lines[i]) != exp {
			t.Fatalf("field %d: expected %q, got %q", i, exp, lines[i])
		}
	}
}

func TestSplit(t *testing.T) {
	out := captureOutput(func() {
		code := runScript(fmt.Sprintf(`
p = open(%q)
parts = p.split([5.0])
print(len(parts))
print(type(parts[0]))
total = 0
for part in parts:
    total += part.samples
print(total == p.samples)
`, scriptFixture("cpu.jfr")), "", nil, testTimeout)
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if strings.TrimSpace(lines[0]) != "2" {
		t.Fatalf("expected 2 parts, got %q", lines[0])
	}
	if strings.TrimSpace(lines[1]) != "Profile" {
		t.Fatalf("expected Profile type, got %q", lines[1])
	}
	if strings.TrimSpace(lines[2]) != "True" {
		t.Fatalf("expected split to preserve total samples, got %q", lines[2])
	}
}

func TestSplitMultiple(t *testing.T) {
	out := captureOutput(func() {
		code := runScript(fmt.Sprintf(`
p = open(%q)
parts = p.split([5.0, 10.0])
print(len(parts))
`, scriptFixture("cpu.jfr")), "", nil, testTimeout)
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	if strings.TrimSpace(out) != "3" {
		t.Fatalf("expected 3 parts, got %q", out)
	}
}

func TestSplitCollapsed(t *testing.T) {
	out := captureOutput(func() {
		code := runScript(fmt.Sprintf(`
p = open(%q)
parts = p.split([5.0])
print(len(parts))
`, scriptFixture("perf.collapsed")), "", nil, testTimeout)
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	if strings.TrimSpace(out) != "1" {
		t.Fatalf("expected 1 part for collapsed, got %q", out)
	}
}

func TestSplitUnsorted(t *testing.T) {
	stderr := captureStream(&os.Stderr, func() {
		code := runScript(fmt.Sprintf(`p = open(%q); p.split([10.0, 5.0])`, scriptFixture("cpu.jfr")), "", nil, testTimeout)
		if code == 0 {
			t.Fatalf("expected error for unsorted split times")
		}
	})
	if !strings.Contains(stderr, "strictly increasing") {
		t.Fatalf("expected 'strictly increasing' error, got %q", stderr)
	}
}

func TestSplitNegative(t *testing.T) {
	stderr := captureStream(&os.Stderr, func() {
		code := runScript(fmt.Sprintf(`p = open(%q); p.split([-1.0])`, scriptFixture("cpu.jfr")), "", nil, testTimeout)
		if code == 0 {
			t.Fatalf("expected error for negative split time")
		}
	})
	if !strings.Contains(stderr, "non-negative") {
		t.Fatalf("expected 'non-negative' error, got %q", stderr)
	}
}

func TestSplitDuplicate(t *testing.T) {
	stderr := captureStream(&os.Stderr, func() {
		code := runScript(fmt.Sprintf(`p = open(%q); p.split([5.0, 5.0])`, scriptFixture("cpu.jfr")), "", nil, testTimeout)
		if code == 0 {
			t.Fatalf("expected error for duplicate split times")
		}
	})
	if !strings.Contains(stderr, "strictly increasing") {
		t.Fatalf("expected 'strictly increasing' error, got %q", stderr)
	}
}

func TestTimelineCached(t *testing.T) {
	out := captureOutput(func() {
		code := runScript(fmt.Sprintf(`
p = open(%q)
b1 = p.timeline(buckets=5)
b2 = p.timeline(buckets=5)
print(len(b1))
print(len(b2))
`, scriptFixture("cpu.jfr")), "", nil, testTimeout)
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 || strings.TrimSpace(lines[0]) != strings.TrimSpace(lines[1]) {
		t.Fatalf("expected same bucket count on both calls, got %q", out)
	}
}

func TestTimelineFilteredProfile(t *testing.T) {
	// Build a profile with 2 timed stacks on different threads.
	sf := makeStackFile([]stack{
		{frames: []string{"A", "B"}, lines: []uint32{0, 0}, count: 5, thread: "worker"},
		{frames: []string{"C", "D"}, lines: []uint32{0, 0}, count: 3, thread: "main"},
	})
	timed := &parsedJFR{
		eventCounts:   map[string]int{"cpu": 8},
		stacksByEvent: map[string]*stackFile{"cpu": sf},
		timedEvents: map[string][]timedEvent{
			"cpu": {
				{offsetNanos: 1e9, stackKey: "A;B", frames: []string{"A", "B"}, lines: []uint32{0, 0}, thread: "worker", weight: 2},
				{offsetNanos: 2e9, stackKey: "C;D", frames: []string{"C", "D"}, lines: []uint32{0, 0}, thread: "main", weight: 3},
				{offsetNanos: 3e9, stackKey: "A;B", frames: []string{"A", "B"}, lines: []uint32{0, 0}, thread: "worker", weight: 3},
			},
		},
		spanNanos: 4e9,
	}

	// Filter to worker thread only (5 samples).
	filteredSf := makeStackFile([]stack{
		{frames: []string{"A", "B"}, lines: []uint32{0, 0}, count: 5, thread: "worker"},
	})
	filtered := &starlarkProfile{sf: filteredSf, parsed: timed, timedParsed: timed, event: "cpu", path: "test.jfr"}

	out := captureOutput(func() {
		code := runScript(`
total = 0
for b in p.timeline(buckets=2):
    total += b.samples
print(total)
print(p.samples)
print(total == p.samples)
`, "", nil, testTimeout, withPredeclared("p", filtered))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 3 {
		t.Fatalf("expected 3 lines, got %q", out)
	}
	if strings.TrimSpace(lines[2]) != "True" {
		t.Fatalf("timeline samples (%s) should equal profile samples (%s)", lines[0], lines[1])
	}
}

func TestSplitFilteredProfile(t *testing.T) {
	// Same setup: 2 stacks, filter to 1, then split.
	sf := makeStackFile([]stack{
		{frames: []string{"A", "B"}, lines: []uint32{0, 0}, count: 5, thread: "worker"},
		{frames: []string{"C", "D"}, lines: []uint32{0, 0}, count: 3, thread: "main"},
	})
	timed := &parsedJFR{
		eventCounts:   map[string]int{"cpu": 8},
		stacksByEvent: map[string]*stackFile{"cpu": sf},
		timedEvents: map[string][]timedEvent{
			"cpu": {
				{offsetNanos: 1e9, stackKey: "A;B", frames: []string{"A", "B"}, lines: []uint32{0, 0}, thread: "worker", weight: 2},
				{offsetNanos: 2e9, stackKey: "C;D", frames: []string{"C", "D"}, lines: []uint32{0, 0}, thread: "main", weight: 3},
				{offsetNanos: 3e9, stackKey: "A;B", frames: []string{"A", "B"}, lines: []uint32{0, 0}, thread: "worker", weight: 3},
			},
		},
		spanNanos: 4e9,
	}

	filteredSf := makeStackFile([]stack{
		{frames: []string{"A", "B"}, lines: []uint32{0, 0}, count: 5, thread: "worker"},
	})
	filtered := &starlarkProfile{sf: filteredSf, parsed: timed, timedParsed: timed, event: "cpu", path: "test.jfr"}

	out := captureOutput(func() {
		code := runScript(`
parts = p.split([2.5])
total = 0
for part in parts:
    total += part.samples
print(total)
print(p.samples)
print(total == p.samples)
`, "", nil, testTimeout, withPredeclared("p", filtered))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 3 {
		t.Fatalf("expected 3 lines, got %q", out)
	}
	if strings.TrimSpace(lines[2]) != "True" {
		t.Fatalf("split samples (%s) should equal profile samples (%s)", lines[0], lines[1])
	}
}

func TestSplitThenTimeline(t *testing.T) {
	// Same (stackKey, thread) appears in both early [0,5s) and late [5s,10s) windows.
	// After split, parts[1].timeline() must only show samples in the late window.
	sf := makeStackFile([]stack{
		{frames: []string{"A", "B"}, lines: []uint32{0, 0}, count: 6, thread: "t1"},
	})
	timed := &parsedJFR{
		eventCounts:   map[string]int{"cpu": 6},
		stacksByEvent: map[string]*stackFile{"cpu": sf},
		timedEvents: map[string][]timedEvent{
			"cpu": {
				// 3 events in [0, 5s)
				{offsetNanos: 1e9, stackKey: "A;B", frames: []string{"A", "B"}, lines: []uint32{0, 0}, thread: "t1", weight: 1},
				{offsetNanos: 2e9, stackKey: "A;B", frames: []string{"A", "B"}, lines: []uint32{0, 0}, thread: "t1", weight: 1},
				{offsetNanos: 3e9, stackKey: "A;B", frames: []string{"A", "B"}, lines: []uint32{0, 0}, thread: "t1", weight: 1},
				// 3 events in [5s, 10s)
				{offsetNanos: 6e9, stackKey: "A;B", frames: []string{"A", "B"}, lines: []uint32{0, 0}, thread: "t1", weight: 1},
				{offsetNanos: 7e9, stackKey: "A;B", frames: []string{"A", "B"}, lines: []uint32{0, 0}, thread: "t1", weight: 1},
				{offsetNanos: 8e9, stackKey: "A;B", frames: []string{"A", "B"}, lines: []uint32{0, 0}, thread: "t1", weight: 1},
			},
		},
		spanNanos: 10e9,
	}
	p := newStarlarkProfile(sf, timed, "cpu", "test.jfr")
	p.timedParsed = timed

	out := captureOutput(func() {
		code := runScript(`
parts = p.split([5.0])
# parts[1] should have 3 samples, all in [5s, 10s)
print(parts[1].samples)

# Timeline with 2 buckets: [5s,7.5s) and [7.5s,10s)
buckets = parts[1].timeline(buckets=2)
for b in buckets:
    print(b.samples)
`, "", nil, testTimeout, withPredeclared("p", p))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 3 {
		t.Fatalf("expected 3 lines, got %q", out)
	}
	// parts[1] should have exactly 3 samples.
	if strings.TrimSpace(lines[0]) != "3" {
		t.Fatalf("expected 3 samples in parts[1], got %q", lines[0])
	}
	// 2 buckets span the full recording [0,10s): [0,5s) and [5s,10s).
	// All 3 late events (6s,7s,8s) must land in bucket 1 (the late half).
	// The bug would put them in bucket 0 (timestamps remapped to 1s,2s,3s).
	b0 := strings.TrimSpace(lines[1])
	b1 := strings.TrimSpace(lines[2])
	if b0 != "0" || b1 != "3" {
		t.Fatalf("expected buckets [0, 3], got [%s, %s]", b0, b1)
	}
}

func TestSplitFilterTimeline(t *testing.T) {
	// Composition: split → filter → timeline must preserve temporal scope.
	sf := makeStackFile([]stack{
		{frames: []string{"A", "B"}, lines: []uint32{0, 0}, count: 4, thread: "t1"},
		{frames: []string{"C", "D"}, lines: []uint32{0, 0}, count: 4, thread: "t1"},
	})
	timed := &parsedJFR{
		eventCounts:   map[string]int{"cpu": 8},
		stacksByEvent: map[string]*stackFile{"cpu": sf},
		timedEvents: map[string][]timedEvent{
			"cpu": {
				{offsetNanos: 1e9, stackKey: "A;B", frames: []string{"A", "B"}, lines: []uint32{0, 0}, thread: "t1", weight: 1},
				{offsetNanos: 2e9, stackKey: "C;D", frames: []string{"C", "D"}, lines: []uint32{0, 0}, thread: "t1", weight: 1},
				{offsetNanos: 3e9, stackKey: "A;B", frames: []string{"A", "B"}, lines: []uint32{0, 0}, thread: "t1", weight: 1},
				{offsetNanos: 4e9, stackKey: "C;D", frames: []string{"C", "D"}, lines: []uint32{0, 0}, thread: "t1", weight: 1},
				// After 5s boundary
				{offsetNanos: 6e9, stackKey: "A;B", frames: []string{"A", "B"}, lines: []uint32{0, 0}, thread: "t1", weight: 1},
				{offsetNanos: 7e9, stackKey: "C;D", frames: []string{"C", "D"}, lines: []uint32{0, 0}, thread: "t1", weight: 1},
				{offsetNanos: 8e9, stackKey: "A;B", frames: []string{"A", "B"}, lines: []uint32{0, 0}, thread: "t1", weight: 1},
				{offsetNanos: 9e9, stackKey: "C;D", frames: []string{"C", "D"}, lines: []uint32{0, 0}, thread: "t1", weight: 1},
			},
		},
		spanNanos: 10e9,
	}
	p := newStarlarkProfile(sf, timed, "cpu", "test.jfr")
	p.timedParsed = timed

	out := captureOutput(func() {
		code := runScript(`
parts = p.split([5.0])
# parts[1] has 4 events (6s,7s,8s,9s), filter to A;B only → 2 events (6s,8s)
filtered = parts[1].filter(lambda s: s.has("A"))
print(filtered.samples)

# Timeline should have 2 samples total, not pulled from [0,5s)
total = 0
for b in filtered.timeline(buckets=2):
    total += b.samples
print(total)
print(total == filtered.samples)
`, "", nil, testTimeout, withPredeclared("p", p))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 3 {
		t.Fatalf("expected 3 lines, got %q", out)
	}
	if strings.TrimSpace(lines[0]) != "2" {
		t.Fatalf("expected 2 filtered samples, got %q", lines[0])
	}
	if strings.TrimSpace(lines[2]) != "True" {
		t.Fatalf("timeline total (%s) should equal filtered samples (%s)", lines[1], lines[0])
	}
}

// ---------------------------------------------------------------------------
// Increment 8 — CLI integration tests
// ---------------------------------------------------------------------------

func TestCLIScript(t *testing.T) {
	code, stdout, _ := runCLIForTest(t, []string{"script", "-c", `print("cli-hello")`}, nil)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if !strings.Contains(stdout, "cli-hello") {
		t.Fatalf("expected 'cli-hello', got %q", stdout)
	}
}

func TestCLIScriptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.star")
	os.WriteFile(path, []byte(`print("file-hello")`), 0644)

	code, stdout, _ := runCLIForTest(t, []string{"script", path}, nil)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if !strings.Contains(stdout, "file-hello") {
		t.Fatalf("expected 'file-hello', got %q", stdout)
	}
}

func TestCLIScriptMissing(t *testing.T) {
	code, _, stderr := runCLIForTest(t, []string{"script"}, nil)
	if code == 0 {
		t.Fatalf("expected non-zero exit code for missing script")
	}
	if !strings.Contains(stderr, "error") {
		t.Fatalf("expected error in stderr, got %q", stderr)
	}
}

func TestCLIScriptArgs(t *testing.T) {
	code, stdout, _ := runCLIForTest(t, []string{"script", "-c", `print(ARGS[0])`, "--", "hello"}, nil)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if !strings.Contains(stdout, "hello") {
		t.Fatalf("expected 'hello', got %q", stdout)
	}
}

func TestCLIScriptHelp(t *testing.T) {
	code, stdout, _ := runCLIForTest(t, []string{"script", "--help"}, nil)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	// Check key sections are present.
	for _, want := range []string{"Usage:", "Functions:", "Types:", "Profile", "Stack", "Frame", "Examples:"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("expected help to contain %q, got %q", want, stdout)
		}
	}
}

func TestCLIScriptHelpShort(t *testing.T) {
	code, stdout, _ := runCLIForTest(t, []string{"script", "-h"}, nil)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if !strings.Contains(stdout, "Usage:") {
		t.Fatalf("expected help output, got %q", stdout)
	}
}

func TestCLIScriptHelpWithOtherFlags(t *testing.T) {
	// --help should take precedence even if other flags are present
	code, stdout, _ := runCLIForTest(t, []string{"script", "-c", "print('xyzzy-marker')", "--help"}, nil)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if !strings.Contains(stdout, "Usage:") {
		t.Fatalf("expected help output, got %q", stdout)
	}
	if strings.Contains(stdout, "xyzzy-marker") {
		t.Fatalf("script should not have executed when --help is present")
	}
}

func TestCLIScriptOpenAndHot(t *testing.T) {
	code, stdout, _ := runCLIForTest(t, []string{
		"script", "-c", fmt.Sprintf(`
p = open(%q)
for m in p.hot(3):
    print(m.name)
`, scriptFixture("cpu.jfr")),
	}, nil)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if strings.TrimSpace(stdout) == "" {
		t.Fatalf("expected hot method output, got empty")
	}
}

// ---------------------------------------------------------------------------
// Bucket.profile
// ---------------------------------------------------------------------------

func TestBucketProfile(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A", "B"}, lines: []uint32{0, 0}, count: 5, thread: "t1"},
		{frames: []string{"C", "D"}, lines: []uint32{0, 0}, count: 3, thread: "t1"},
	})
	timed := &parsedJFR{
		eventCounts:   map[string]int{"cpu": 8},
		stacksByEvent: map[string]*stackFile{"cpu": sf},
		timedEvents: map[string][]timedEvent{
			"cpu": {
				{offsetNanos: 1e9, stackKey: "A;B", frames: []string{"A", "B"}, lines: []uint32{0, 0}, thread: "t1", weight: 3},
				{offsetNanos: 2e9, stackKey: "C;D", frames: []string{"C", "D"}, lines: []uint32{0, 0}, thread: "t1", weight: 2},
				{offsetNanos: 3e9, stackKey: "A;B", frames: []string{"A", "B"}, lines: []uint32{0, 0}, thread: "t1", weight: 2},
				{offsetNanos: 4e9, stackKey: "C;D", frames: []string{"C", "D"}, lines: []uint32{0, 0}, thread: "t1", weight: 1},
			},
		},
		spanNanos: 5e9,
	}
	p := newStarlarkProfile(sf, timed, "cpu", "test.jfr")
	p.timedParsed = timed

	out := captureOutput(func() {
		code := runScript(`
buckets = p.timeline(buckets=2)
for b in buckets:
    if b.samples > 0:
        bp = b.profile
        print(type(bp))
        print(bp.samples == b.samples)
        break
`, "", nil, testTimeout, withPredeclared("p", p))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected 2 lines, got %q", out)
	}
	if strings.TrimSpace(lines[0]) != "Profile" {
		t.Fatalf("expected Profile type, got %q", lines[0])
	}
	if strings.TrimSpace(lines[1]) != "True" {
		t.Fatalf("expected bucket profile samples to match bucket samples, got %q", lines[1])
	}
}

func TestBucketProfileFilter(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A", "B"}, lines: []uint32{0, 0}, count: 5, thread: "t1"},
		{frames: []string{"C", "D"}, lines: []uint32{0, 0}, count: 3, thread: "t1"},
	})
	timed := &parsedJFR{
		eventCounts:   map[string]int{"cpu": 8},
		stacksByEvent: map[string]*stackFile{"cpu": sf},
		timedEvents: map[string][]timedEvent{
			"cpu": {
				{offsetNanos: 1e9, stackKey: "A;B", frames: []string{"A", "B"}, lines: []uint32{0, 0}, thread: "t1", weight: 5},
				{offsetNanos: 2e9, stackKey: "C;D", frames: []string{"C", "D"}, lines: []uint32{0, 0}, thread: "t1", weight: 3},
			},
		},
		spanNanos: 3e9,
	}
	p := newStarlarkProfile(sf, timed, "cpu", "test.jfr")
	p.timedParsed = timed

	out := captureOutput(func() {
		code := runScript(`
buckets = p.timeline(buckets=1)
bp = buckets[0].profile
filtered = bp.filter(lambda s: s.has("A"))
print(filtered.samples)
`, "", nil, testTimeout, withPredeclared("p", p))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	if strings.TrimSpace(out) != "5" {
		t.Fatalf("expected 5 filtered samples, got %q", out)
	}
}

func TestBucketProfileTimeline(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A", "B"}, lines: []uint32{0, 0}, count: 4, thread: "t1"},
	})
	timed := &parsedJFR{
		eventCounts:   map[string]int{"cpu": 4},
		stacksByEvent: map[string]*stackFile{"cpu": sf},
		timedEvents: map[string][]timedEvent{
			"cpu": {
				{offsetNanos: 1e9, stackKey: "A;B", frames: []string{"A", "B"}, lines: []uint32{0, 0}, thread: "t1", weight: 1},
				{offsetNanos: 2e9, stackKey: "A;B", frames: []string{"A", "B"}, lines: []uint32{0, 0}, thread: "t1", weight: 1},
				{offsetNanos: 3e9, stackKey: "A;B", frames: []string{"A", "B"}, lines: []uint32{0, 0}, thread: "t1", weight: 1},
				{offsetNanos: 4e9, stackKey: "A;B", frames: []string{"A", "B"}, lines: []uint32{0, 0}, thread: "t1", weight: 1},
			},
		},
		spanNanos: 5e9,
	}
	p := newStarlarkProfile(sf, timed, "cpu", "test.jfr")
	p.timedParsed = timed

	out := captureOutput(func() {
		code := runScript(`
buckets = p.timeline(buckets=2)
# Get the first non-empty bucket's profile and check its timeline
for b in buckets:
    if b.samples > 0:
        bp = b.profile
        inner = bp.timeline(buckets=2)
        total = 0
        for ib in inner:
            total += ib.samples
        print(total == b.samples)
        break
`, "", nil, testTimeout, withPredeclared("p", p))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	if strings.TrimSpace(out) != "True" {
		t.Fatalf("expected bucket profile timeline to match bucket samples, got %q", out)
	}
}

// ---------------------------------------------------------------------------
// Profile.tree / Profile.trace / Profile.callers
// ---------------------------------------------------------------------------

func TestProfileTree(t *testing.T) {
	p := testProfile()
	out := captureOutput(func() {
		code := runScript(`
result = p.tree("Server.handle")
print(result)
`, "", nil, testTimeout, withPredeclared("p", p))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	if !strings.Contains(out, "Server.handle") {
		t.Fatalf("expected tree output to contain Server.handle, got %q", out)
	}
	if !strings.Contains(out, "%]") {
		t.Fatalf("expected percentage in tree output, got %q", out)
	}
}

func TestProfileTreeNoMethod(t *testing.T) {
	p := testProfile()
	out := captureOutput(func() {
		code := runScript(`
result = p.tree()
print(result)
`, "", nil, testTimeout, withPredeclared("p", p))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	// Root tree should contain Thread.run (root frame).
	if !strings.Contains(out, "Thread.run") {
		t.Fatalf("expected root tree to contain Thread.run, got %q", out)
	}
}

func TestProfileTreeDepth(t *testing.T) {
	p := testProfile()
	out := captureOutput(func() {
		code := runScript(`
result = p.tree("Server.handle", depth=1)
print(result)
`, "", nil, testTimeout, withPredeclared("p", p))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	// With depth=1, we should see Server.handle but NOT its grandchildren.
	if !strings.Contains(out, "Server.handle") {
		t.Fatalf("expected Server.handle in output, got %q", out)
	}
	// HashMap.put is 2 levels below Server.handle — should be truncated by depth=1.
	if strings.Contains(out, "HashMap.put") {
		t.Fatalf("depth=1 should not show HashMap.put (2 levels deep), got %q", out)
	}
}

func TestProfileTreeNoMatch(t *testing.T) {
	p := testProfile()
	out := captureOutput(func() {
		code := runScript(`
result = p.tree("NonExistent")
print(result)
`, "", nil, testTimeout, withPredeclared("p", p))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	if !strings.Contains(out, "no stacks matching") {
		t.Fatalf("expected 'no stacks matching' message, got %q", out)
	}
}

func TestProfileTreeEmpty(t *testing.T) {
	sf := makeStackFile(nil)
	p := newStarlarkProfile(sf, nil, "cpu", "test")
	out := captureOutput(func() {
		code := runScript(`
result = p.tree("anything")
print(repr(result))
`, "", nil, testTimeout, withPredeclared("p", p))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	if !strings.Contains(out, `""`) {
		t.Fatalf("expected empty string for empty profile tree, got %q", out)
	}
}

func TestProfileTrace(t *testing.T) {
	p := testProfile()
	out := captureOutput(func() {
		code := runScript(`
result = p.trace("Server.handle")
print(result)
`, "", nil, testTimeout, withPredeclared("p", p))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	if !strings.Contains(out, "Server.handle") {
		t.Fatalf("expected trace output to contain Server.handle, got %q", out)
	}
	if !strings.Contains(out, "Hottest leaf:") {
		t.Fatalf("expected 'Hottest leaf:' in trace output, got %q", out)
	}
}

func TestProfileTraceRequired(t *testing.T) {
	p := testProfile()
	stderr := captureStream(&os.Stderr, func() {
		code := runScript(`p.trace()`, "", nil, testTimeout, withPredeclared("p", p))
		if code == 0 {
			t.Fatalf("expected error when method not provided to trace()")
		}
	})
	if !strings.Contains(stderr, "method") {
		t.Fatalf("expected error about missing method, got %q", stderr)
	}
}

func TestProfileTraceEmptyMethod(t *testing.T) {
	p := testProfile()
	stderr := captureStream(&os.Stderr, func() {
		code := runScript(`p.trace("")`, "", nil, testTimeout, withPredeclared("p", p))
		if code == 0 {
			t.Fatalf("expected error for empty method string")
		}
	})
	if !strings.Contains(stderr, "non-empty") {
		t.Fatalf("expected 'non-empty' error, got %q", stderr)
	}
}

func TestProfileTraceFQN(t *testing.T) {
	p := testProfile()
	out := captureOutput(func() {
		code := runScript(`
result = p.trace("Server.handle", fqn=True)
print(result)
`, "", nil, testTimeout, withPredeclared("p", p))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	// FQN output should contain dots.
	if !strings.Contains(out, "com.example") {
		t.Fatalf("expected FQN with dots in trace output, got %q", out)
	}
}

func TestProfileTraceNoMatch(t *testing.T) {
	p := testProfile()
	out := captureOutput(func() {
		code := runScript(`
result = p.trace("NonExistent")
print(result)
`, "", nil, testTimeout, withPredeclared("p", p))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	if !strings.Contains(out, "no stacks matching") {
		t.Fatalf("expected 'no stacks matching' message, got %q", out)
	}
}

func TestProfileCallers(t *testing.T) {
	p := testProfile()
	out := captureOutput(func() {
		code := runScript(`
result = p.callers("HashMap.put")
print(result)
`, "", nil, testTimeout, withPredeclared("p", p))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	if !strings.Contains(out, "HashMap.put") {
		t.Fatalf("expected callers output to contain HashMap.put, got %q", out)
	}
	// Should show callers toward root.
	if !strings.Contains(out, "Service.process") {
		t.Fatalf("expected Service.process as caller, got %q", out)
	}
}

func TestProfileCallersRequired(t *testing.T) {
	p := testProfile()
	stderr := captureStream(&os.Stderr, func() {
		code := runScript(`p.callers()`, "", nil, testTimeout, withPredeclared("p", p))
		if code == 0 {
			t.Fatalf("expected error when method not provided to callers()")
		}
	})
	if !strings.Contains(stderr, "method") {
		t.Fatalf("expected error about missing method, got %q", stderr)
	}
}

func TestProfileCallersEmptyMethod(t *testing.T) {
	p := testProfile()
	stderr := captureStream(&os.Stderr, func() {
		code := runScript(`p.callers("")`, "", nil, testTimeout, withPredeclared("p", p))
		if code == 0 {
			t.Fatalf("expected error for empty method string")
		}
	})
	if !strings.Contains(stderr, "non-empty") {
		t.Fatalf("expected 'non-empty' error, got %q", stderr)
	}
}

func TestProfileCallersNoMatch(t *testing.T) {
	p := testProfile()
	out := captureOutput(func() {
		code := runScript(`
result = p.callers("NonExistent")
print(result)
`, "", nil, testTimeout, withPredeclared("p", p))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	if !strings.Contains(out, "no stacks matching") {
		t.Fatalf("expected 'no stacks matching' message, got %q", out)
	}
}

func TestProfileTreeReturnType(t *testing.T) {
	p := testProfile()
	out := captureOutput(func() {
		code := runScript(`print(type(p.tree("Server")))`, "", nil, testTimeout, withPredeclared("p", p))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	if strings.TrimSpace(out) != "string" {
		t.Fatalf("expected string type, got %q", out)
	}
}

func TestBucketProfileTree(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"A", "B", "C"}, lines: []uint32{0, 0, 0}, count: 5, thread: "t1"},
		{frames: []string{"A", "B", "D"}, lines: []uint32{0, 0, 0}, count: 3, thread: "t1"},
	})
	timed := &parsedJFR{
		eventCounts:   map[string]int{"cpu": 8},
		stacksByEvent: map[string]*stackFile{"cpu": sf},
		timedEvents: map[string][]timedEvent{
			"cpu": {
				{offsetNanos: 1e9, stackKey: "A;B;C", frames: []string{"A", "B", "C"}, lines: []uint32{0, 0, 0}, thread: "t1", weight: 5},
				{offsetNanos: 2e9, stackKey: "A;B;D", frames: []string{"A", "B", "D"}, lines: []uint32{0, 0, 0}, thread: "t1", weight: 3},
			},
		},
		spanNanos: 3e9,
	}
	p := newStarlarkProfile(sf, timed, "cpu", "test.jfr")
	p.timedParsed = timed

	out := captureOutput(func() {
		code := runScript(`
buckets = p.timeline(buckets=1)
bp = buckets[0].profile
result = bp.tree("B")
print(result)
`, "", nil, testTimeout, withPredeclared("p", p))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	if !strings.Contains(out, "B") || !strings.Contains(out, "%]") {
		t.Fatalf("expected tree output from bucket profile, got %q", out)
	}
	if !strings.Contains(out, "C") || !strings.Contains(out, "D") {
		t.Fatalf("expected children C and D in bucket profile tree, got %q", out)
	}
}

// ---------------------------------------------------------------------------
// tree/trace/callers on real JFR fixtures
// ---------------------------------------------------------------------------

func TestProfileTreeJFR(t *testing.T) {
	out := captureOutput(func() {
		code := runScript(fmt.Sprintf(`
p = open(%q)
print(p.tree("Workload.lockStep", depth=2))
`, scriptFixture("cpu.jfr")), "", nil, testTimeout)
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	if !strings.Contains(out, "Workload.lockStep") {
		t.Fatalf("expected Workload.lockStep in tree, got %q", out)
	}
	if !strings.Contains(out, "%]") {
		t.Fatalf("expected percentage in tree, got %q", out)
	}
}

func TestProfileTraceJFR(t *testing.T) {
	out := captureOutput(func() {
		code := runScript(fmt.Sprintf(`
p = open(%q)
print(p.trace("Workload.lockStep"))
`, scriptFixture("cpu.jfr")), "", nil, testTimeout)
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	if !strings.Contains(out, "Workload.lockStep") {
		t.Fatalf("expected Workload.lockStep in trace, got %q", out)
	}
	if !strings.Contains(out, "Hottest leaf:") {
		t.Fatalf("expected Hottest leaf in trace, got %q", out)
	}
}

func TestProfileCallersJFR(t *testing.T) {
	out := captureOutput(func() {
		code := runScript(fmt.Sprintf(`
p = open(%q)
print(p.callers("Workload.computeStep"))
`, scriptFixture("cpu.jfr")), "", nil, testTimeout)
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	if !strings.Contains(out, "Workload.computeStep") {
		t.Fatalf("expected Workload.computeStep in callers, got %q", out)
	}
	if !strings.Contains(out, "Thread.run") {
		t.Fatalf("expected Thread.run as caller, got %q", out)
	}
}

func TestProfileTreeJFRTimeline(t *testing.T) {
	out := captureOutput(func() {
		code := runScript(fmt.Sprintf(`
p = open(%q)
buckets = p.timeline(buckets=2)
for b in buckets:
    if b.samples > 0:
        result = b.profile.tree("Workload", depth=2)
        if "Workload" in result:
            print("OK")
            break
`, scriptFixture("cpu.jfr")), "", nil, testTimeout)
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	if strings.TrimSpace(out) != "OK" {
		t.Fatalf("expected OK from bucket.profile.tree on JFR, got %q", out)
	}
}

// ---------------------------------------------------------------------------
// Timeline numeric resolution (Item 3)
// ---------------------------------------------------------------------------

func TestTimelineNumericResolutionInt(t *testing.T) {
	// resolution=30 (integer keyword) should work like resolution="30s".
	outKw := captureOutput(func() {
		code := runScript(fmt.Sprintf(`
p = open(%q)
buckets = p.timeline(resolution=30)
print(len(buckets))
`, scriptFixture("cpu.jfr")), "", nil, testTimeout)
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	outStr := captureOutput(func() {
		code := runScript(fmt.Sprintf(`
p = open(%q)
buckets = p.timeline(resolution="30s")
print(len(buckets))
`, scriptFixture("cpu.jfr")), "", nil, testTimeout)
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	if strings.TrimSpace(outKw) != strings.TrimSpace(outStr) {
		t.Errorf("resolution=30 produced %q buckets, resolution='30s' produced %q", outKw, outStr)
	}
}

func TestTimelineNumericResolutionFloat(t *testing.T) {
	out := captureOutput(func() {
		code := runScript(fmt.Sprintf(`
p = open(%q)
buckets = p.timeline(resolution=0.5)
print(len(buckets))
`, scriptFixture("cpu.jfr")), "", nil, testTimeout)
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	n := strings.TrimSpace(out)
	if n == "0" {
		t.Fatal("expected buckets > 0 for sub-second resolution")
	}
}

func TestTimelineNumericPositionalError(t *testing.T) {
	stderr := captureStream(&os.Stderr, func() {
		code := runScript(fmt.Sprintf(`
p = open(%q)
buckets = p.timeline(30)
print(len(buckets))
`, scriptFixture("cpu.jfr")), "", nil, testTimeout)
		if code == 0 {
			t.Fatal("expected error for positional numeric resolution")
		}
	})
	if !strings.Contains(stderr, "keyword") {
		t.Errorf("error should mention 'keyword', got: %q", stderr)
	}
}

func TestTimelineResolutionBadType(t *testing.T) {
	stderr := captureStream(&os.Stderr, func() {
		code := runScript(fmt.Sprintf(`
p = open(%q)
buckets = p.timeline(resolution=True)
print(len(buckets))
`, scriptFixture("cpu.jfr")), "", nil, testTimeout)
		if code == 0 {
			t.Fatal("expected error for bool resolution")
		}
	})
	if !strings.Contains(stderr, "must be string, int, or float") {
		t.Errorf("error should mention type constraint, got: %q", stderr)
	}
}

// ---------------------------------------------------------------------------
// Profile.no_idle()
// ---------------------------------------------------------------------------

func TestProfileNoIdle(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"java/lang/Thread.run", "com/example/App.work"}, lines: []uint32{0, 0}, count: 10, thread: "worker-1"},
		{frames: []string{"java/lang/Thread.run", "java/lang/Object.wait"}, lines: []uint32{0, 0}, count: 7, thread: "worker-2"},
	})
	p := newStarlarkProfile(sf, nil, "cpu", "test")

	out := captureOutput(func() {
		code := runScript(`
filtered = p.no_idle()
print(filtered.samples)
`, "", nil, testTimeout, withPredeclared("p", p))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	if strings.TrimSpace(out) != "10" {
		t.Fatalf("expected 10 samples after no_idle, got %q", out)
	}
}

func TestProfileNoIdleEmpty(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"java/lang/Thread.run", "java/lang/Object.wait"}, lines: []uint32{0, 0}, count: 5, thread: "t1"},
		{frames: []string{"java/lang/Thread.run", "java/util/concurrent/locks/LockSupport.park"}, lines: []uint32{0, 0}, count: 3, thread: "t2"},
	})
	p := newStarlarkProfile(sf, nil, "cpu", "test")

	out := captureOutput(func() {
		code := runScript(`
filtered = p.no_idle()
print(filtered.samples)
`, "", nil, testTimeout, withPredeclared("p", p))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	if strings.TrimSpace(out) != "0" {
		t.Fatalf("expected 0 samples after no_idle, got %q", out)
	}
}

func TestProfileNoIdlePreservesOriginal(t *testing.T) {
	sf := makeStackFile([]stack{
		{frames: []string{"java/lang/Thread.run", "com/example/App.work"}, lines: []uint32{0, 0}, count: 10, thread: "worker-1"},
		{frames: []string{"java/lang/Thread.run", "java/lang/Object.wait"}, lines: []uint32{0, 0}, count: 7, thread: "worker-2"},
	})
	p := newStarlarkProfile(sf, nil, "cpu", "test")

	out := captureOutput(func() {
		code := runScript(`
_ = p.no_idle()
print(p.samples)
`, "", nil, testTimeout, withPredeclared("p", p))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	if strings.TrimSpace(out) != "17" {
		t.Fatalf("expected original 17, got %q", out)
	}
}

func TestProfileNoIdleTimeline(t *testing.T) {
	out := captureOutput(func() {
		code := runScript(fmt.Sprintf(`
p = open(%q)
ni = p.no_idle()
buckets = ni.timeline(buckets=3)
total = 0
for b in buckets:
    total += b.samples
print(total)
print(ni.samples)
`, scriptFixture("cpu.jfr")), "", nil, testTimeout)
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected 2 lines, got %d: %q", len(lines), out)
	}
	bucketTotal := strings.TrimSpace(lines[0])
	niSamples := strings.TrimSpace(lines[1])
	if bucketTotal != niSamples {
		t.Fatalf("bucket total (%s) should equal no_idle samples (%s)", bucketTotal, niSamples)
	}
}

func TestProfileNoIdleScopedEvents(t *testing.T) {
	// Build a timed profile with both idle and non-idle events, then split()
	// to produce a child with scopedEvents != nil. no_idle() on that child
	// must filter both the stackFile and scopedEvents so that a subsequent
	// timeline() excludes idle events from bucket counts.
	sf := makeStackFile([]stack{
		{frames: []string{"A", "B"}, lines: []uint32{0, 0}, count: 4, thread: "t1"},
		{frames: []string{"A", "java/lang/Object.wait"}, lines: []uint32{0, 0}, count: 3, thread: "t1"},
	})
	events := []timedEvent{
		{offsetNanos: 0, stackKey: "A;B", frames: []string{"A", "B"}, lines: []uint32{0, 0}, thread: "t1", weight: 2},
		{offsetNanos: 500_000_000, stackKey: "A;java/lang/Object.wait", frames: []string{"A", "java/lang/Object.wait"}, lines: []uint32{0, 0}, thread: "t1", weight: 1},
		{offsetNanos: 1_500_000_000, stackKey: "A;B", frames: []string{"A", "B"}, lines: []uint32{0, 0}, thread: "t1", weight: 2},
		{offsetNanos: 2_000_000_000, stackKey: "A;java/lang/Object.wait", frames: []string{"A", "java/lang/Object.wait"}, lines: []uint32{0, 0}, thread: "t1", weight: 2},
	}
	timed := &parsedJFR{
		eventCounts:   map[string]int{"cpu": 7},
		stacksByEvent: map[string]*stackFile{"cpu": sf},
		timedEvents:   map[string][]timedEvent{"cpu": events},
		spanNanos:     3_000_000_000,
	}
	p := newStarlarkProfile(sf, timed, "cpu", "test.jfr")
	p.timedParsed = timed

	out := captureOutput(func() {
		code := runScript(`
# split at 1s: part0=[0,1s) has 2 non-idle + 1 idle, part1=[1s,+) has 2 non-idle + 2 idle
parts = p.split([1.0])
part1 = parts[1]
# part1 has scopedEvents != nil (from split)
ni = part1.no_idle()
print(ni.samples)
buckets = ni.timeline(buckets=1)
print(buckets[0].samples)
`, "", nil, testTimeout, withPredeclared("p", p))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected 2 lines, got %d: %q", len(lines), out)
	}
	// part1 has events at 1.5s (B, weight=2) and 2s (Object.wait, weight=2).
	// no_idle removes the Object.wait event → 2 samples remain.
	if strings.TrimSpace(lines[0]) != "2" {
		t.Fatalf("expected no_idle samples 2, got %q", lines[0])
	}
	if strings.TrimSpace(lines[1]) != "2" {
		t.Fatalf("expected bucket samples 2, got %q", lines[1])
	}
}

// ---------------------------------------------------------------------------
// Bucket.label
// ---------------------------------------------------------------------------

func TestBucketLabel(t *testing.T) {
	// Synthetic profile with known nanos so we can predict exact label output.
	sf := makeStackFile([]stack{
		{frames: []string{"A", "B"}, lines: []uint32{0, 0}, count: 5, thread: "t1"},
	})
	events := []timedEvent{
		{offsetNanos: 0, stackKey: "A;B", frames: []string{"A", "B"}, lines: []uint32{0, 0}, thread: "t1", weight: 3},
		{offsetNanos: 2_000_000_000, stackKey: "A;B", frames: []string{"A", "B"}, lines: []uint32{0, 0}, thread: "t1", weight: 2},
	}
	timed := &parsedJFR{
		eventCounts:   map[string]int{"cpu": 5},
		stacksByEvent: map[string]*stackFile{"cpu": sf},
		timedEvents:   map[string][]timedEvent{"cpu": events},
		spanNanos:     3_000_000_000,
	}
	p := newStarlarkProfile(sf, timed, "cpu", "test.jfr")
	p.timedParsed = timed

	out := captureOutput(func() {
		code := runScript(`
buckets = p.timeline(buckets=3)
for b in buckets:
    print(b.label)
`, "", nil, testTimeout, withPredeclared("p", p))
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	// 3 buckets over 3s → 1s each. bucketWidth=1e9 → precision=1.
	// formatTimelineTimestamp(0, 1e9)="0.0s", (1e9, 1e9)="1.0s", etc.
	expected := []string{"0.0s-1.0s", "1.0s-2.0s", "2.0s-3.0s"}
	if len(lines) != len(expected) {
		t.Fatalf("expected %d labels, got %d: %q", len(expected), len(lines), out)
	}
	for i, exp := range expected {
		got := strings.TrimSpace(lines[i])
		if got != exp {
			t.Fatalf("bucket %d: expected %q, got %q", i, exp, got)
		}
	}
}

func TestBucketLabelType(t *testing.T) {
	out := captureOutput(func() {
		code := runScript(fmt.Sprintf(`
p = open(%q)
buckets = p.timeline(buckets=5)
print(type(buckets[0].label))
`, scriptFixture("cpu.jfr")), "", nil, testTimeout)
		if code != 0 {
			t.Fatalf("expected exit 0, got %d", code)
		}
	})
	if strings.TrimSpace(out) != "string" {
		t.Fatalf("expected string type, got %q", out)
	}
}
