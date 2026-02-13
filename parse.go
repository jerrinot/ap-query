package main

import (
	"bufio"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/grafana/jfr-parser/parser"
	"github.com/grafana/jfr-parser/parser/types"
)

// ---------------------------------------------------------------------------
// Data model — mirrors the Python ap_query.Stack
// ---------------------------------------------------------------------------

type stack struct {
	frames []string  // root → leaf order
	lines  []uint32  // parallel to frames, 0 = unknown
	count  int
	thread string // "" if unknown
}

type stackFile struct {
	stacks       []stack
	totalSamples int
}

func (sf *stackFile) filterByThread(thread string) *stackFile {
	if thread == "" {
		return sf
	}
	out := &stackFile{}
	for i := range sf.stacks {
		if strings.Contains(sf.stacks[i].thread, thread) {
			out.stacks = append(out.stacks, sf.stacks[i])
			out.totalSamples += sf.stacks[i].count
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Frame / thread resolution
// ---------------------------------------------------------------------------

func resolveFrame(p *parser.Parser, sf types.StackFrame) string {
	method := p.GetMethod(sf.Method)
	if method == nil {
		return "<unknown>"
	}
	className := ""
	class := p.GetClass(method.Type)
	if class != nil {
		className = p.GetSymbolString(class.Name)
	}
	methodName := p.GetSymbolString(method.Name)
	if className == "" {
		return methodName
	}
	return className + "." + methodName
}

func resolveThread(p *parser.Parser, ref types.ThreadRef) string {
	idx, ok := p.Threads.IDMap[ref]
	if !ok {
		return ""
	}
	t := &p.Threads.Thread[idx]
	if t.JavaName != "" {
		return t.JavaName
	}
	return t.OsName
}

// ---------------------------------------------------------------------------
// JFR → stackFile
// ---------------------------------------------------------------------------

func readJFRBytes(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	if strings.HasSuffix(path, ".gz") {
		gr, err := gzip.NewReader(f)
		if err != nil {
			return nil, fmt.Errorf("gzip: %w", err)
		}
		defer gr.Close()
		return io.ReadAll(gr)
	}
	return io.ReadAll(f)
}

// stackKey is used to aggregate identical stacks.
type stackKey struct {
	frames string // semicolon-joined
	thread string
}

// aggValue holds the frame/line data for an aggregated stack key.
type aggValue struct {
	frames []string
	lines  []uint32
	count  int
}

func parseJFR(path, eventType string) (*stackFile, error) {
	buf, err := readJFRBytes(path)
	if err != nil {
		return nil, err
	}

	p := parser.NewParser(buf, parser.Options{})
	agg := make(map[stackKey]*aggValue)

	for {
		typ, err := p.ParseEvent()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parse event: %w", err)
		}

		var stRef types.StackTraceRef
		var thRef types.ThreadRef
		var match bool

		switch {
		case eventType == "cpu" && typ == p.TypeMap.T_EXECUTION_SAMPLE:
			stRef = p.ExecutionSample.StackTrace
			thRef = p.ExecutionSample.SampledThread
			match = true
		case eventType == "wall" && typ == p.TypeMap.T_WALL_CLOCK_SAMPLE:
			stRef = p.WallClockSample.StackTrace
			thRef = p.WallClockSample.SampledThread
			match = true
		case eventType == "alloc" && typ == p.TypeMap.T_ALLOC_IN_NEW_TLAB:
			stRef = p.ObjectAllocationInNewTLAB.StackTrace
			thRef = p.ObjectAllocationInNewTLAB.EventThread
			match = true
		case eventType == "alloc" && typ == p.TypeMap.T_ALLOC_OUTSIDE_TLAB:
			stRef = p.ObjectAllocationOutsideTLAB.StackTrace
			thRef = p.ObjectAllocationOutsideTLAB.EventThread
			match = true
		case eventType == "alloc" && typ == p.TypeMap.T_ALLOC_SAMPLE:
			stRef = p.ObjectAllocationSample.StackTrace
			thRef = p.ObjectAllocationSample.EventThread
			match = true
		case eventType == "lock" && typ == p.TypeMap.T_MONITOR_ENTER:
			stRef = p.JavaMonitorEnter.StackTrace
			thRef = p.JavaMonitorEnter.EventThread
			match = true
		}

		if !match {
			continue
		}

		st := p.GetStacktrace(stRef)
		if st == nil || len(st.Frames) == 0 {
			continue
		}

		// JFR frames are leaf-first; reverse to root-first for collapsed format.
		n := len(st.Frames)
		parts := make([]string, n)
		lineNums := make([]uint32, n)
		for i, f := range st.Frames {
			parts[n-1-i] = resolveFrame(p, f)
			lineNums[n-1-i] = f.LineNumber
		}

		// Build key that includes line numbers for differentiation.
		keyParts := make([]string, n)
		for i := 0; i < n; i++ {
			if lineNums[i] > 0 {
				keyParts[i] = fmt.Sprintf("%s:%d", parts[i], lineNums[i])
			} else {
				keyParts[i] = parts[i]
			}
		}

		thread := resolveThread(p, thRef)
		key := stackKey{frames: strings.Join(keyParts, ";"), thread: thread}
		if v, ok := agg[key]; ok {
			v.count++
		} else {
			agg[key] = &aggValue{frames: parts, lines: lineNums, count: 1}
		}
	}

	sf := &stackFile{}
	for k, v := range agg {
		sf.stacks = append(sf.stacks, stack{
			frames: v.frames,
			lines:  v.lines,
			count:  v.count,
			thread: k.thread,
		})
		sf.totalSamples += v.count
	}
	return sf, nil
}

// ---------------------------------------------------------------------------
// Collapsed-stack text → stackFile
// ---------------------------------------------------------------------------

// openReader opens a file for reading, handling gzip and stdin ("-").
func openReader(path string) (io.ReadCloser, error) {
	if path == "-" {
		return io.NopCloser(os.Stdin), nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	if strings.HasSuffix(path, ".gz") {
		gr, err := gzip.NewReader(f)
		if err != nil {
			f.Close()
			return nil, fmt.Errorf("gzip: %w", err)
		}
		return &gzipReadCloser{gz: gr, f: f}, nil
	}
	return f, nil
}

type gzipReadCloser struct {
	gz *gzip.Reader
	f  *os.File
}

func (g *gzipReadCloser) Read(p []byte) (int, error) { return g.gz.Read(p) }
func (g *gzipReadCloser) Close() error {
	g.gz.Close()
	return g.f.Close()
}

var (
	collapsedLineRe = regexp.MustCompile(`^(.+)\s+(\d+)$`)
	threadFrameRe   = regexp.MustCompile(`^\[(.+?)(?:\s+tid=\d+)?\]$`)
	annotatedFrameRe = regexp.MustCompile(`^(.+?):(\d+)(?:_\[[^\]]*\])?$`)
)

func parseCollapsed(r io.Reader) (*stackFile, error) {
	sf := &stackFile{}
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		m := collapsedLineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		framesStr := m[1]
		count, _ := strconv.Atoi(m[2])
		if count <= 0 {
			continue
		}

		parts := strings.Split(framesStr, ";")
		thread := ""
		startIdx := 0

		// Check if first frame is a thread marker.
		if len(parts) > 0 {
			if tm := threadFrameRe.FindStringSubmatch(parts[0]); tm != nil {
				thread = tm[1]
				startIdx = 1
			}
		}

		frames := make([]string, 0, len(parts)-startIdx)
		lines := make([]uint32, 0, len(parts)-startIdx)

		for _, part := range parts[startIdx:] {
			if am := annotatedFrameRe.FindStringSubmatch(part); am != nil {
				frames = append(frames, am[1])
				ln, _ := strconv.ParseUint(am[2], 10, 32)
				lines = append(lines, uint32(ln))
			} else {
				frames = append(frames, part)
				lines = append(lines, 0)
			}
		}

		if len(frames) == 0 {
			continue
		}

		sf.stacks = append(sf.stacks, stack{
			frames: frames,
			lines:  lines,
			count:  count,
			thread: thread,
		})
		sf.totalSamples += count
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return sf, nil
}

// ---------------------------------------------------------------------------
// Unified input: auto-detect JFR vs collapsed text
// ---------------------------------------------------------------------------

func isJFRPath(path string) bool {
	if path == "-" {
		return false
	}
	p := strings.ToLower(path)
	return strings.HasSuffix(p, ".jfr") || strings.HasSuffix(p, ".jfr.gz")
}

func openInput(path, eventType string) (sf *stackFile, isJFR bool, err error) {
	if isJFRPath(path) {
		sf, err = parseJFR(path, eventType)
		return sf, true, err
	}
	rc, err := openReader(path)
	if err != nil {
		return nil, false, err
	}
	defer rc.Close()
	sf, err = parseCollapsed(rc)
	return sf, false, err
}

// ---------------------------------------------------------------------------
// Discover available event types
// ---------------------------------------------------------------------------

func discoverEvents(path string) (map[string]int, error) {
	buf, err := readJFRBytes(path)
	if err != nil {
		return nil, err
	}

	p := parser.NewParser(buf, parser.Options{})
	counts := make(map[string]int)

	for {
		typ, err := p.ParseEvent()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch typ {
		case p.TypeMap.T_EXECUTION_SAMPLE:
			counts["cpu"]++
		case p.TypeMap.T_WALL_CLOCK_SAMPLE:
			counts["wall"]++
		case p.TypeMap.T_ALLOC_IN_NEW_TLAB, p.TypeMap.T_ALLOC_OUTSIDE_TLAB, p.TypeMap.T_ALLOC_SAMPLE:
			counts["alloc"]++
		case p.TypeMap.T_MONITOR_ENTER:
			counts["lock"]++
		}
	}
	return counts, nil
}
