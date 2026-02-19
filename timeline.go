package main

import (
	"fmt"
	"math"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

func newTimelineCmd() *cobra.Command {
	var shared sharedFlags
	var buckets int
	var resolution string
	var method string
	var noTopMethod bool
	var topN int
	var pctFlag bool
	var hide string
	cmd := &cobra.Command{
		Use:   "timeline <file>",
		Short: "Sample distribution over time (JFR only)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			pctx, err := preprocessProfile(shared.toOpts(args[0], "timeline"))
			if err != nil {
				return err
			}
			var hideRe *regexp.Regexp
			if hide != "" {
				re, err := regexp.Compile(hide)
				if err != nil {
					return fmt.Errorf("invalid --hide regex: %v", err)
				}
				hideRe = re
			}
			return cmdTimeline(pctx.parsed, pctx.eventType, buckets, resolution, method,
				!noTopMethod, shared.noIdle, hideRe, shared.thread,
				pctx.fromNanos, pctx.toNanos, topN, pctFlag)
		},
	}
	shared.register(cmd)
	cmd.Flags().IntVar(&buckets, "buckets", 0, "Number of time buckets (default: auto ~20)")
	cmd.Flags().StringVar(&resolution, "resolution", "", "Fixed bucket width (e.g. 1s, 500ms)")
	cmd.Flags().StringVarP(&method, "method", "m", "", "Only count samples containing METHOD")
	cmd.Flags().BoolVar(&noTopMethod, "no-top-method", false, "Omit per-bucket hot method annotation")
	cmd.Flags().IntVar(&topN, "top", 0, "Show only the N highest-sample buckets")
	cmd.Flags().BoolVar(&pctFlag, "pct", false, "Show method percentage per bucket")
	cmd.Flags().StringVar(&hide, "hide", "", "Remove matching frames before analysis (regex)")
	return cmd
}

func resolveBucketRange(fromNanos, toNanos, span int64, events []timedEvent) (bucketOrigin, bucketSpan int64) {
	if fromNanos >= 0 {
		bucketOrigin = fromNanos
	}
	if fromNanos >= 0 && toNanos >= 0 {
		bucketSpan = toNanos - fromNanos
	} else if fromNanos >= 0 && span > 0 && span > fromNanos {
		bucketSpan = span - fromNanos
	} else if fromNanos >= 0 {
		// fromNanos at or beyond recording span — effective span is zero.
	} else if toNanos >= 0 {
		bucketSpan = toNanos
	} else if span > 0 {
		bucketSpan = span
	} else if len(events) > 0 {
		// Zero span from chunk headers: derive from event range.
		maxOffset := events[0].offsetNanos
		for i := range events {
			if events[i].offsetNanos > maxOffset {
				maxOffset = events[i].offsetNanos
			}
		}
		bucketSpan = maxOffset - bucketOrigin
	}
	return
}

func computeBucketWidth(bucketSpan int64, buckets int, resolution string) (numBuckets int, bucketWidth int64, err error) {
	numBuckets = buckets

	if bucketSpan == 0 {
		return 1, 0, nil
	} else if resolution != "" {
		d, parseErr := time.ParseDuration(resolution)
		if parseErr != nil {
			return 0, 0, fmt.Errorf("invalid resolution %q: %v", resolution, parseErr)
		}
		bucketWidth = d.Nanoseconds()
		if bucketWidth <= 0 {
			return 0, 0, fmt.Errorf("resolution must be positive")
		}
		numBuckets = int(math.Ceil(float64(bucketSpan) / float64(bucketWidth)))
		if numBuckets < 1 {
			numBuckets = 1
		}
	} else if numBuckets <= 0 {
		// Auto: ~20 buckets, clamped [5, 40].
		numBuckets = int(float64(bucketSpan) / float64(time.Second) / 1.5)
		if numBuckets < 5 {
			numBuckets = 5
		}
		if numBuckets > 40 {
			numBuckets = 40
		}
	}
	if bucketWidth == 0 && bucketSpan > 0 {
		bucketWidth = bucketSpan / int64(numBuckets)
		if bucketWidth == 0 {
			bucketWidth = 1
		}
	}
	return numBuckets, bucketWidth, nil
}

