// ap-query: analyze async-profiler profiles (JFR or collapsed text).
//
// Usage:
//
//	ap-query <command> [flags] <file>
//
// Input: .jfr/.jfr.gz files are parsed as JFR binary; all other files and
// stdin (-) are parsed as collapsed text.
//
// Commands: hot, tree, trace, callers, threads, filter, events, collapse, diff, lines, info
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

var version = "dev"

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
  info      One-shot triage: events, threads, hot methods, and drill-down.
  hot       Rank methods by self-time and total-time.
  tree      Call tree descending from a method (optional -m; shows all if omitted).
  trace     Hottest path from a method to leaf (-m required).
  callers   Callers ascending to a method (-m required).
  lines     Source-line breakdown inside a method (-m required).
  threads   Thread sample distribution.
  diff      Compare two profiles: shows REGRESSION / IMPROVEMENT / NEW / GONE.
  collapse  Emit collapsed-stack text (useful for piping JFR output).
  filter    Output stacks passing through a method (-m required).
  events    List event types in a JFR file (JFR only).
  version   Print version and check for updates.
  update    Download and install the latest release.
  init      Install agent skill for JFR profiling analysis.

Global flags:
  --event TYPE, -e TYPE   Event type: cpu (default), wall, alloc, lock. JFR only.
  -t THREAD               Filter stacks to threads matching substring.

Command-specific flags:
  -m METHOD, --method METHOD   Substring match against method names.
  --top N                      Limit output rows (default: 10 for hot; unlimited for diff, lines, threads).
  --depth N                    Max tree/callers depth (default: 4).
  --min-pct F                  Hide tree/callers/trace nodes below this %% (default: 1.0; trace default: 0.5).
  --min-delta F                Hide diff entries below this %% change (default: 0.5).
  --fqn                        Show fully-qualified names instead of Class.method.
  --assert-below F             Exit 1 if top method self%% >= F (for CI gates).
  --expand N                   Auto-expand top N hot methods in info (default: 3, 0=off).
  --top-threads N              Threads shown in info (default: 10, 0=all).
  --top-methods N              Hot methods shown in info (default: 20, 0=all).
  --include-callers            Include caller frames in filter output.

Examples:
  ap-query info profile.jfr
  ap-query hot profile.jfr --event cpu --top 20
  ap-query tree profile.jfr -m HashMap.resize --depth 6
  ap-query trace profile.jfr -m HashMap.resize --min-pct 0.5
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
			case "fqn", "include-callers", "force", "project", "claude", "codex", "stdout":
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
	if cmd == "version" || cmd == "--version" {
		printVersion(os.Stdout)
		return
	}
	if cmd == "update" {
		cmdUpdate()
		return
	}
	f := parseFlags(os.Args[2:])

	if cmd == "init" {
		cmdInit(initOpts{
			asprof:  f.str("asprof"),
			force:   f.boolean("force"),
			project: f.boolean("project"),
			claude:  f.boolean("claude"),
			codex:   f.boolean("codex"),
			stdout:  f.boolean("stdout"),
		})
		return
	}

	eventExplicit := f.str("event", "e") != ""
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
		if err := cmdEvents(f.args[0]); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
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
		if err := cmdHot(sf, top, fqn, assertBelow); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}

	case "tree":
		method := f.str("m", "method")
		depth := f.intVal([]string{"depth"}, 4)
		minPct := f.floatVal([]string{"min-pct"}, 1.0)
		cmdTree(sf, method, depth, minPct)

	case "trace":
		method := f.str("m", "method")
		if method == "" {
			fmt.Fprintln(os.Stderr, "error: -m/--method required")
			os.Exit(2)
		}
		minPct := f.floatVal([]string{"min-pct"}, 0.5)
		fqn := f.boolean("fqn")
		cmdTrace(sf, method, minPct, fqn)

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
		if err := cmdLines(sf, method, top, fqn); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}

	case "info":
		expand := f.intVal([]string{"expand"}, 3)
		topThreads := f.intVal([]string{"top-threads"}, 10)
		topMethods := f.intVal([]string{"top-methods"}, 20)
		var eventCounts map[string]int
		if isJFR {
			eventCounts, _ = discoverEvents(path)
			if !eventExplicit && len(eventCounts) > 0 && eventCounts[eventType] == 0 {
				// Default event type has no samples; pick the dominant one.
				best, bestN := "", 0
				for name, n := range eventCounts {
					if n > bestN {
						best, bestN = name, n
					}
				}
				if best != "" && best != eventType {
					eventType = best
					sf, _, err = openInput(path, eventType)
					if err != nil {
						fmt.Fprintf(os.Stderr, "error: %v\n", err)
						os.Exit(1)
					}
					if thread != "" {
						sf = sf.filterByThread(thread)
					}
				}
			}
		}
		cmdInfo(sf, eventType, isJFR, eventCounts, expand, topThreads, topMethods)

	default:
		usage()
	}
}

// ---------------------------------------------------------------------------
// version
// ---------------------------------------------------------------------------

func printVersion(w *os.File) {
	fmt.Fprintf(w, "ap-query version %s\n", version)
	latest := checkLatestVersion()
	if latest != "" && latest != version && latest != "v"+version {
		fmt.Fprintf(w, "A newer version is available: %s\n", latest)
		fmt.Fprintf(w, "  https://github.com/jerrinot/ap-query/releases/latest\n")
	}
}

