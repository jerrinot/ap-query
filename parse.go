package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"unicode/utf8"

	"github.com/grafana/jfr-parser/parser"
	"github.com/grafana/jfr-parser/parser/types"
	"github.com/grafana/jfr-parser/parser/types/def"
)

var warnedLargeEventCount atomic.Bool

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

func (sf *stackFile) filterIdle() *stackFile {
	out := &stackFile{}
	for i := range sf.stacks {
		st := &sf.stacks[i]
		if len(st.frames) > 0 && isIdleLeaf(st.frames[len(st.frames)-1]) {
			continue
		}
		out.stacks = append(out.stacks, *st)
		out.totalSamples += st.count
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

// stackFileFromEvents builds a minimal stackFile from timed events for
// suggestion purposes. It deduplicates frames upfront to avoid allocating
// one stack per event on large recordings.
func stackFileFromEvents(events []timedEvent) *stackFile {
	seen := make(map[string]bool)
	var frames []string
	for i := range events {
		for _, fr := range events[i].frames {
			if !seen[fr] {
				seen[fr] = true
				frames = append(frames, fr)
			}
		}
	}
	if len(frames) == 0 {
		return &stackFile{}
	}
	return &stackFile{
		stacks:       []stack{{frames: frames, lines: make([]uint32, len(frames)), count: 1}},
		totalSamples: 1,
	}
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

	// Pre-allocate buffer for non-gzipped files to avoid repeated
	// slice growing (significant for large files).
	info, err := f.Stat()
	if err != nil {
		return io.ReadAll(f)
	}
	buf := make([]byte, info.Size())
	_, err = io.ReadFull(f, buf)
	if err != nil {
		return nil, err
	}
	return buf, nil
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

type parsedProfile struct {
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

func allEventTypes() map[string]struct{} {
	return map[string]struct{}{
		"cpu":   {},
		"wall":  {},
		"alloc": {},
		"lock":  {},
	}
}

func singleEventType(eventType string) map[string]struct{} {
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

type jfrEventInfo struct {
	eventType  string
	stRef      types.StackTraceRef
	thRef      types.ThreadRef
	startTicks uint64
	weight     int
}

func classifyEvent(p *parser.Parser, typ def.TypeID) (jfrEventInfo, bool) {
	switch typ {
	case p.TypeMap.T_EXECUTION_SAMPLE:
		return jfrEventInfo{"cpu", p.ExecutionSample.StackTrace, p.ExecutionSample.SampledThread, p.ExecutionSample.StartTime, 1}, true
	case p.TypeMap.T_WALL_CLOCK_SAMPLE:
		weight := int(p.WallClockSample.Samples)
		if weight < 1 {
			weight = 1
		}
		return jfrEventInfo{"wall", p.WallClockSample.StackTrace, p.WallClockSample.SampledThread, p.WallClockSample.StartTime, weight}, true
	case p.TypeMap.T_ALLOC_IN_NEW_TLAB:
		return jfrEventInfo{"alloc", p.ObjectAllocationInNewTLAB.StackTrace, p.ObjectAllocationInNewTLAB.EventThread, p.ObjectAllocationInNewTLAB.StartTime, 1}, true
	case p.TypeMap.T_ALLOC_OUTSIDE_TLAB:
		return jfrEventInfo{"alloc", p.ObjectAllocationOutsideTLAB.StackTrace, p.ObjectAllocationOutsideTLAB.EventThread, p.ObjectAllocationOutsideTLAB.StartTime, 1}, true
	case p.TypeMap.T_ALLOC_SAMPLE:
		return jfrEventInfo{"alloc", p.ObjectAllocationSample.StackTrace, p.ObjectAllocationSample.EventThread, p.ObjectAllocationSample.StartTime, 1}, true
	case p.TypeMap.T_MONITOR_ENTER:
		return jfrEventInfo{"lock", p.JavaMonitorEnter.StackTrace, p.JavaMonitorEnter.EventThread, p.JavaMonitorEnter.StartTime, 1}, true
	default:
		return jfrEventInfo{}, false
	}
}

func parseJFRData(path string, stackEvents map[string]struct{}, opts parseOpts) (*parsedProfile, error) {
	buf, err := readJFRBytes(path)
	if err != nil {
		return nil, err
	}

	// Scan chunk headers for origin/span before event parsing.
	originNanos, spanNanos, scanErr := scanChunkHeaders(buf)
	if scanErr != nil {
		fmt.Fprintf(os.Stderr, "warning: %v; timeline data may be unavailable\n", scanErr)
	}

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

		info, ok := classifyEvent(p, typ)
		if !ok {
			continue
		}

		counts[info.eventType]++

		if opts.collectTimestamps {
			if _, ok := stackEvents[info.eventType]; !ok {
				continue
			}
			hdr := p.ChunkHeader()
			offsetNanos := ticksToNanos(info.startTicks, hdr.StartTicks, hdr.StartNanos, uint64(originNanos), hdr.TicksPerSecond)

			// Time-range filtering: skip events outside the window.
			if opts.fromNanos >= 0 && offsetNanos < opts.fromNanos {
				continue
			}
			if opts.toNanos >= 0 && offsetNanos >= opts.toNanos {
				continue
			}

			cached := resolveStackTraceCached(p, stackCache, info.stRef)
			if len(cached.frames) == 0 {
				continue
			}
			thread := resolveThread(p, info.thRef)
			timedByEvent[info.eventType] = append(timedByEvent[info.eventType], timedEvent{
				offsetNanos: offsetNanos,
				stackKey:    cached.key,
				frames:      cached.frames,
				lines:       cached.lines,
				thread:      thread,
				weight:      info.weight,
			})
		} else {
			agg, ok := aggByEvent[info.eventType]
			if !ok {
				continue
			}
			appendJFRStackSample(p, stackCache, agg, info.stRef, info.thRef, info.weight)
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
		if total > 10_000_000 && warnedLargeEventCount.CompareAndSwap(false, true) {
			fmt.Fprintf(os.Stderr, "warning: %d events collected; consider using --from/--to to narrow the time window\n", total)
		}
	}

	return &parsedProfile{
		eventCounts:   counts,
		stacksByEvent: stacksByEvent,
		timedEvents:   timedByEvent,
		originNanos:   originNanos,
		spanNanos:     spanNanos,
	}, nil
}

// filterIdleEvents returns timed events whose leaf frame is not idle,
// matching filterIdle() semantics on stackFile.
func filterIdleEvents(events []timedEvent) []timedEvent {
	if events == nil {
		return nil
	}
	var out []timedEvent
	for i := range events {
		if len(events[i].frames) > 0 && isIdleLeaf(events[i].frames[len(events[i].frames)-1]) {
			continue
		}
		out = append(out, events[i])
	}
	return out
}

// ---------------------------------------------------------------------------
// Collapsed-stack text → stackFile
// ---------------------------------------------------------------------------

// openReader opens a file for reading, handling gzip decompression.
func openReader(path string) (io.ReadCloser, error) {
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

type profileFormat int

const (
	formatCollapsed profileFormat = iota
	formatJFR
	formatPprof
)

func detectFormat(path string) profileFormat {
	if path == "-" {
		return formatCollapsed
	}
	p := strings.ToLower(path)
	switch {
	case strings.HasSuffix(p, ".jfr"), strings.HasSuffix(p, ".jfr.gz"):
		return formatJFR
	case strings.HasSuffix(p, ".pb.gz"), strings.HasSuffix(p, ".pb"),
		strings.HasSuffix(p, ".pprof"), strings.HasSuffix(p, ".pprof.gz"):
		return formatPprof
	default:
		return formatCollapsed
	}
}

// parseStructuredProfile dispatches to the appropriate parser for JFR or pprof.
// Returns (nil, nil) for collapsed text format.
func parseStructuredProfile(path string, stackEvents map[string]struct{}) (*parsedProfile, error) {
	switch detectFormat(path) {
	case formatJFR:
		return parseJFRData(path, stackEvents, parseOpts{})
	case formatPprof:
		return parsePprofData(path, stackEvents)
	default:
		return nil, nil
	}
}

func openInput(path, eventType string) (sf *stackFile, hasMetadata bool, err error) {
	format := detectFormat(path)
	switch format {
	case formatJFR:
		parsed, err := parseJFRData(path, singleEventType(eventType), parseOpts{})
		if err != nil {
			return nil, true, err
		}
		sf = parsed.stacksByEvent[eventType]
		if sf == nil {
			sf = &stackFile{}
		}
		return sf, true, nil
	case formatPprof:
		parsed, err := parsePprofData(path, singleEventType(eventType))
		if err != nil {
			return nil, true, err
		}
		sf = parsed.stacksByEvent[eventType]
		if sf == nil {
			sf = &stackFile{}
		}
		return sf, true, nil
	default:
		rc, err := openReader(path)
		if err != nil {
			return nil, false, err
		}
		defer rc.Close()
		sf, err = parseCollapsed(rc)
		return sf, false, err
	}
}

// stdinResult holds the result of parsing stdin with format auto-detection.
type stdinResult struct {
	parsed *parsedProfile // non-nil when stdin contained pprof (gzipped or raw)
	sf     *stackFile     // non-nil when stdin contained collapsed text
}

// parseStdin reads all of stdin and auto-detects the format.
// Binary content (gzip or raw protobuf) → pprof; printable text → collapsed.
// When data looks binary but pprof parsing fails AND the data is valid UTF-8,
// we fall back to collapsed (handles non-ASCII method names like café).
// Invalid UTF-8 that also fails pprof is genuinely corrupt — we surface the error.
func parseStdin(stackEvents map[string]struct{}) (stdinResult, error) {
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return stdinResult{}, err
	}
	if stdinLooksBinary(data) {
		// Binary data — try pprof (profile.Parse handles gzip and raw protobuf).
		parsed, pprofErr := parsePprofFromReader(bytes.NewReader(data), stackEvents)
		if pprofErr == nil {
			return stdinResult{parsed: parsed}, nil
		}
		// pprof failed. If the data is valid UTF-8, it's likely text with
		// non-ASCII characters (e.g. accented method names) — fall back to
		// collapsed. Otherwise it's corrupt binary — surface the error.
		if !utf8.Valid(data) {
			return stdinResult{}, fmt.Errorf("stdin: not valid pprof: %w", pprofErr)
		}
	}
	sf, err := parseCollapsed(bytes.NewReader(data))
	if err != nil {
		return stdinResult{}, err
	}
	return stdinResult{sf: sf}, nil
}

// stdinLooksBinary returns true if data contains non-text bytes, indicating
// binary content (gzip-compressed or raw protobuf) rather than collapsed text.
// Collapsed text is printable ASCII (0x20–0x7E) plus whitespace (\n, \r, \t).
// Anything outside that range — control chars below 0x20 or high bytes >= 0x7F
// (protobuf varints, gzip framing, UTF-8 multibyte) — triggers the binary path.
func stdinLooksBinary(data []byte) bool {
	n := len(data)
	if n > 256 {
		n = 256
	}
	for _, b := range data[:n] {
		if b > 0x7e || (b < 0x20 && b != '\n' && b != '\r' && b != '\t') {
			return true
		}
	}
	return false
}
