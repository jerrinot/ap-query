// ap-query: analyze async-profiler profiles (JFR or collapsed text).
//
// Usage:
//
//	ap-query <command> [flags] <file>
//
// Input: .jfr/.jfr.gz files are parsed as JFR binary; all other files and
// stdin (-) are parsed as collapsed text.
//
// Commands: hot, tree, callers, threads, filter, events, collapse, diff, lines, info
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------------------
// CLI
// ---------------------------------------------------------------------------

func usage() {
	fmt.Fprintf(os.Stderr, `ap-query: analyze async-profiler profiles (JFR or collapsed text)

Usage:
  ap-query <command> [flags] <file>

Input auto-detection:
  .jfr / .jfr.gz  →  JFR binary (supports --event selection)
  everything else  →  collapsed-stack text (one "frames count" per line)
  -                →  collapsed text from stdin

Commands:
  info      One-shot triage: events (JFR only), top threads, top hot methods.
  hot       Rank methods by self-time (leaf cost).
  tree      Call tree descending from a method (-m required).
  callers   Callers ascending to a method (-m required).
  lines     Source-line breakdown inside a method (-m required).
  threads   Thread sample distribution.
  diff      Compare two profiles: shows REGRESSION / IMPROVEMENT / NEW / GONE.
  collapse  Emit collapsed-stack text (useful for piping JFR output).
  filter    Output stacks passing through a method (-m required).
  events    List event types in a JFR file (JFR only).

Global flags:
  --event TYPE, -e TYPE   Event type: cpu (default), wall, alloc, lock. JFR only.
  -t THREAD               Filter stacks to threads matching substring.

Command-specific flags:
  -m METHOD, --method METHOD   Substring match against method names.
  --top N                      Limit output rows (default: 10 for hot; unlimited for diff, lines, threads).
  --depth N                    Max tree/callers depth (default: 4).
  --min-pct F                  Hide tree/callers nodes below this %% (default: 1.0).
  --min-delta F                Hide diff entries below this %% change (default: 0.5).
  --fqn                        Show fully-qualified names instead of Class.method.
  --assert-below F             Exit 1 if top method self%% >= F (for CI gates).
  --include-callers            Include caller frames in filter output.

Examples:
  ap-query info profile.jfr
  ap-query hot profile.jfr --event cpu --top 20
  ap-query tree profile.jfr -m HashMap.resize --depth 6
  ap-query callers profile.jfr -m HashMap.resize
  ap-query lines profile.jfr -m HashMap.resize
  ap-query hot profile.jfr -t "http-nio" --assert-below 15.0
  ap-query diff before.jfr after.jfr --min-delta 0.5
  ap-query collapse profile.jfr --event wall | ap-query hot -
  echo "A;B;C 10" | ap-query hot -
`)
	os.Exit(2)
}

// ---------------------------------------------------------------------------
// Flag parser
// ---------------------------------------------------------------------------

type flags struct {
	args  []string // positional
	vals  map[string]string
	bools map[string]bool
}

func parseFlags(args []string) flags {
	f := flags{vals: make(map[string]string), bools: make(map[string]bool)}
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "--" {
			f.args = append(f.args, args[i+1:]...)
			break
		}
		if strings.HasPrefix(a, "--") || (strings.HasPrefix(a, "-") && len(a) == 2) {
			key := strings.TrimLeft(a, "-")
			// Known boolean flags
			switch key {
			case "fqn", "include-callers":
				f.bools[key] = true
				i++
				continue
			}
			// Value flags
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				f.vals[key] = args[i+1]
				i += 2
				continue
			}
			// Flag with no value — treat as boolean
			f.bools[key] = true
			i++
			continue
		}
		f.args = append(f.args, a)
		i++
	}
	return f
}

func (f *flags) str(keys ...string) string {
	for _, k := range keys {
		if v, ok := f.vals[k]; ok {
			return v
		}
	}
	return ""
}

func (f *flags) intVal(keys []string, def int) int {
	for _, k := range keys {
		if v, ok := f.vals[k]; ok {
			n, err := strconv.Atoi(v)
			if err != nil {
				fmt.Fprintf(os.Stderr, "error: invalid integer value for --%s: %q\n", k, v)
				os.Exit(2)
			}
			return n
		}
	}
	return def
}