func checkLatestVersion() string {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("https://api.github.com/repos/jerrinot/ap-query/releases/latest")
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return ""
	}
	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return ""
	}
	return release.TagName
}

// ---------------------------------------------------------------------------
// update
// ---------------------------------------------------------------------------

func cmdUpdate() {
	if version == "dev" {
		fmt.Fprintln(os.Stderr, "error: cannot self-update a dev build; use 'go install' or download a release binary")
		os.Exit(1)
	}

	execPath, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot determine executable path: %v\n", err)
		os.Exit(1)
	}
	execPath, err = filepath.EvalSymlinks(execPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot resolve executable path: %v\n", err)
		os.Exit(1)
	}

	if isGoInstall(execPath) {
		fmt.Fprintln(os.Stderr, "It looks like ap-query was installed via 'go install'.")
		fmt.Fprintln(os.Stderr, "Please update with:  go install github.com/jerrinot/ap-query@latest")
		return
	}

	latest := checkLatestVersion()
	if latest == "" {
		fmt.Fprintln(os.Stderr, "error: could not check latest version (network error?)")
		os.Exit(1)
	}

	currentNorm := strings.TrimPrefix(version, "v")
	latestNorm := strings.TrimPrefix(latest, "v")
	if currentNorm == latestNorm {
		fmt.Printf("ap-query %s is already the latest version.\n", version)
		return
	}

	fmt.Printf("Updating ap-query %s → %s ...\n", version, latest)

	client := &http.Client{Timeout: 30 * time.Second}

	// Download and parse checksums
	checksumsURL := downloadURL(latest, "checksums.txt")
	resp, err := client.Get(checksumsURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: downloading checksums: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		fmt.Fprintf(os.Stderr, "error: downloading checksums: HTTP %d\n", resp.StatusCode)
		os.Exit(1)
	}
	checksums, err := parseChecksums(resp.Body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: parsing checksums: %v\n", err)
		os.Exit(1)
	}

	archive := archiveName()
	expectedHash, ok := checksums[archive]
	if !ok {
		fmt.Fprintf(os.Stderr, "error: no checksum found for %s\n", archive)
		os.Exit(1)
	}

	// Download and verify archive
	archiveURL := downloadURL(latest, archive)
	archiveData, err := downloadAndVerify(archiveURL, expectedHash, client)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	// Extract binary
	binaryData, err := extractBinary(archiveData)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	// Replace current binary
	if err := replaceBinary(execPath, binaryData); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Successfully updated to ap-query %s\n", latest)
}

func isGoInstall(execPath string) bool {
	dir := filepath.Dir(execPath)

	if gobin := os.Getenv("GOBIN"); gobin != "" && dir == gobin {
		return true
	}

	gopath := os.Getenv("GOPATH")
	if gopath == "" {
		home, err := os.UserHomeDir()
		if err == nil {
			gopath = filepath.Join(home, "go")
		}
	}
	if gopath != "" && dir == filepath.Join(gopath, "bin") {
		return true
	}

	goroot := runtime.GOROOT()
	if goroot != "" && dir == filepath.Join(goroot, "bin") {
		return true
	}
	return false
}

func archiveName() string {
	return fmt.Sprintf("ap-query_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
}

func downloadURL(tag, filename string) string {
	return fmt.Sprintf("https://github.com/jerrinot/ap-query/releases/download/%s/%s", tag, filename)
}

func parseChecksums(r io.Reader) (map[string]string, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	result := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) != 2 {
			continue
		}
		hash := parts[0]
		filename := parts[1]
		result[filename] = hash
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("no checksums found")
	}
	return result, nil
}

func downloadAndVerify(url, expectedHash string, client *http.Client) ([]byte, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("downloading %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("downloading %s: HTTP %d", url, resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %v", err)
	}

	h := sha256.Sum256(data)
	actual := hex.EncodeToString(h[:])
	if actual != expectedHash {
		return nil, fmt.Errorf("checksum mismatch: expected %s, got %s", expectedHash, actual)
	}
	return data, nil
}

func extractBinary(archiveData []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archiveData))
	if err != nil {
		return nil, fmt.Errorf("decompressing archive: %v", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading tar: %v", err)
		}
		if filepath.Base(hdr.Name) == "ap-query" && hdr.Typeflag == tar.TypeReg {
			data, err := io.ReadAll(tr)
			if err != nil {
				return nil, fmt.Errorf("extracting binary: %v", err)
			}
			return data, nil
		}
	}
	return nil, fmt.Errorf("ap-query binary not found in archive")
}

func replaceBinary(execPath string, newBinary []byte) error {
	dir := filepath.Dir(execPath)

	// Get permissions of old binary
	info, err := os.Stat(execPath)
	if err != nil {
		return fmt.Errorf("stat %s: %v", execPath, err)
	}
	mode := info.Mode().Perm()

	// Write to temp file in same directory (required for atomic rename)
	tmp, err := os.CreateTemp(dir, "ap-query-update-*")
	if err != nil {
		return fmt.Errorf("creating temp file: %v", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // cleanup on failure

	if _, err := tmp.Write(newBinary); err != nil {
		tmp.Close()
		return fmt.Errorf("writing temp file: %v", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temp file: %v", err)
	}

	if err := os.Chmod(tmpPath, mode); err != nil {
		return fmt.Errorf("setting permissions: %v", err)
	}

	if err := os.Rename(tmpPath, execPath); err != nil {
		return fmt.Errorf("replacing binary: %v", err)
	}
	return nil
}