func cmdTimeline(parsed *parsedProfile, eventType string,
	buckets int, resolution string, method string, topMethod bool,
	noIdle bool, hide *regexp.Regexp, thread string, fromNanos, toNanos int64,
	topN int, pct bool) error {

	events := parsed.timedEvents[eventType]
	bucketOrigin, bucketSpan := resolveBucketRange(fromNanos, toNanos, parsed.spanNanos, events)

	// Idle filtering for timeline.
	if noIdle {
		var totalBefore int
		for i := range events {
			totalBefore += events[i].weight
		}
		var filtered []timedEvent
		for i := range events {
			if len(events[i].frames) == 0 || !isIdleLeaf(events[i].frames[len(events[i].frames)-1]) {
				filtered = append(filtered, events[i])
			}
		}
		events = filtered
		var filteredWeight int
		for i := range events {
			filteredWeight += events[i].weight
		}
		if totalBefore > 0 {
			fmt.Fprintf(os.Stderr, "Idle filter: %d/%d samples remain (%.1f%% idle removed)\n",
				filteredWeight, totalBefore, pctOf(totalBefore-filteredWeight, totalBefore))
		}
	}

	// Hide matching frames so they don't appear as the hot method.
	if hide != nil {
		for i := range events {
			var frames []string
			for _, fr := range events[i].frames {
				if !matchesHide(fr, hide) {
					frames = append(frames, fr)
				}
			}
			events[i].frames = frames
		}
	}

	// Thread filtering for timeline (applied here, not in main).
	if thread != "" {
		var totalBefore int
		for i := range events {
			totalBefore += events[i].weight
		}
		var filtered []timedEvent
		for i := range events {
			if strings.Contains(events[i].thread, thread) {
				filtered = append(filtered, events[i])
			}
		}
		events = filtered
		var filteredWeight int
		for i := range events {
			filteredWeight += events[i].weight
		}
		if totalBefore > 0 {
			fmt.Fprintf(os.Stderr, "Thread filter: %s — %d/%d samples (%.1f%%)\n",
				thread, filteredWeight, totalBefore, pctOf(filteredWeight, totalBefore))
		}
	}

	// Count total weight before method filtering.
	var totalWeight int
	for i := range events {
		totalWeight += events[i].weight
	}

	// Method filtering.
	preMethodEvents := events
	var matchedWeight int
	if method != "" {
		var methodFiltered []timedEvent
		for i := range events {
			for _, fr := range events[i].frames {
				if matchesMethod(fr, method) {
					methodFiltered = append(methodFiltered, events[i])
					matchedWeight += events[i].weight
					break
				}
			}
		}
		events = methodFiltered
	} else {
		matchedWeight = totalWeight
	}

	if pct && method == "" {
		return fmt.Errorf("--pct requires --method")
	}

	if method != "" && matchedWeight == 0 {
		noMatchMessage(os.Stdout, stackFileFromEvents(preMethodEvents), method)
		return nil
	}

	numBuckets, bucketWidth, err := computeBucketWidth(bucketSpan, buckets, resolution)
	if err != nil {
		return err
	}
	const maxBuckets = 10_000
	if numBuckets > maxBuckets {
		return fmt.Errorf("bucket count %d exceeds maximum (%d); use a larger --resolution or fewer --buckets", numBuckets, maxBuckets)
	}

	// Assign events to buckets.
	bucketCounts := make([]int, numBuckets)
	type bucketData struct {
		eventIdxs []int
	}
	var perBucket []bucketData
	if topMethod {
		perBucket = make([]bucketData, numBuckets)
	}

	assignBucket := func(offsetNanos int64) int {
		if bucketSpan == 0 {
			return 0
		}
		idx := int((offsetNanos - bucketOrigin) * int64(numBuckets) / bucketSpan)
		if idx < 0 {
			idx = 0
		}
		if idx >= numBuckets {
			idx = numBuckets - 1
		}
		return idx
	}

	for i := range events {
		e := &events[i]
		idx := assignBucket(e.offsetNanos)
		bucketCounts[idx] += e.weight
		if topMethod {
			perBucket[idx].eventIdxs = append(perBucket[idx].eventIdxs, i)
		}
	}

	// Total (pre-method-filter) counts per bucket for --pct mode.
	var totalBucketCounts []int
	if pct {
		totalBucketCounts = make([]int, numBuckets)
		for i := range preMethodEvents {
			idx := assignBucket(preMethodEvents[i].offsetNanos)
			totalBucketCounts[idx] += preMethodEvents[i].weight
		}
	}

	// --top N: select highest-sample buckets.
	var displayBuckets []int
	if topN > 0 {
		type idxCount struct {
			idx, count int
		}
		var nonEmpty []idxCount
		for i, c := range bucketCounts {
			if c > 0 {
				nonEmpty = append(nonEmpty, idxCount{i, c})
			}
		}
		sort.Slice(nonEmpty, func(a, b int) bool {
			return nonEmpty[a].count > nonEmpty[b].count
		})
		n := topN
		if n > len(nonEmpty) {
			n = len(nonEmpty)
		}
		displayBuckets = make([]int, n)
		for i := 0; i < n; i++ {
			displayBuckets[i] = nonEmpty[i].idx
		}
		sort.Ints(displayBuckets) // restore time order
	}

	// Print header.
	durationStr := formatDuration(bucketSpan)
	widthStr := formatBucketWidth(bucketWidth)
	header := fmt.Sprintf("Duration: %s  Buckets: %d (%s each)  Event: %s", durationStr, numBuckets, widthStr, eventType)
	if topN > 0 {
		header += fmt.Sprintf("  Top: %d", topN)
	}
	if method != "" {
		header += fmt.Sprintf("  Matched: %d/%d", matchedWeight, totalWeight)
	} else {
		header += fmt.Sprintf("  Total: %d", matchedWeight)
	}
	fmt.Println(header)
	fmt.Println()

	// Compute peak threshold (>2x median) from ALL buckets.
	sortedCounts := make([]int, numBuckets)
	copy(sortedCounts, bucketCounts)
	sort.Ints(sortedCounts)
	median := sortedCounts[numBuckets/2]
	peakThreshold := median * 2

	// Find max for bar scaling from displayed buckets only.
	maxCount := 0
	maxPct := 0.0
	if displayBuckets != nil {
		for _, i := range displayBuckets {
			if bucketCounts[i] > maxCount {
				maxCount = bucketCounts[i]
			}
			if pct && totalBucketCounts[i] > 0 {
				p := 100.0 * float64(bucketCounts[i]) / float64(totalBucketCounts[i])
				if p > maxPct {
					maxPct = p
				}
			}
		}
	} else {
		for i := 0; i < numBuckets; i++ {
			if bucketCounts[i] > maxCount {
				maxCount = bucketCounts[i]
			}
			if pct && totalBucketCounts[i] > 0 {
				p := 100.0 * float64(bucketCounts[i]) / float64(totalBucketCounts[i])
				if p > maxPct {
					maxPct = p
				}
			}
		}
	}

	// Print column header.
	valueCol := "Samples"
	if pct {
		valueCol = "    Pct"
	}
	if topMethod {
		fmt.Printf("%-17s %7s  %-40s  %s\n", "Time", valueCol, "", "Hot Method (self)")
	} else {
		fmt.Printf("%-17s %7s\n", "Time", valueCol)
	}

	const barWidth = 40

	// Build iteration list.
	var iterBuckets []int
	if displayBuckets != nil {
		iterBuckets = displayBuckets
	} else {
		iterBuckets = make([]int, numBuckets)
		for i := range iterBuckets {
			iterBuckets[i] = i
		}
	}

	for _, i := range iterBuckets {
		start := bucketOrigin + int64(i)*bucketWidth
		end := start + bucketWidth

		startStr := formatTimelineTimestamp(start, bucketWidth)
		endStr := formatTimelineTimestamp(end, bucketWidth)
		timeLabel := startStr + "-" + endStr

		count := bucketCounts[i]

		// Value column (count or percentage).
		var valueStr string
		var barFraction float64
		if pct {
			var pctVal float64
			if totalBucketCounts[i] > 0 {
				pctVal = 100.0 * float64(count) / float64(totalBucketCounts[i])
			}
			valueStr = fmt.Sprintf("%5.1f%%", pctVal)
			if maxPct > 0 {
				barFraction = pctVal / maxPct
			}
		} else {
			valueStr = fmt.Sprintf("%7d", count)
			if maxCount > 0 {
				barFraction = float64(count) / float64(maxCount)
			}
		}

		// Bar.
		barLen := int(barFraction * barWidth)
		if barLen > barWidth {
			barLen = barWidth
		}
		bar := strings.Repeat("\u2588", barLen)

		// Peak annotation.
		peak := ""
		if count > peakThreshold && peakThreshold > 0 {
			peak = "  <- peak"
		}

		// Top method annotation.
		topMethodStr := ""
		if topMethod && count > 0 {
			topName := ""
			topCount := 0
			bucketSelfCounts := make(map[string]int)
			for _, eventIdx := range perBucket[i].eventIdxs {
				ev := events[eventIdx]
				if len(ev.frames) == 0 {
					continue
				}
				leaf := displayName(ev.frames[len(ev.frames)-1], false)
				newCount := bucketSelfCounts[leaf] + ev.weight
				bucketSelfCounts[leaf] = newCount
				if newCount > topCount {
					topCount = newCount
					topName = leaf
				}
			}
			if topName != "" && topCount > 0 {
				methodPct := pctOf(topCount, count)
				topMethodStr = fmt.Sprintf("  %s (%.0f%%)", topName, methodPct)
			}
		}

		if topMethod {
			fmt.Printf("%-17s %s  %-40s%s%s\n", timeLabel, valueStr, bar, topMethodStr, peak)
		} else {
			fmt.Printf("%-17s %s  %s%s\n", timeLabel, valueStr, bar, peak)
		}
	}
	return nil
}

