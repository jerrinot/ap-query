package main

import (
	"fmt"
	"regexp"
	"strings"
)

func shortName(frame string) string {
	// "com/example/App.process" → "App.process"
	// "com.example.App.process" → "App.process"
	base := strings.ReplaceAll(frame, "/", ".")
	parts := strings.Split(base, ".")
	if len(parts) >= 2 {
		return parts[len(parts)-2] + "." + parts[len(parts)-1]
	}
	return base
}

func displayName(frame string, fqn bool) string {
	if fqn {
		return strings.ReplaceAll(frame, "/", ".")
	}
	return shortName(frame)
}

func matchesMethod(frame, pattern string) bool {
	normalized := strings.ReplaceAll(frame, "/", ".")
	return strings.Contains(normalized, pattern) || strings.Contains(shortName(frame), pattern)
}

func matchesHide(frame string, re *regexp.Regexp) bool {
	normalized := strings.ReplaceAll(frame, "/", ".")
	return re.MatchString(normalized) || re.MatchString(shortName(frame))
}

func truncate(n, top int) int {
	if top > 0 && top < n {
		return top
	}
	return n
}

func pctOf(n, total int) float64 {
	return 100.0 * float64(n) / float64(total)
}

func threadPrefix(thread string) string {
	if thread == "" {
		return ""
	}
	return fmt.Sprintf("[%s];", thread)
}