func (f *flags) floatVal(keys []string, def float64) float64 {
	for _, k := range keys {
		if v, ok := f.vals[k]; ok {
			n, err := strconv.ParseFloat(v, 64)
			if err != nil {
				fmt.Fprintf(os.Stderr, "error: invalid numeric value for --%s: %q\n", k, v)
				os.Exit(2)
			}
			return n
		}
	}
	return def
}

func (f *flags) boolean(keys ...string) bool {
	for _, k := range keys {
		if f.bools[k] {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// main
// ---------------------------------------------------------------------------

func main() {
	if len(os.Args) < 2 {
		usage()
	}

	cmd := os.Args[1]
	if cmd == "-h" || cmd == "--help" || cmd == "help" {
		usage()
	}
	f := parseFlags(os.Args[2:])

	eventType := f.str("event", "e")
	if eventType == "" {
		eventType = "cpu"
	}
	switch eventType {
	case "cpu", "wall", "alloc", "lock":
	default:
		fmt.Fprintf(os.Stderr, "error: unknown event type %q (valid: cpu, wall, alloc, lock)\n", eventType)
		os.Exit(2)
	}

	if cmd == "events" {
		if len(f.args) < 1 {
			usage()
		}
		if !isJFRPath(f.args[0]) {
			fmt.Fprintln(os.Stderr, "error: events command requires a JFR file")
			os.Exit(2)
		}
		cmdEvents(f.args[0])
		return
	}

	// diff requires two positional args
	if cmd == "diff" {
		if len(f.args) < 2 {
			fmt.Fprintln(os.Stderr, "error: diff requires two files")
			os.Exit(2)
		}
		thread := f.str("t", "thread")
		fqn := f.boolean("fqn")
		minDelta := f.floatVal([]string{"min-delta"}, 0.5)

		before, _, err := openInput(f.args[0], eventType)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		after, _, err := openInput(f.args[1], eventType)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		if thread != "" {
			before = before.filterByThread(thread)
			after = after.filterByThread(thread)
		}
		top := f.intVal([]string{"top"}, 0)
		cmdDiff(before, after, minDelta, top, fqn)
		return
	}

	if len(f.args) < 1 {
		usage()
	}
	path := f.args[0]
	thread := f.str("t", "thread")

	sf, isJFR, err := openInput(path, eventType)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if thread != "" {
		sf = sf.filterByThread(thread)
	}

	switch cmd {
	case "hot":
		top := f.intVal([]string{"top"}, 10)
		fqn := f.boolean("fqn")
		assertBelow := f.floatVal([]string{"assert-below"}, 0)
		cmdHot(sf, top, fqn, assertBelow)

	case "tree":
		method := f.str("m", "method")
		if method == "" {
			fmt.Fprintln(os.Stderr, "error: -m/--method required")
			os.Exit(2)
		}
		depth := f.intVal([]string{"depth"}, 4)
		minPct := f.floatVal([]string{"min-pct"}, 1.0)
		cmdTree(sf, method, depth, minPct)

	case "callers":
		method := f.str("m", "method")
		if method == "" {
			fmt.Fprintln(os.Stderr, "error: -m/--method required")
			os.Exit(2)
		}
		depth := f.intVal([]string{"depth"}, 4)
		minPct := f.floatVal([]string{"min-pct"}, 1.0)
		cmdCallers(sf, method, depth, minPct)

	case "threads":
		top := f.intVal([]string{"top"}, 0)
		cmdThreads(sf, top)

	case "filter":
		method := f.str("m", "method")
		if method == "" {
			fmt.Fprintln(os.Stderr, "error: -m/--method required")
			os.Exit(2)
		}
		inclCallers := f.boolean("include-callers")
		cmdFilter(sf, method, inclCallers)

	case "collapse":
		cmdCollapse(sf)

	case "lines":
		method := f.str("m", "method")
		if method == "" {
			fmt.Fprintln(os.Stderr, "error: -m/--method required")
			os.Exit(2)
		}
		top := f.intVal([]string{"top"}, 0)
		fqn := f.boolean("fqn")
		cmdLines(sf, method, top, fqn)

	case "info":
		cmdInfo(path, sf, eventType, isJFR)

	default:
		usage()
	}
}
