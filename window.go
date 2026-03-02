package main

import (
	"fmt"
	"time"
)

type durationWindow struct {
	fromNanos int64
	toNanos   int64
	specified bool
}

func parseDurationWindow(fromName, fromRaw, toName, toRaw string) (durationWindow, error) {
	w := durationWindow{
		fromNanos: -1,
		toNanos:   -1,
	}
	if fromRaw == "" && toRaw == "" {
		return w, nil
	}
	w.specified = true

	if fromRaw != "" {
		d, err := time.ParseDuration(fromRaw)
		if err != nil {
			return w, fmt.Errorf("invalid %s value %q: %v", fromName, fromRaw, err)
		}
		w.fromNanos = d.Nanoseconds()
		if w.fromNanos < 0 {
			return w, fmt.Errorf("%s must not be negative (got %s)", fromName, fromRaw)
		}
	}

	if toRaw != "" {
		d, err := time.ParseDuration(toRaw)
		if err != nil {
			return w, fmt.Errorf("invalid %s value %q: %v", toName, toRaw, err)
		}
		w.toNanos = d.Nanoseconds()
		if w.toNanos < 0 {
			return w, fmt.Errorf("%s must not be negative (got %s)", toName, toRaw)
		}
	}

	if w.fromNanos >= 0 && w.toNanos >= 0 && w.toNanos < w.fromNanos {
		return w, fmt.Errorf("%s must be >= %s", toName, fromName)
	}

	return w, nil
}
