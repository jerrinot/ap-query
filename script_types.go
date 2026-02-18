package main

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"go.starlark.net/starlark"
)

// ---------------------------------------------------------------------------
// starlarkProfile
// ---------------------------------------------------------------------------

type starlarkProfile struct {
	sf                *stackFile
	parsed            *parsedJFR
	timedParsed       *parsedJFR   // lazy re-parse with timestamps
	scopedEvents      []timedEvent // non-nil for split-derived profiles (preserves temporal identity)
	event             string
	events            []string
	path              string
	frozen            bool
	scopedOriginNanos int64          // global offset of scope start (0 for root)
	scopedSpanNanos   int64          // duration of scope window in nanos
	isScoped          bool           // true for bucket/split-derived profiles
	stackList         *starlark.List // cached
}

func newStarlarkProfile(sf *stackFile, parsed *parsedJFR, event, path string) *starlarkProfile {
	var events []string
	if parsed != nil {
		for e := range parsed.eventCounts {
			events = append(events, e)
		}
	}
	return &starlarkProfile{
		sf:     sf,
		parsed: parsed,
		event:  event,
		events: events,
		path:   path,
	}
}

func (p *starlarkProfile) scopeEnd(fallback int64) int64 {
	if p.isScoped {
		return p.scopedOriginNanos + p.scopedSpanNanos
	}
	return fallback
}

func (p *starlarkProfile) effectiveDuration() float64 {
	if p.isScoped {
		return float64(p.scopedSpanNanos) / 1e9
	}
	if p.parsed != nil && p.parsed.spanNanos > 0 {
		return float64(p.parsed.spanNanos) / 1e9
	}
	return 0
}

func (p *starlarkProfile) summaryString() string {
	return fmt.Sprintf("%s: %d samples, %.1fs, %d stacks", p.event, p.sf.totalSamples, p.effectiveDuration(), len(p.sf.stacks))
}

func (p *starlarkProfile) String() string        { return p.summaryString() }
func (p *starlarkProfile) Type() string          { return "Profile" }
func (p *starlarkProfile) Freeze()               { p.frozen = true }
func (p *starlarkProfile) Truth() starlark.Bool  { return starlark.True }
func (p *starlarkProfile) Hash() (uint32, error) { return 0, fmt.Errorf("unhashable type: Profile") }

func (p *starlarkProfile) buildStackList() *starlark.List {
	if p.stackList != nil {
		return p.stackList
	}
	elems := make([]starlark.Value, len(p.sf.stacks))
	for i := range p.sf.stacks {
		elems[i] = newStarlarkStack(&p.sf.stacks[i])
	}
	list := starlark.NewList(elems)
	list.Freeze()
	p.stackList = list
	return list
}

func (p *starlarkProfile) Attr(name string) (starlark.Value, error) {
	switch name {
	case "stacks":
		return p.buildStackList(), nil
	case "samples":
		return starlark.MakeInt(p.sf.totalSamples), nil
	case "duration":
		return starlark.Float(p.effectiveDuration()), nil
	case "start":
		if p.isScoped {
			return starlark.Float(float64(p.scopedOriginNanos) / 1e9), nil
		}
		return starlark.Float(0), nil
	case "end":
		if p.isScoped {
			return starlark.Float(float64(p.scopedOriginNanos+p.scopedSpanNanos) / 1e9), nil
		}
		return starlark.Float(p.effectiveDuration()), nil
	case "event":
		return starlark.String(p.event), nil
	case "events":
		elems := make([]starlark.Value, len(p.events))
		for i, e := range p.events {
			elems[i] = starlark.String(e)
		}
		return starlark.NewList(elems), nil
	case "path":
		return starlark.String(p.path), nil
	case "hot":
		return starlark.NewBuiltin("hot", p.methodHot), nil
	case "threads":
		return starlark.NewBuiltin("threads", p.methodThreads), nil
	case "filter":
		return starlark.NewBuiltin("filter", p.methodFilter), nil
	case "group_by":
		return starlark.NewBuiltin("group_by", p.methodGroupBy), nil
	case "timeline":
		return starlark.NewBuiltin("timeline", p.methodTimeline), nil
	case "split":
		return starlark.NewBuiltin("split", p.methodSplit), nil
	case "tree":
		return starlark.NewBuiltin("tree", p.methodTree), nil
	case "trace":
		return starlark.NewBuiltin("trace", p.methodTrace), nil
	case "callers":
		return starlark.NewBuiltin("callers", p.methodCallers), nil
	case "no_idle":
		return starlark.NewBuiltin("no_idle", p.methodNoIdle), nil
	case "summary":
		return starlark.NewBuiltin("summary", p.methodSummary), nil
	}
	return nil, starlark.NoSuchAttrError(fmt.Sprintf("Profile has no .%s attribute", name))
}

func (p *starlarkProfile) AttrNames() []string {
	return []string{"stacks", "samples", "duration", "start", "end", "event", "events", "path", "hot", "threads", "filter", "group_by", "timeline", "split", "tree", "trace", "callers", "no_idle", "summary"}
}

