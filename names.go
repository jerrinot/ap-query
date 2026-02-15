package main

import (
	"fmt"
	"regexp"
	"strings"
)

func shortName(frame string) string {
	base := strings.ReplaceAll(frame, "/", ".")

	// Native frames from shared libraries: "libc.so.6.__sched_yield" → "__sched_yield"
	if soIdx := strings.Index(base, ".so."); soIdx >= 0 {
		after := base[soIdx+4:]
		// Skip version components (all-digit parts like "6" in libc.so.6)
		for {
			dot := strings.IndexByte(after, '.')
			if dot < 0 {
				break
			}
			allDigits := true
			for _, c := range after[:dot] {
				if c < '0' || c > '9' {
					allDigits = false
					break
				}
			}
			if !allDigits {
				break
			}
			after = after[dot+1:]
		}
		if after != "" {
			return after
		}
	}

	// Java frames: "com/example/App.process" → "App.process"
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

// idleJavaPatterns match Java idle frames by exact class name and method
// prefix on the short name (e.g. "Object.wait", "Object.wait0").
var idleJavaPatterns = []struct{ class, method string }{
	{"Thread", "sleep"},
	{"Object", "wait"},
	{"LockSupport", "park"},
	{"Unsafe", "park"},
}

// idleNativePatterns match native idle frames by prefix on the short name.
var idleNativePatterns = []string{
	"__futex",
	"__sched_yield",
	"epoll_wait",
	"pthread_cond_wait",
	"pthread_cond_timedwait",
}

func isIdleLeaf(frame string) bool {
	short := shortName(frame)

	// Java frames: exact class + method prefix (avoids matching
	// e.g. TransactionObject.waitFor against Object.wait).
	if dot := strings.LastIndexByte(short, '.'); dot >= 0 {
		class := short[:dot]
		method := short[dot+1:]
		for _, p := range idleJavaPatterns {
			if class == p.class && strings.HasPrefix(method, p.method) {
				return true
			}
		}
	}

	// Native frames (no dot): prefix match.
	for _, p := range idleNativePatterns {
		if strings.HasPrefix(short, p) {
			return true
		}
	}
	return false
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
