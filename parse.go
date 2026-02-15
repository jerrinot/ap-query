package main

import (
	"bufio"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/grafana/jfr-parser/parser"
	"github.com/grafana/jfr-parser/parser/types"
)

// ---------------------------------------------------------------------------
// Data model
// ---------------------------------------------------------------------------

type stack struct {
	frames []string // root → leaf order
	lines  []uint32 // parallel to frames, 0 = unknown
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

type parsedJFR struct {
	eventCounts   map[string]int
	stacksByEvent map[string]*stackFile
}

func allJFREventTypes() map[string]struct{} {
	return map[string]struct{}{
		"cpu":   {},
		"wall":  {},
		"alloc": {},
		"lock":  {},
	}
}

func singleJFREventType(eventType string) map[string]struct{} {
	return map[string]struct{}{eventType: {}}
}

func appendJFRStackSample(p *parser.Parser, agg map[stackKey]*aggValue, stRef types.StackTraceRef, thRef types.ThreadRef) {
	st := p.GetStacktrace(stRef)
	if st == nil || len(st.Frames) == 0 {
		return
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

func buildStackFile(agg map[stackKey]*aggValue) *stackFile {
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
	return sf
}

func parseJFRData(path string, stackEvents map[string]struct{}) (*parsedJFR, error) {
	buf, err := readJFRBytes(path)
	if err != nil {
		return nil, err
	}

	p := parser.NewParser(buf, parser.Options{})
	counts := make(map[string]int)
	aggByEvent := make(map[string]map[stackKey]*aggValue, len(stackEvents))
	for eventType := range stackEvents {
		aggByEvent[eventType] = make(map[stackKey]*aggValue)
	}

	for {
		typ, err := p.ParseEvent()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parse event: %w", err)
		}

		var eventType string
		var stRef types.StackTraceRef
		var thRef types.ThreadRef

		switch typ {
		case p.TypeMap.T_EXECUTION_SAMPLE:
			eventType = "cpu"
			stRef = p.ExecutionSample.StackTrace
			thRef = p.ExecutionSample.SampledThread
		case p.TypeMap.T_WALL_CLOCK_SAMPLE:
			eventType = "wall"
			stRef = p.WallClockSample.StackTrace
			thRef = p.WallClockSample.SampledThread
		case p.TypeMap.T_ALLOC_IN_NEW_TLAB:
			eventType = "alloc"
			stRef = p.ObjectAllocationInNewTLAB.StackTrace
			thRef = p.ObjectAllocationInNewTLAB.EventThread
		case p.TypeMap.T_ALLOC_OUTSIDE_TLAB:
			eventType = "alloc"
			stRef = p.ObjectAllocationOutsideTLAB.StackTrace
			thRef = p.ObjectAllocationOutsideTLAB.EventThread
		case p.TypeMap.T_ALLOC_SAMPLE:
			eventType = "alloc"
			stRef = p.ObjectAllocationSample.StackTrace
			thRef = p.ObjectAllocationSample.EventThread
		case p.TypeMap.T_MONITOR_ENTER:
			eventType = "lock"
			stRef = p.JavaMonitorEnter.StackTrace
			thRef = p.JavaMonitorEnter.EventThread
		}
		if eventType == "" {
			continue
		}

		counts[eventType]++
		agg, ok := aggByEvent[eventType]
		if !ok {
			continue
		}
		appendJFRStackSample(p, agg, stRef, thRef)
	}

	stacksByEvent := make(map[string]*stackFile, len(aggByEvent))
	for eventType, agg := range aggByEvent {
		stacksByEvent[eventType] = buildStackFile(agg)
	}
	return &parsedJFR{
		eventCounts:   counts,
		stacksByEvent: stacksByEvent,
	}, nil
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

// splitCollapsedLine splits "frames count\n" into the frames string and count.
// Returns ("", 0) if the line is malformed.
func splitCollapsedLine(line string) (string, int) {
	i := strings.LastIndexByte(line, ' ')
	if i < 1 {
		return "", 0
	}
	count, err := strconv.Atoi(line[i+1:])
	if err != nil || count <= 0 {
		return "", 0
	}
	return line[:i], count
}

// parseThreadFrame checks if frame is "[name]" or "[name tid=N]" and returns
// the thread name, or "" if not a thread marker.
func parseThreadFrame(frame string) string {
	if len(frame) < 3 || frame[0] != '[' || frame[len(frame)-1] != ']' {
		return ""
	}
	inner := frame[1 : len(frame)-1]
	// Strip optional " tid=N" suffix.
	if idx := strings.Index(inner, " tid="); idx >= 0 {
		inner = inner[:idx]
	}
	return inner
}

// parseAnnotatedFrame strips jfrconv annotations from "Method:line_[type]".
// Returns (method, lineNumber) or (frame, 0) if not annotated.
func parseAnnotatedFrame(frame string) (string, uint32) {
	// Look for the _[...] suffix first.
	base := frame
	if idx := strings.LastIndex(frame, "_["); idx >= 0 && frame[len(frame)-1] == ']' {
		base = frame[:idx]
	}
	// Now base should be "Method:line" if annotated.
	colon := strings.LastIndexByte(base, ':')
	if colon < 1 {
		return frame, 0
	}
	ln, err := strconv.ParseUint(base[colon+1:], 10, 32)
	if err != nil {
		return frame, 0
	}
	return base[:colon], uint32(ln)
}

func parseCollapsed(r io.Reader) (*stackFile, error) {
	sf := &stackFile{}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		framesStr, count := splitCollapsedLine(line)
		if count == 0 {
			continue
		}

		parts := strings.Split(framesStr, ";")
		thread := ""
		startIdx := 0

		if len(parts) > 0 {
			if t := parseThreadFrame(parts[0]); t != "" {
				thread = t
				startIdx = 1
			}
		}

		frames := make([]string, 0, len(parts)-startIdx)
		lines := make([]uint32, 0, len(parts)-startIdx)

		for _, part := range parts[startIdx:] {
			name, ln := parseAnnotatedFrame(part)
			frames = append(frames, name)
			lines = append(lines, ln)
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
		parsed, err := parseJFRData(path, singleJFREventType(eventType))
		if err != nil {
			return nil, true, err
		}
		sf = parsed.stacksByEvent[eventType]
		if sf == nil {
			sf = &stackFile{}
		}
		return sf, true, nil
	}
	rc, err := openReader(path)
	if err != nil {
		return nil, false, err
	}
	defer rc.Close()
	sf, err = parseCollapsed(rc)
	return sf, false, err
}