func (p *starlarkProfile) methodHot(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var n int
	var fqn bool
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "n?", &n, "fqn?", &fqn); err != nil {
		return nil, err
	}
	ranked := computeHot(p.sf, fqn)
	if n > 0 && n < len(ranked) {
		ranked = ranked[:n]
	}
	elems := make([]starlark.Value, len(ranked))
	for i, e := range ranked {
		elems[i] = newStarlarkMethod(e, p.sf.totalSamples, fqn)
	}
	return starlark.NewList(elems), nil
}

func (p *starlarkProfile) methodThreads(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var n int
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "n?", &n); err != nil {
		return nil, err
	}
	ranked, _, _ := computeThreads(p.sf)
	if n > 0 && n < len(ranked) {
		ranked = ranked[:n]
	}
	elems := make([]starlark.Value, len(ranked))
	for i, e := range ranked {
		elems[i] = newStarlarkThread(e, p.sf.totalSamples)
	}
	return starlark.NewList(elems), nil
}

func (p *starlarkProfile) methodFilter(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var fn starlark.Callable
	if err := starlark.UnpackPositionalArgs(b.Name(), args, kwargs, 1, &fn); err != nil {
		return nil, err
	}
	newSf := &stackFile{}
	for i := range p.sf.stacks {
		st := &p.sf.stacks[i]
		wrapped := newStarlarkStack(st)
		result, err := starlark.Call(thread, fn, starlark.Tuple{wrapped}, nil)
		if err != nil {
			return nil, err
		}
		if result.Truth() {
			newSf.stacks = append(newSf.stacks, *st)
			newSf.totalSamples += st.count
		}
	}
	child := newStarlarkProfile(newSf, p.parsed, p.event, p.path)
	child.timedParsed = p.timedParsed
	child.scopedEvents = p.scopedEvents
	child.scopedOriginNanos = p.scopedOriginNanos
	child.scopedSpanNanos = p.scopedSpanNanos
	child.isScoped = p.isScoped
	return child, nil
}

func (p *starlarkProfile) methodGroupBy(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var fn starlark.Callable
	if err := starlark.UnpackPositionalArgs(b.Name(), args, kwargs, 1, &fn); err != nil {
		return nil, err
	}
	groups := make(map[string]*stackFile)
	for i := range p.sf.stacks {
		st := &p.sf.stacks[i]
		wrapped := newStarlarkStack(st)
		result, err := starlark.Call(thread, fn, starlark.Tuple{wrapped}, nil)
		if err != nil {
			return nil, err
		}
		if result == starlark.None {
			continue
		}
		key, ok := starlark.AsString(result)
		if !ok {
			return nil, fmt.Errorf("group_by: key function must return a string or None, got %s", result.Type())
		}
		sf := groups[key]
		if sf == nil {
			sf = &stackFile{}
			groups[key] = sf
		}
		sf.stacks = append(sf.stacks, *st)
		sf.totalSamples += st.count
	}
	dict := starlark.NewDict(len(groups))
	for key, sf := range groups {
		child := newStarlarkProfile(sf, p.parsed, p.event, p.path)
		child.timedParsed = p.timedParsed
		child.scopedEvents = p.scopedEvents
		child.scopedOriginNanos = p.scopedOriginNanos
		child.scopedSpanNanos = p.scopedSpanNanos
		child.isScoped = p.isScoped
		dict.SetKey(starlark.String(key), child)
	}
	return dict, nil
}

// computeBucketWidthSafe delegates to computeBucketWidth for Starlark scripts.
// The caller (methodTimeline) applies its own bucket cap (100M), separate from
// the CLI's 10K cap enforced in cmdTimeline.
func computeBucketWidthSafe(bucketSpan int64, buckets int, resolution string) (int, int64, error) {
	return computeBucketWidth(bucketSpan, buckets, resolution)
}

func (p *starlarkProfile) ensureTimedParsed() (*parsedJFR, error) {
	if p.timedParsed != nil {
		return p.timedParsed, nil
	}
	// Check if the original parse already has timed data.
	if p.parsed != nil && p.parsed.timedEvents != nil {
		p.timedParsed = p.parsed
		return p.timedParsed, nil
	}
	// Re-parse with timestamps.
	if !isJFRPath(p.path) {
		return nil, nil // collapsed text has no timestamps
	}
	parsed, err := parseJFRData(p.path, allJFREventTypes(), parseOpts{collectTimestamps: true, fromNanos: -1, toNanos: -1})
	if err != nil {
		return nil, fmt.Errorf("timeline: %v", err)
	}
	p.timedParsed = parsed
	return p.timedParsed, nil
}

// resolveTimedEvents returns the timed events for this profile, respecting both
// temporal scope (from split) and stack-set scope (from filter/group_by/thread).
func (p *starlarkProfile) resolveTimedEvents(timed *parsedJFR) []timedEvent {
	source := timed.timedEvents[p.event]
	if p.scopedEvents != nil {
		source = p.scopedEvents
	}
	return p.profileTimedEvents(source)
}

