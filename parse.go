package main

import (
	"bufio"
	"compress/gzip"
	"encoding/binary"
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

func (sf *stackFile) hideFrames(re *regexp.Regexp) *stackFile {
	out := &stackFile{totalSamples: sf.totalSamples}
	for i := range sf.stacks {
		st := &sf.stacks[i]
		var frames []string
		var lines []uint32
		for j, fr := range st.frames {
			if !matchesHide(fr, re) {
				frames = append(frames, fr)
				lines = append(lines, st.lines[j])
			}
		}
		if len(frames) == 0 {
			continue
		}
		out.stacks = append(out.stacks, stack{
			frames: frames,
			lines:  lines,
			count:  st.count,
			thread: st.thread,
		})
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

type timedEvent struct {
	offsetNanos int64    // nanoseconds since recording start (originNanos)
	stackKey    string   // prebuilt key from cachedStackTrace
	frames      []string // resolved frame names (shared with cache)
	lines       []uint32 // resolved line numbers (shared with cache)
	thread      string   // resolved thread name
	weight      int      // sample count (>1 for wall batch samples)
}

type parseOpts struct {
	collectTimestamps bool
	fromNanos         int64 // -1 = no filter
	toNanos           int64 // -1 = no filter
}

type parsedJFR struct {
	eventCounts   map[string]int
	stacksByEvent map[string]*stackFile
	timedEvents   map[string][]timedEvent // nil when not collecting
	originNanos   int64                   // first chunk's StartNanos
	spanNanos     int64                   // total recording span from chunk header scan
}

// cachedStackTrace stores a resolved stacktrace in root->leaf order plus
// the prebuilt aggregation key including line numbers.
type cachedStackTrace struct {
	frames []string
	lines  []uint32
	key    string
}

func digitsUint32(n uint32) int {
	switch {
	case n < 10:
		return 1
	case n < 100:
		return 2
	case n < 1000:
		return 3
	case n < 10000:
		return 4
	case n < 100000:
		return 5
	case n < 1000000:
		return 6
	case n < 10000000:
		return 7
	case n < 100000000:
		return 8
	case n < 1000000000:
		return 9
	default:
		return 10
	}
}

func buildStackKeyWithLines(frames []string, lines []uint32) string {
	if len(frames) == 0 {
		return ""
	}

	size := len(frames) - 1 // semicolons
	for i := range frames {
		size += len(frames[i])
		if lines[i] > 0 {
			size += 1 + digitsUint32(lines[i]) // ":" + decimal line number
		}
	}

	var b strings.Builder
	b.Grow(size)
	var numBuf [10]byte
	for i := range frames {
		if i > 0 {
			b.WriteByte(';')
		}
		b.WriteString(frames[i])
		if lines[i] > 0 {
			b.WriteByte(':')
			n := len(numBuf)
			v := lines[i]
			for {
				n--
				numBuf[n] = byte('0' + v%10)
				v /= 10
				if v == 0 {
					break
				}
			}
			b.Write(numBuf[n:])
		}
	}
	return b.String()
}

func resolveStackTraceCached(p *parser.Parser, cache map[types.StackTraceRef]*cachedStackTrace, stRef types.StackTraceRef) *cachedStackTrace {
	if cached, ok := cache[stRef]; ok {
		return cached
	}

	st := p.GetStacktrace(stRef)
	if st == nil || len(st.Frames) == 0 {
		cached := &cachedStackTrace{}
		cache[stRef] = cached
		return cached
	}

	// JFR frames are leaf-first; reverse to root-first for collapsed format.
	n := len(st.Frames)
	frames := make([]string, n)
	lines := make([]uint32, n)
	for i, f := range st.Frames {
		frames[n-1-i] = resolveFrame(p, f)
		lines[n-1-i] = f.LineNumber
	}

	cached := &cachedStackTrace{
		frames: frames,
		lines:  lines,
		key:    buildStackKeyWithLines(frames, lines),
	}
	cache[stRef] = cached
	return cached
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

// ticksToNanos converts a tick-based StartTime to nanosecond offset from originNanos.
// Overflow-safe: uses quotient/remainder to avoid intermediate overflow.
func ticksToNanos(startTicks, hdrStartTicks, hdrStartNanos, originNanos, tps uint64) int64 {
	if tps == 0 {
		return 0
	}
	delta := startTicks - hdrStartTicks
	sec := delta / tps
	rem := delta % tps
	return int64(hdrStartNanos-originNanos) +
		int64(sec)*1_000_000_000 +
		int64(rem)*1_000_000_000/int64(tps)
}

const jfrChunkHeaderSize = 68
const jfrChunkMagic = 0x464c5200

// scanChunkHeaders reads 68-byte chunk headers linked by Size to determine
// originNanos (first chunk StartNanos) and spanNanos (max end - origin).
func scanChunkHeaders(buf []byte) (originNanos int64, spanNanos int64, err error) {
	pos := 0
	chunks := 0
	var origin uint64
	var maxEnd uint64

	for pos+jfrChunkHeaderSize <= len(buf) {
		magic := binary.BigEndian.Uint32(buf[pos:])
		if magic != jfrChunkMagic {
			if chunks == 0 {
				return 0, 0, fmt.Errorf("no valid JFR chunk header found")
			}
			fmt.Fprintf(os.Stderr, "warning: truncated chunk header scan at offset %d; timeline span may be incomplete\n", pos)
			break
		}
		size := int(binary.BigEndian.Uint64(buf[pos+8:]))
		startNanos := binary.BigEndian.Uint64(buf[pos+32:])
		durationNanos := binary.BigEndian.Uint64(buf[pos+40:])

		if chunks == 0 {
			origin = startNanos
		}
		end := startNanos + durationNanos
		if chunks == 0 || end > maxEnd {
			maxEnd = end
		}
		chunks++

		if size <= 0 || pos+size > len(buf) || pos+size <= pos {
			fmt.Fprintf(os.Stderr, "warning: truncated chunk header scan at offset %d; timeline span may be incomplete\n", pos)
			break
		}
		pos += size
	}

	if chunks == 0 {
		return 0, 0, fmt.Errorf("no valid JFR chunk header found")
	}

	originNanos = int64(origin)
	if maxEnd > origin {
		spanNanos = int64(maxEnd - origin)
	}
	return originNanos, spanNanos, nil
}

func appendJFRStackSample(p *parser.Parser, stackCache map[types.StackTraceRef]*cachedStackTrace, agg map[stackKey]*aggValue, stRef types.StackTraceRef, thRef types.ThreadRef, weight int) {
	cached := resolveStackTraceCached(p, stackCache, stRef)
	if len(cached.frames) == 0 {
		return
	}

	thread := resolveThread(p, thRef)
	key := stackKey{frames: cached.key, thread: thread}
	if v, ok := agg[key]; ok {
		v.count += weight
	} else {
		agg[key] = &aggValue{frames: cached.frames, lines: cached.lines, count: weight}
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

func buildStackFileFromTimed(events []timedEvent) *stackFile {
	agg := make(map[stackKey]*aggValue)
	for i := range events {
		e := &events[i]
		key := stackKey{frames: e.stackKey, thread: e.thread}
		if v, ok := agg[key]; ok {
			v.count += e.weight
		} else {
			agg[key] = &aggValue{frames: e.frames, lines: e.lines, count: e.weight}
		}
	}
	return buildStackFile(agg)
}

func parseJFRData(path string, stackEvents map[string]struct{}, opts parseOpts) (*parsedJFR, error) {
	buf, err := readJFRBytes(path)
	if err != nil {
		return nil, err
	}

	// Scan chunk headers for origin/span before event parsing.
	var originNanos, spanNanos int64
	originNanos, spanNanos, _ = scanChunkHeaders(buf)

	p := parser.NewParser(buf, parser.Options{})
	counts := make(map[string]int)
	aggByEvent := make(map[string]map[stackKey]*aggValue, len(stackEvents))
	for eventType := range stackEvents {
		aggByEvent[eventType] = make(map[stackKey]*aggValue)
	}
	// async-profiler call_trace_id values are stable across JFR chunks, so stack
	// decoding can be memoized globally for the file. Thread refs are chunk-local
	// (raw OS tids) and therefore resolved directly per event.
	stackCache := make(map[types.StackTraceRef]*cachedStackTrace)

	var timedByEvent map[string][]timedEvent
	if opts.collectTimestamps {
		timedByEvent = make(map[string][]timedEvent, len(stackEvents))
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
		var startTicks uint64
		weight := 1

		switch typ {
		case p.TypeMap.T_EXECUTION_SAMPLE:
			eventType = "cpu"
			stRef = p.ExecutionSample.StackTrace
			thRef = p.ExecutionSample.SampledThread
			startTicks = p.ExecutionSample.StartTime
		case p.TypeMap.T_WALL_CLOCK_SAMPLE:
			eventType = "wall"
			stRef = p.WallClockSample.StackTrace
			thRef = p.WallClockSample.SampledThread
			startTicks = p.WallClockSample.StartTime
			weight = int(p.WallClockSample.Samples)
			if weight < 1 {
				weight = 1
			}
		case p.TypeMap.T_ALLOC_IN_NEW_TLAB:
			eventType = "alloc"
			stRef = p.ObjectAllocationInNewTLAB.StackTrace
			thRef = p.ObjectAllocationInNewTLAB.EventThread
			startTicks = p.ObjectAllocationInNewTLAB.StartTime
		case p.TypeMap.T_ALLOC_OUTSIDE_TLAB:
			eventType = "alloc"
			stRef = p.ObjectAllocationOutsideTLAB.StackTrace
			thRef = p.ObjectAllocationOutsideTLAB.EventThread
			startTicks = p.ObjectAllocationOutsideTLAB.StartTime
		case p.TypeMap.T_ALLOC_SAMPLE:
			eventType = "alloc"
			stRef = p.ObjectAllocationSample.StackTrace
			thRef = p.ObjectAllocationSample.EventThread
			startTicks = p.ObjectAllocationSample.StartTime
		case p.TypeMap.T_MONITOR_ENTER:
			eventType = "lock"
			stRef = p.JavaMonitorEnter.StackTrace
			thRef = p.JavaMonitorEnter.EventThread
			startTicks = p.JavaMonitorEnter.StartTime
		}
		if eventType == "" {
			continue
		}

		counts[eventType]++

		if opts.collectTimestamps {
			if _, ok := stackEvents[eventType]; !ok {
				continue
			}
			hdr := p.ChunkHeader()
			offsetNanos := ticksToNanos(startTicks, hdr.StartTicks, hdr.StartNanos, uint64(originNanos), hdr.TicksPerSecond)

			// Time-range filtering: skip events outside the window.
			if opts.fromNanos >= 0 && offsetNanos < opts.fromNanos {
				continue
			}
			if opts.toNanos >= 0 && offsetNanos >= opts.toNanos {
				continue
			}

			cached := resolveStackTraceCached(p, stackCache, stRef)
			if len(cached.frames) == 0 {
				continue
			}
			thread := resolveThread(p, thRef)
			timedByEvent[eventType] = append(timedByEvent[eventType], timedEvent{
				offsetNanos: offsetNanos,
				stackKey:    cached.key,
				frames:      cached.frames,
				lines:       cached.lines,
				thread:      thread,
				weight:      weight,
			})
		} else {
			agg, ok := aggByEvent[eventType]
			if !ok {
				continue
			}
			appendJFRStackSample(p, stackCache, agg, stRef, thRef, weight)
		}
	}

	stacksByEvent := make(map[string]*stackFile, len(aggByEvent))
	if opts.collectTimestamps {
		// Build stackFiles from timed events (already filtered by from/to).
		for eventType := range stackEvents {
			if events, ok := timedByEvent[eventType]; ok && len(events) > 0 {
				stacksByEvent[eventType] = buildStackFileFromTimed(events)
			} else {
				stacksByEvent[eventType] = &stackFile{}
			}
		}
	} else {
		for eventType, agg := range aggByEvent {
			stacksByEvent[eventType] = buildStackFile(agg)
		}
	}
	if opts.collectTimestamps {
		total := 0
		for _, events := range timedByEvent {
			total += len(events)
		}
		if total > 10_000_000 {
			fmt.Fprintf(os.Stderr, "warning: %d events collected; consider using --from/--to to narrow the time window\n", total)
		}
	}

	return &parsedJFR{
		eventCounts:   counts,
		stacksByEvent: stacksByEvent,
		timedEvents:   timedByEvent,
		originNanos:   originNanos,
		spanNanos:     spanNanos,
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
		parsed, err := parseJFRData(path, singleJFREventType(eventType), parseOpts{})
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
