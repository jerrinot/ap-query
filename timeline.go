package main

import (
	"fmt"
	"math"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

const timelineHelp = `Usage: ap-query timeline [flags] <file>

Sample distribution over time (JFR only).

Flags:
  --buckets N                  Number of time buckets (default: auto ~20).
  --resolution DURATION        Fixed bucket width (e.g. 1s, 500ms). Overrides --buckets.
  -m METHOD, --method METHOD   Only count samples containing METHOD.
  --no-top-method              Omit per-bucket hot method annotation.
  --hide REGEX                 Remove matching frames before analysis.
  --event TYPE, -e TYPE        Event type (default: cpu).
  -t THREAD                    Filter to threads matching substring.
  --from DURATION              Start of time window.
  --to DURATION                End of time window.
  --no-idle                    Remove idle leaf frames.

Examples:
  ap-query timeline profile.jfr
  ap-query timeline profile.jfr --from 12s --to 14s --resolution 500ms
`

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

func computeBucketWidth(bucketSpan int64, buckets int, resolution string) (numBuckets int, bucketWidth int64) {
	numBuckets = buckets

	if bucketSpan == 0 {
		return 1, 0
	} else if resolution != "" {
		d, err := time.ParseDuration(resolution)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: invalid --resolution value %q: %v\n", resolution, err)
			os.Exit(2)
		}
		bucketWidth = d.Nanoseconds()
		if bucketWidth <= 0 {
			fmt.Fprintln(os.Stderr, "error: --resolution must be positive")
			os.Exit(2)
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
	const maxBuckets = 10_000
	if numBuckets > maxBuckets {
		fmt.Fprintf(os.Stderr, "error: bucket count %d exceeds maximum (%d); use a larger --resolution or fewer --buckets\n", numBuckets, maxBuckets)
		os.Exit(2)
	}
	if bucketWidth == 0 && bucketSpan > 0 {
		bucketWidth = bucketSpan / int64(numBuckets)
		if bucketWidth == 0 {
			bucketWidth = 1
		}
	}
	return
}

func cmdTimeline(parsed *parsedJFR, eventType string,
	buckets int, resolution string, method string, topMethod bool,
	noIdle bool, hide *regexp.Regexp, thread string, fromNanos, toNanos int64) {

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

	if method != "" && matchedWeight == 0 {
		noMatchMessage(os.Stdout, stackFileFromEvents(preMethodEvents), method)
		return
	}

	numBuckets, bucketWidth := computeBucketWidth(bucketSpan, buckets, resolution)

	// Assign events to buckets.
	bucketCounts := make([]int, numBuckets)
	type bucketData struct {
		eventIdxs []int
	}
	var perBucket []bucketData
	if topMethod {
		perBucket = make([]bucketData, numBuckets)
	}

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
		bucketCounts[idx] += e.weight
		if topMethod {
			perBucket[idx].eventIdxs = append(perBucket[idx].eventIdxs, i)
		}
	}

	// Print header.
	durationStr := formatDuration(bucketSpan)
	widthStr := formatBucketWidth(bucketWidth)
	if method != "" {
		fmt.Printf("Duration: %s  Buckets: %d (%s each)  Event: %s  Matched: %d/%d\n",
			durationStr, numBuckets, widthStr, eventType, matchedWeight, totalWeight)
	} else {
		fmt.Printf("Duration: %s  Buckets: %d (%s each)  Event: %s  Total: %d\n",
			durationStr, numBuckets, widthStr, eventType, matchedWeight)
	}
	fmt.Println()

	// Compute peak threshold (>2x median).
	sortedCounts := make([]int, numBuckets)
	copy(sortedCounts, bucketCounts)
	sort.Ints(sortedCounts)
	median := sortedCounts[numBuckets/2]
	peakThreshold := median * 2

	// Find max for bar scaling.
	maxCount := 0
	for _, c := range bucketCounts {
		if c > maxCount {
			maxCount = c
		}
	}

	// Print column header.
	if topMethod {
		fmt.Printf("%-17s %7s  %-40s  %s\n", "Time", "Samples", "", "Hot Method (self)")
	} else {
		fmt.Printf("%-17s %7s\n", "Time", "Samples")
	}

	const barWidth = 40
	for i := 0; i < numBuckets; i++ {
		start := bucketOrigin + int64(i)*bucketWidth
		end := start + bucketWidth

		startStr := formatTimelineTimestamp(start, bucketWidth)
		endStr := formatTimelineTimestamp(end, bucketWidth)
		timeLabel := startStr + "-" + endStr

		count := bucketCounts[i]

		// Bar.
		barLen := 0
		if maxCount > 0 {
			barLen = count * barWidth / maxCount
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
				pct := pctOf(topCount, count)
				topMethodStr = fmt.Sprintf("  %s (%.0f%%)", topName, pct)
			}
		}

		if topMethod {
			fmt.Printf("%-17s %7d  %-40s%s%s\n", timeLabel, count, bar, topMethodStr, peak)
		} else {
			fmt.Printf("%-17s %7d  %s%s\n", timeLabel, count, bar, peak)
		}
	}
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