// profileTimedEvents returns the subset of timed events that belong to this
// profile's current stack set, consuming counts so that totals align with
// p.samples.
func (p *starlarkProfile) profileTimedEvents(all []timedEvent) []timedEvent {
	remaining := make(map[stackKey]int, len(p.sf.stacks))
	for i := range p.sf.stacks {
		st := &p.sf.stacks[i]
		k := stackKey{
			frames: buildStackKeyWithLines(st.frames, st.lines),
			thread: st.thread,
		}
		remaining[k] += st.count
	}

	out := make([]timedEvent, 0, len(all))
	for i := range all {
		e := all[i]
		k := stackKey{frames: e.stackKey, thread: e.thread}
		left := remaining[k]
		if left <= 0 {
			continue
		}
		if e.weight > left {
			e.weight = left
		}
		out = append(out, e)
		remaining[k] = left - e.weight
	}
	return out
}

func (p *starlarkProfile) methodTimeline(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var resolutionVal starlark.Value
	var buckets int
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "resolution?", &resolutionVal, "buckets?", &buckets); err != nil {
		return nil, err
	}

	var resolution string
	if resolutionVal != nil {
		switch v := resolutionVal.(type) {
		case starlark.String:
			resolution = string(v)
		case starlark.Int:
			if len(args) > 0 {
				return nil, fmt.Errorf("timeline: numeric resolution requires keyword form: timeline(resolution=%v)", v)
			}
			n, ok := v.Int64()
			if !ok {
				return nil, fmt.Errorf("timeline: resolution integer too large")
			}
			resolution = fmt.Sprintf("%ds", n)
		case starlark.Float:
			if len(args) > 0 {
				return nil, fmt.Errorf("timeline: numeric resolution requires keyword form: timeline(resolution=%v)", v)
			}
			d := time.Duration(float64(v) * float64(time.Second))
			resolution = d.String()
		case starlark.NoneType:
			// default
		default:
			return nil, fmt.Errorf("timeline: resolution must be string, int, or float, got %s", resolutionVal.Type())
		}
	}

	timed, err := p.ensureTimedParsed()
	if err != nil {
		return nil, err
	}
	if timed == nil {
		return starlark.NewList(nil), nil
	}

	events := p.resolveTimedEvents(timed)
	if len(events) == 0 {
		return starlark.NewList(nil), nil
	}

	bucketOrigin, bucketSpan := resolveBucketRange(-1, -1, timed.spanNanos, events)
	numBuckets, bucketWidth, err := computeBucketWidthSafe(bucketSpan, buckets, resolution)
	if err != nil {
		return nil, err
	}
	const maxBuckets = 100_000_000
	if numBuckets > maxBuckets {
		return nil, fmt.Errorf("timeline: bucket count %d exceeds maximum (%d); use a larger resolution", numBuckets, maxBuckets)
	}

	// Assign events to buckets.
	bucketEvents := make([][]timedEvent, numBuckets)
	for i := range events {
		e := &events[i]
		var idx int
		if bucketSpan == 0 {
			idx = 0
		} else {
			relative := e.offsetNanos - bucketOrigin
			idx = int(relative * int64(numBuckets) / bucketSpan)
		}
		if idx < 0 {
			idx = 0
		}
		if idx >= numBuckets {
			idx = numBuckets - 1
		}
		bucketEvents[idx] = append(bucketEvents[idx], *e)
	}

	elems := make([]starlark.Value, numBuckets)
	for i := 0; i < numBuckets; i++ {
		startNanos := bucketOrigin + int64(i)*bucketWidth
		endNanos := startNanos + bucketWidth
		var samples int
		for _, e := range bucketEvents[i] {
			samples += e.weight
		}
		elems[i] = &starlarkBucket{
			startSec:   float64(startNanos) / 1e9,
			endSec:     float64(endNanos) / 1e9,
			startNanos: startNanos,
			endNanos:   endNanos,
			samples:    samples,
			events:     bucketEvents[i],
			parent:     p,
		}
	}
	return starlark.NewList(elems), nil
}