func timelinePrecision(bucketWidth int64) int {
	switch {
	case bucketWidth <= 0:
		return 1
	case bucketWidth >= int64(100*time.Millisecond):
		return 1
	case bucketWidth >= int64(10*time.Millisecond):
		return 2
	case bucketWidth >= int64(time.Millisecond):
		return 3
	case bucketWidth >= int64(100*time.Microsecond):
		return 4
	case bucketWidth >= int64(10*time.Microsecond):
		return 5
	case bucketWidth >= int64(time.Microsecond):
		return 6
	case bucketWidth >= 100:
		return 7
	case bucketWidth >= 10:
		return 8
	default:
		return 9
	}
}

func formatTimelineTimestamp(nanos int64, bucketWidth int64) string {
	if nanos < 0 {
		nanos = 0
	}
	precision := timelinePrecision(bucketWidth)
	totalSec := float64(nanos) / float64(time.Second)
	if totalSec < 60 {
		return fmt.Sprintf("%.*fs", precision, totalSec)
	}
	minutes := int(totalSec) / 60
	sec := totalSec - float64(minutes*60)
	return fmt.Sprintf("%dm%0*.*fs", minutes, precision+3, precision, sec)
}

func formatBucketWidth(nanos int64) string {
	if nanos < 0 {
		nanos = 0
	}
	if nanos >= int64(time.Second) {
		return formatDuration(nanos)
	}
	switch {
	case nanos == 0:
		return "0.0s"
	case nanos%int64(time.Millisecond) == 0:
		return fmt.Sprintf("%dms", nanos/int64(time.Millisecond))
	case nanos%int64(time.Microsecond) == 0:
		return fmt.Sprintf("%dus", nanos/int64(time.Microsecond))
	default:
		// Non-round sub-ms values: show fractional ms (truncated).
		ms := float64(nanos) / float64(time.Millisecond)
		return fmt.Sprintf("%.1fms", ms)
	}
}

func formatDuration(nanos int64) string {
	if nanos < 0 {
		nanos = 0
	}
	d := time.Duration(nanos) * time.Nanosecond
	totalSec := d.Seconds()
	if totalSec < 60 {
		return fmt.Sprintf("%.1fs", totalSec)
	}
	minutes := int(totalSec) / 60
	sec := totalSec - float64(minutes*60)
	return fmt.Sprintf("%dm%.1fs", minutes, sec)
}