func (p *starlarkProfile) methodSplit(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var timesList *starlark.List
	if err := starlark.UnpackPositionalArgs(b.Name(), args, kwargs, 1, &timesList); err != nil {
		return nil, err
	}

	timed, err := p.ensureTimedParsed()
	if err != nil {
		return nil, err
	}
	if timed == nil {
		return nil, fmt.Errorf("split: requires JFR data (collapsed text has no timestamps)")
	}

	events := p.resolveTimedEvents(timed)

	// Parse and validate the split times.
	n := timesList.Len()
	splitNanos := make([]int64, n)
	scopeEnd := p.scopeEnd(timed.spanNanos)
	var scopeDurationNanos int64
	if p.isScoped {
		scopeDurationNanos = p.scopedSpanNanos
	} else {
		scopeDurationNanos = timed.spanNanos
	}
	// When span metadata is missing, derive from event range.
	if scopeDurationNanos == 0 && !p.isScoped && len(events) > 0 {
		var maxOffset int64
		for i := range events {
			if events[i].offsetNanos > maxOffset {
				maxOffset = events[i].offsetNanos
			}
		}
		scopeDurationNanos = maxOffset
		scopeEnd = maxOffset
	}
	for i := 0; i < n; i++ {
		v := timesList.Index(i)
		var f float64
		if s, ok := v.(starlark.String); ok {
			d, parseErr := time.ParseDuration(string(s))
			if parseErr != nil {
				return nil, fmt.Errorf("split: invalid duration %q at index %d: %v", string(s), i, parseErr)
			}
			if d < 0 {
				return nil, fmt.Errorf("split: times must be non-negative, got %q at index %d", string(s), i)
			}
			f = d.Seconds()
		} else if numF, ok := starlark.AsFloat(v); ok {
			f = numF
		} else {
			return nil, fmt.Errorf("split: times must be floats or duration strings, got %s", v.Type())
		}
		if f < 0 {
			return nil, fmt.Errorf("split: times must be non-negative, got %g at index %d", f, i)
		}
		nanos := int64(f * 1e9)
		if nanos > scopeDurationNanos {
			return nil, fmt.Errorf("split: time %g exceeds scope duration %g", f, float64(scopeDurationNanos)/1e9)
		}
		if i > 0 && nanos <= splitNanos[i-1] {
			return nil, fmt.Errorf("split: times must be strictly increasing, got %g after %g", f, float64(splitNanos[i-1])/1e9)
		}
		splitNanos[i] = nanos
	}

	// Convert scope-relative split times to global nanos for event comparison.
	globalSplitNanos := make([]int64, n)
	for i, ns := range splitNanos {
		globalSplitNanos[i] = p.scopedOriginNanos + ns
	}

	// Partition events into len(splitNanos)+1 segments.
	segments := make([][]timedEvent, n+1)
	for i := range events {
		e := &events[i]
		seg := n // last segment by default
		for j, boundary := range globalSplitNanos {
			if e.offsetNanos < boundary {
				seg = j
				break
			}
		}
		segments[seg] = append(segments[seg], *e)
	}

	elems := make([]starlark.Value, len(segments))
	for i, seg := range segments {
		sf := buildStackFileFromTimed(seg)
		child := newStarlarkProfile(sf, timed, p.event, p.path)
		child.scopedEvents = seg
		// Compute scoped origin and span for this segment.
		var segStart, segEnd int64
		if i == 0 {
			segStart = p.scopedOriginNanos
		} else {
			segStart = globalSplitNanos[i-1]
		}
		if i < n {
			segEnd = globalSplitNanos[i]
		} else {
			segEnd = scopeEnd
		}
		child.scopedOriginNanos = segStart
		child.scopedSpanNanos = segEnd - segStart
		child.isScoped = true
		elems[i] = child
	}
	return starlark.NewList(elems), nil
}

func (p *starlarkProfile) methodTree(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var method string
	depth := 4
	minPct := 1.0
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "method?", &method, "depth?", &depth, "min_pct?", &minPct); err != nil {
		return nil, err
	}
	return starlark.String(computeTreeString(p.sf, method, depth, minPct)), nil
}

func (p *starlarkProfile) methodTrace(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var method string
	minPct := 0.5
	var fqn bool
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "method", &method, "min_pct?", &minPct, "fqn?", &fqn); err != nil {
		return nil, err
	}
	if method == "" {
		return nil, fmt.Errorf("trace: method must be non-empty")
	}
	return starlark.String(computeTraceString(p.sf, method, minPct, fqn)), nil
}

func (p *starlarkProfile) methodCallers(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var method string
	depth := 4
	minPct := 1.0
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "method", &method, "depth?", &depth, "min_pct?", &minPct); err != nil {
		return nil, err
	}
	if method == "" {
		return nil, fmt.Errorf("callers: method must be non-empty")
	}
	return starlark.String(computeCallersString(p.sf, method, depth, minPct)), nil
}

func (p *starlarkProfile) methodNoIdle(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if err := starlark.UnpackPositionalArgs("no_idle", args, kwargs, 0); err != nil {
		return nil, err
	}
	newSf := p.sf.filterIdle()
	child := newStarlarkProfile(newSf, p.parsed, p.event, p.path)
	child.timedParsed = p.timedParsed
	child.scopedEvents = filterIdleEvents(p.scopedEvents)
	child.scopedOriginNanos = p.scopedOriginNanos
	child.scopedSpanNanos = p.scopedSpanNanos
	child.isScoped = p.isScoped
	return child, nil
}

func (p *starlarkProfile) methodSummary(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if err := starlark.UnpackPositionalArgs(b.Name(), args, kwargs, 0); err != nil {
		return nil, err
	}
	return starlark.String(p.summaryString()), nil
}

// ---------------------------------------------------------------------------
// starlarkBucket
// ---------------------------------------------------------------------------

type starlarkBucket struct {
	startSec   float64
	endSec     float64
	startNanos int64
	endNanos   int64
	samples    int
	events     []timedEvent
	parent     *starlarkProfile // profile that created this bucket
	stacks     *starlark.List   // cached
	profile    *starlarkProfile // cached, lazy
}

func (b *starlarkBucket) String() string {
	return fmt.Sprintf("<Bucket %.1fs-%.1fs samples=%d>", b.startSec, b.endSec, b.samples)
}
func (b *starlarkBucket) Type() string          { return "Bucket" }
func (b *starlarkBucket) Freeze()               {}
func (b *starlarkBucket) Truth() starlark.Bool  { return starlark.True }
func (b *starlarkBucket) Hash() (uint32, error) { return 0, fmt.Errorf("unhashable type: Bucket") }

func (b *starlarkBucket) buildStacks() *starlark.List {
	if b.stacks != nil {
		return b.stacks
	}
	p := b.buildProfile()
	elems := make([]starlark.Value, len(p.sf.stacks))
	for i := range p.sf.stacks {
		elems[i] = newStarlarkStack(&p.sf.stacks[i])
	}
	list := starlark.NewList(elems)
	b.stacks = list
	return list
}

func (b *starlarkBucket) buildProfile() *starlarkProfile {
	if b.profile != nil {
		return b.profile
	}
	sf := buildStackFileFromTimed(b.events)
	child := newStarlarkProfile(sf, b.parent.parsed, b.parent.event, b.parent.path)
	child.timedParsed = b.parent.timedParsed
	child.scopedEvents = b.events
	child.scopedOriginNanos = b.startNanos
	child.scopedSpanNanos = b.endNanos - b.startNanos
	child.isScoped = true
	b.profile = child
	return child
}

func (b *starlarkBucket) Attr(name string) (starlark.Value, error) {
	switch name {
	case "start":
		return starlark.Float(b.startSec), nil
	case "end":
		return starlark.Float(b.endSec), nil
	case "samples":
		return starlark.MakeInt(b.samples), nil
	case "stacks":
		return b.buildStacks(), nil
	case "hot":
		return starlark.NewBuiltin("hot", b.methodHot), nil
	case "profile":
		return b.buildProfile(), nil
	case "label":
		width := b.endNanos - b.startNanos
		startStr := formatTimelineTimestamp(b.startNanos, width)
		endStr := formatTimelineTimestamp(b.endNanos, width)
		return starlark.String(startStr + "-" + endStr), nil
	}
	return nil, starlark.NoSuchAttrError(fmt.Sprintf("Bucket has no .%s attribute", name))
}

func (b *starlarkBucket) AttrNames() []string {
	return []string{"start", "end", "samples", "stacks", "hot", "profile", "label"}
}

func (b *starlarkBucket) methodHot(_ *starlark.Thread, bn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	n := 5
	if err := starlark.UnpackArgs(bn.Name(), args, kwargs, "n?", &n); err != nil {
		return nil, err
	}
	p := b.buildProfile()
	ranked := computeHot(p.sf, false)
	if n > 0 && n < len(ranked) {
		ranked = ranked[:n]
	}
	elems := make([]starlark.Value, len(ranked))
	for i, e := range ranked {
		elems[i] = newStarlarkMethod(e, p.sf.totalSamples, false)
	}
	return starlark.NewList(elems), nil
}

// ---------------------------------------------------------------------------
// starlarkStack
// ---------------------------------------------------------------------------

type starlarkStack struct {
	st        *stack
	frameList *starlark.List // cached
}

func newStarlarkStack(st *stack) *starlarkStack {
	return &starlarkStack{st: st}
}

func (s *starlarkStack) String() string {
	return fmt.Sprintf("<Stack depth=%d samples=%d>", len(s.st.frames), s.st.count)
}
func (s *starlarkStack) Type() string          { return "Stack" }
func (s *starlarkStack) Freeze()               {}
func (s *starlarkStack) Truth() starlark.Bool  { return starlark.True }
func (s *starlarkStack) Hash() (uint32, error) { return 0, fmt.Errorf("unhashable type: Stack") }

func (s *starlarkStack) buildFrameList() *starlark.List {
	if s.frameList != nil {
		return s.frameList
	}
	elems := make([]starlark.Value, len(s.st.frames))
	for i := range s.st.frames {
		var line uint32
		if i < len(s.st.lines) {
			line = s.st.lines[i]
		}
		elems[i] = newStarlarkFrame(s.st.frames[i], line)
	}
	list := starlark.NewList(elems)
	list.Freeze()
	s.frameList = list
	return list
}

func (s *starlarkStack) Attr(name string) (starlark.Value, error) {
	switch name {
	case "frames":
		return s.buildFrameList(), nil
	case "thread":
		return starlark.String(s.st.thread), nil
	case "samples":
		return starlark.MakeInt(s.st.count), nil
	case "leaf":
		if len(s.st.frames) == 0 {
			return starlark.None, nil
		}
		idx := len(s.st.frames) - 1
		var line uint32
		if idx < len(s.st.lines) {
			line = s.st.lines[idx]
		}
		return newStarlarkFrame(s.st.frames[idx], line), nil
	case "root":
		if len(s.st.frames) == 0 {
			return starlark.None, nil
		}
		var line uint32
		if len(s.st.lines) > 0 {
			line = s.st.lines[0]
		}
		return newStarlarkFrame(s.st.frames[0], line), nil
	case "depth":
		return starlark.MakeInt(len(s.st.frames)), nil
	case "has":
		return starlark.NewBuiltin("has", s.methodHas), nil
	case "has_seq":
		return starlark.NewBuiltin("has_seq", s.methodHasSeq), nil
	case "above":
		return starlark.NewBuiltin("above", s.methodAbove), nil
	case "below":
		return starlark.NewBuiltin("below", s.methodBelow), nil
	case "thread_has":
		return starlark.NewBuiltin("thread_has", s.methodThreadHas), nil
	}
	return nil, starlark.NoSuchAttrError(fmt.Sprintf("Stack has no .%s attribute", name))
}

func (s *starlarkStack) AttrNames() []string {
	return []string{"frames", "thread", "samples", "leaf", "root", "depth", "has", "has_seq", "above", "below", "thread_has"}
}

func (s *starlarkStack) methodHas(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var pattern string
	if err := starlark.UnpackPositionalArgs(b.Name(), args, kwargs, 1, &pattern); err != nil {
		return nil, err
	}
	for _, fr := range s.st.frames {
		if matchesMethod(fr, pattern) {
			return starlark.True, nil
		}
	}
	return starlark.False, nil
}

func (s *starlarkStack) methodHasSeq(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("has_seq: requires at least one pattern argument")
	}
	patterns := make([]string, len(args))
	for i, a := range args {
		p, ok := starlark.AsString(a)
		if !ok {
			return nil, fmt.Errorf("has_seq: argument %d must be a string", i)
		}
		patterns[i] = p
	}
	pi := 0
	for _, fr := range s.st.frames {
		if matchesMethod(fr, patterns[pi]) {
			pi++
			if pi >= len(patterns) {
				return starlark.True, nil
			}
		}
	}
	return starlark.False, nil
}

func (s *starlarkStack) methodAbove(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var pattern string
	if err := starlark.UnpackPositionalArgs(b.Name(), args, kwargs, 1, &pattern); err != nil {
		return nil, err
	}
	for i, fr := range s.st.frames {
		if matchesMethod(fr, pattern) {
			// Return frames above (closer to leaf = after this index).
			result := make([]starlark.Value, 0, len(s.st.frames)-i-1)
			for j := i + 1; j < len(s.st.frames); j++ {
				var line uint32
				if j < len(s.st.lines) {
					line = s.st.lines[j]
				}
				result = append(result, newStarlarkFrame(s.st.frames[j], line))
			}
			return starlark.NewList(result), nil
		}
	}
	return starlark.NewList(nil), nil
}

func (s *starlarkStack) methodBelow(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var pattern string
	if err := starlark.UnpackPositionalArgs(b.Name(), args, kwargs, 1, &pattern); err != nil {
		return nil, err
	}
	for i, fr := range s.st.frames {
		if matchesMethod(fr, pattern) {
			// Return frames below (closer to root = before this index).
			result := make([]starlark.Value, i)
			for j := 0; j < i; j++ {
				var line uint32
				if j < len(s.st.lines) {
					line = s.st.lines[j]
				}
				result[j] = newStarlarkFrame(s.st.frames[j], line)
			}
			return starlark.NewList(result), nil
		}
	}
	return starlark.NewList(nil), nil
}

func (s *starlarkStack) methodThreadHas(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var pattern string
	if err := starlark.UnpackPositionalArgs(b.Name(), args, kwargs, 1, &pattern); err != nil {
		return nil, err
	}
	return starlark.Bool(strings.Contains(s.st.thread, pattern)), nil
}

// ---------------------------------------------------------------------------
// starlarkFrame
// ---------------------------------------------------------------------------

type starlarkFrame struct {
	raw    string // original frame string from JFR/collapsed
	line   uint32
	parsed bool
	pkg    string
	cls    string
	method string
}

func newStarlarkFrame(raw string, line uint32) *starlarkFrame {
	return &starlarkFrame{raw: raw, line: line}
}

func (f *starlarkFrame) parse() {
	if f.parsed {
		return
	}
	f.parsed = true

	// Normalize slashes to dots.
	fqn := strings.ReplaceAll(f.raw, "/", ".")

	// Find the method name (last dot-separated segment) and class (second-to-last).
	// Handle native frames (no dots) and shared library frames.

	// Check for .so. pattern (native shared library).
	if strings.Contains(fqn, ".so.") {
		// For "libc.so.6.__sched_yield", method is the short name.
		short := shortName(f.raw)
		f.method = short
		return
	}

	parts := strings.Split(fqn, ".")
	if len(parts) >= 3 {
		f.method = parts[len(parts)-1]
		f.cls = parts[len(parts)-2]
		f.pkg = strings.Join(parts[:len(parts)-2], ".")
	} else if len(parts) == 2 {
		f.method = parts[1]
		f.cls = parts[0]
	} else {
		f.method = fqn
	}
}

func (f *starlarkFrame) String() string        { return shortName(f.raw) }
func (f *starlarkFrame) Type() string          { return "Frame" }
func (f *starlarkFrame) Freeze()               {}
func (f *starlarkFrame) Truth() starlark.Bool  { return starlark.True }
func (f *starlarkFrame) Hash() (uint32, error) { return 0, fmt.Errorf("unhashable type: Frame") }

func (f *starlarkFrame) Attr(name string) (starlark.Value, error) {
	switch name {
	case "name":
		return starlark.String(shortName(f.raw)), nil
	case "fqn":
		return starlark.String(strings.ReplaceAll(f.raw, "/", ".")), nil
	case "pkg":
		f.parse()
		return starlark.String(f.pkg), nil
	case "cls":
		f.parse()
		return starlark.String(f.cls), nil
	case "method":
		f.parse()
		return starlark.String(f.method), nil
	case "line":
		return starlark.MakeInt(int(f.line)), nil
	}
	return nil, starlark.NoSuchAttrError(fmt.Sprintf("Frame has no .%s attribute", name))
}

func (f *starlarkFrame) AttrNames() []string {
	return []string{"name", "fqn", "pkg", "cls", "method", "line"}
}

// ---------------------------------------------------------------------------
// starlarkMethod
// ---------------------------------------------------------------------------

type starlarkMethod struct {
	entry        hotEntry
	totalSamples int
	fqn          bool
}

func newStarlarkMethod(e hotEntry, totalSamples int, fqn bool) *starlarkMethod {
	return &starlarkMethod{entry: e, totalSamples: totalSamples, fqn: fqn}
}

func (m *starlarkMethod) String() string        { return m.entry.name }
func (m *starlarkMethod) Type() string          { return "Method" }
func (m *starlarkMethod) Freeze()               {}
func (m *starlarkMethod) Truth() starlark.Bool  { return starlark.True }
func (m *starlarkMethod) Hash() (uint32, error) { return 0, fmt.Errorf("unhashable type: Method") }

func (m *starlarkMethod) Attr(name string) (starlark.Value, error) {
	switch name {
	case "name":
		return starlark.String(m.entry.name), nil
	case "fqn":
		return starlark.String(m.entry.name), nil
	case "self":
		return starlark.MakeInt(m.entry.selfCount), nil
	case "self_pct":
		return starlark.Float(pctOf(m.entry.selfCount, m.totalSamples)), nil
	case "total":
		return starlark.MakeInt(m.entry.totalCount), nil
	case "total_pct":
		return starlark.Float(pctOf(m.entry.totalCount, m.totalSamples)), nil
	}
	return nil, starlark.NoSuchAttrError(fmt.Sprintf("Method has no .%s attribute", name))
}

func (m *starlarkMethod) AttrNames() []string {
	return []string{"name", "fqn", "self", "self_pct", "total", "total_pct"}
}

// ---------------------------------------------------------------------------
// starlarkThread
// ---------------------------------------------------------------------------

type starlarkThread struct {
	entry        threadEntry
	totalSamples int
}

func newStarlarkThread(e threadEntry, totalSamples int) *starlarkThread {
	return &starlarkThread{entry: e, totalSamples: totalSamples}
}

func (t *starlarkThread) String() string        { return t.entry.name }
func (t *starlarkThread) Type() string          { return "Thread" }
func (t *starlarkThread) Freeze()               {}
func (t *starlarkThread) Truth() starlark.Bool  { return starlark.True }
func (t *starlarkThread) Hash() (uint32, error) { return 0, fmt.Errorf("unhashable type: Thread") }

func (t *starlarkThread) Attr(name string) (starlark.Value, error) {
	switch name {
	case "name":
		return starlark.String(t.entry.name), nil
	case "samples":
		return starlark.MakeInt(t.entry.samples), nil
	case "pct":
		return starlark.Float(pctOf(t.entry.samples, t.totalSamples)), nil
	}
	return nil, starlark.NoSuchAttrError(fmt.Sprintf("Thread has no .%s attribute", name))
}

func (t *starlarkThread) AttrNames() []string {
	return []string{"name", "samples", "pct"}
}

// ---------------------------------------------------------------------------
// starlarkDiff
// ---------------------------------------------------------------------------

type scriptDiffEntry struct {
	name   string
	fqn    string
	before float64
	after  float64
	delta  float64
}

type starlarkDiff struct {
	regressions  []scriptDiffEntry
	improvements []scriptDiffEntry
	added        []scriptDiffEntry
	removed      []scriptDiffEntry
	all          []scriptDiffEntry
}

func computeScriptDiff(before, after *stackFile, minDelta float64, top int, fqn bool) *starlarkDiff {
	beforePctFQN := selfPcts(before, true)
	afterPctFQN := selfPcts(after, true)

	var beforePct, afterPct map[string]float64
	var fqnMap map[string]string

	if fqn {
		beforePct = beforePctFQN
		afterPct = afterPctFQN
	} else {
		beforePct = selfPcts(before, false)
		afterPct = selfPcts(after, false)
		// Build FQN lookup: short name → fqn name.
		fqnMap = make(map[string]string)
		for f := range beforePctFQN {
			short := shortName(strings.ReplaceAll(f, ".", "/"))
			fqnMap[short] = f
		}
		for f := range afterPctFQN {
			short := shortName(strings.ReplaceAll(f, ".", "/"))
			fqnMap[short] = f
		}
	}

	allMethods := make(map[string]bool)
	for m := range beforePct {
		allMethods[m] = true
	}
	for m := range afterPct {
		allMethods[m] = true
	}

	var regressions, improvements, added, removed, allEntries []scriptDiffEntry

	for m := range allMethods {
		b := beforePct[m]
		a := afterPct[m]
		delta := a - b

		var entryFQN string
		if fqn {
			entryFQN = m
		} else {
			entryFQN = fqnMap[m]
			if entryFQN == "" {
				entryFQN = m
			}
		}

		entryName := m
		_, inBefore := beforePct[m]
		_, inAfter := afterPct[m]

		if inBefore && inAfter {
			if math.Abs(delta) < minDelta {
				continue
			}
			entry := scriptDiffEntry{name: entryName, fqn: entryFQN, before: b, after: a, delta: delta}
			allEntries = append(allEntries, entry)
			if delta > 0 {
				regressions = append(regressions, entry)
			} else {
				improvements = append(improvements, entry)
			}
		} else if !inBefore && inAfter {
			if a < minDelta {
				continue
			}
			entry := scriptDiffEntry{name: entryName, fqn: entryFQN, before: 0, after: a, delta: a}
			added = append(added, entry)
			allEntries = append(allEntries, entry)
		} else if inBefore && !inAfter {
			if b < minDelta {
				continue
			}
			entry := scriptDiffEntry{name: entryName, fqn: entryFQN, before: b, after: 0, delta: -b}
			removed = append(removed, entry)
			allEntries = append(allEntries, entry)
		}
	}

	sort.Slice(regressions, func(i, j int) bool { return regressions[i].delta > regressions[j].delta })
	sort.Slice(improvements, func(i, j int) bool { return improvements[i].delta < improvements[j].delta })
	sort.Slice(added, func(i, j int) bool { return added[i].after > added[j].after })
	sort.Slice(removed, func(i, j int) bool { return removed[i].before > removed[j].before })
	sort.Slice(allEntries, func(i, j int) bool { return math.Abs(allEntries[i].delta) > math.Abs(allEntries[j].delta) })

	regressions = regressions[:truncate(len(regressions), top)]
	improvements = improvements[:truncate(len(improvements), top)]
	added = added[:truncate(len(added), top)]
	removed = removed[:truncate(len(removed), top)]
	allEntries = allEntries[:truncate(len(allEntries), top)]

	return &starlarkDiff{
		regressions:  regressions,
		improvements: improvements,
		added:        added,
		removed:      removed,
		all:          allEntries,
	}
}

func diffEntryList(entries []scriptDiffEntry) *starlark.List {
	elems := make([]starlark.Value, len(entries))
	for i, e := range entries {
		elems[i] = &starlarkDiffEntry{entry: e}
	}
	return starlark.NewList(elems)
}

func (d *starlarkDiff) String() string        { return fmt.Sprintf("<Diff entries=%d>", len(d.all)) }
func (d *starlarkDiff) Type() string          { return "Diff" }
func (d *starlarkDiff) Freeze()               {}
func (d *starlarkDiff) Truth() starlark.Bool  { return starlark.True }
func (d *starlarkDiff) Hash() (uint32, error) { return 0, fmt.Errorf("unhashable type: Diff") }

func (d *starlarkDiff) Attr(name string) (starlark.Value, error) {
	switch name {
	case "regressions":
		return diffEntryList(d.regressions), nil
	case "improvements":
		return diffEntryList(d.improvements), nil
	case "added":
		return diffEntryList(d.added), nil
	case "removed":
		return diffEntryList(d.removed), nil
	case "all":
		return diffEntryList(d.all), nil
	}
	return nil, starlark.NoSuchAttrError(fmt.Sprintf("Diff has no .%s attribute", name))
}

func (d *starlarkDiff) AttrNames() []string {
	return []string{"regressions", "improvements", "added", "removed", "all"}
}

// ---------------------------------------------------------------------------
// starlarkDiffEntry
// ---------------------------------------------------------------------------

type starlarkDiffEntry struct {
	entry scriptDiffEntry
}

func (e *starlarkDiffEntry) String() string       { return e.entry.name }
func (e *starlarkDiffEntry) Type() string         { return "DiffEntry" }
func (e *starlarkDiffEntry) Freeze()              {}
func (e *starlarkDiffEntry) Truth() starlark.Bool { return starlark.True }
func (e *starlarkDiffEntry) Hash() (uint32, error) {
	return 0, fmt.Errorf("unhashable type: DiffEntry")
}

func (e *starlarkDiffEntry) Attr(name string) (starlark.Value, error) {
	switch name {
	case "name":
		return starlark.String(e.entry.name), nil
	case "fqn":
		return starlark.String(e.entry.fqn), nil
	case "before":
		return starlark.Float(e.entry.before), nil
	case "after":
		return starlark.Float(e.entry.after), nil
	case "delta":
		return starlark.Float(e.entry.delta), nil
	}
	return nil, starlark.NoSuchAttrError(fmt.Sprintf("DiffEntry has no .%s attribute", name))
}

func (e *starlarkDiffEntry) AttrNames() []string {
	return []string{"name", "fqn", "before", "after", "delta"}
}
