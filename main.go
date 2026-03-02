// ap-query: analyze async-profiler profiles (JFR or collapsed text).
//
// Usage:
//
//	ap-query <command> [flags] <file>
//
// Input: .jfr/.jfr.gz → JFR binary; .pb.gz/.pprof → pprof protobuf;
// all other files → collapsed text; stdin (-) → auto-detect (binary = pprof, text = collapsed).
//
// Commands: hot, tree, trace, callers, threads, filter, events, collapse, diff, lines, info, timeline, script
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
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var version = "dev"

// ---------------------------------------------------------------------------
// Shared preprocessing
// ---------------------------------------------------------------------------

type profileContext struct {
	sf            *stackFile
	parsed        *parsedProfile // nil for collapsed input
	hasMetadata   bool
	eventType     string
	eventExplicit bool
	eventCounts   map[string]int
	eventReason   eventSelectionReason
	fromNanos     int64
	toNanos       int64
	spanNanos     int64
	stacksByEvent map[string]*stackFile // for info cross-event summary
}

type preprocessOpts struct {
	eventFlag string
	thread    string
	fromStr   string
	toStr     string
	noIdle    bool
	path      string
	command   string
}

func preprocessProfile(opts preprocessOpts) (*profileContext, error) {
	eventExplicit := opts.eventFlag != ""
	eventType := opts.eventFlag
	if eventType == "" {
		eventType = "cpu"
	}
	if !isKnownEventType(eventType) {
		format := detectFormat(opts.path)
		if format == formatCollapsed && opts.path != "-" {
			return nil, fmt.Errorf("unknown event type %q (valid: %s)", eventType, validEventTypesString())
		}
	}

	// Parse time range.
	window, err := parseDurationWindow("--from", opts.fromStr, "--to", opts.toStr)
	if err != nil {
		return nil, err
	}
	fromNanos := window.fromNanos
	toNanos := window.toNanos
	needTimed := window.specified

	path := opts.path
	cmd := opts.command

	if cmd == "timeline" && detectFormat(path) != formatJFR {
		return nil, fmt.Errorf("timeline requires a JFR file (pprof and collapsed text lack per-sample timestamps)")
	}

	if needTimed && detectFormat(path) != formatJFR {
		fmt.Fprintln(os.Stderr, "warning: --from/--to ignored for non-JFR input (no timestamps)")
		needTimed = false
		fromNanos = -1
		toNanos = -1
	}

	if cmd == "timeline" {
		needTimed = true
	}

	var sf *stackFile
	var parsed *parsedProfile
	hasMetadata := false
	var eventCounts map[string]int
	eventReason := eventReasonUnknown

	format := detectFormat(path)
	switch format {
	case formatJFR:
		hasMetadata = true
		eventsToParse := allEventTypes()
		if eventExplicit {
			eventsToParse = singleEventType(eventType)
		}
		po := parseOpts{warnLargeCount: true}
		if needTimed {
			po.collectTimestamps = true
			po.fromNanos = fromNanos
			po.toNanos = toNanos
		}
		var err error
		parsed, err = parseJFRData(path, eventsToParse, po)
		if err != nil {
			return nil, err
		}

		if fromNanos >= 0 && parsed.spanNanos > 0 && fromNanos >= parsed.spanNanos {
			fmt.Fprintf(os.Stderr, "warning: --from %s is beyond recording duration (%s); result will be empty\n",
				opts.fromStr, formatDuration(parsed.spanNanos))
			fromNanos = parsed.spanNanos
		}
		if toNanos >= 0 && parsed.spanNanos > 0 && toNanos > parsed.spanNanos {
			toNanos = parsed.spanNanos
		}

		if needTimed && parsed.timedEvents != nil {
			filteredCounts := make(map[string]int)
			for et, events := range parsed.timedEvents {
				for _, e := range events {
					filteredCounts[et] += e.weight
				}
			}
			eventCounts = filteredCounts
		} else {
			eventCounts = parsed.eventCounts
		}
		eventType, eventReason = resolveEventType(eventType, eventExplicit, eventCounts)
		sf = parsed.stacksByEvent[eventType]
		if sf == nil {
			sf = &stackFile{}
		}
	case formatPprof:
		hasMetadata = true
		eventsToParse := allEventTypes()
		if eventExplicit {
			eventsToParse = singleEventType(eventType)
		}
		var err error
		parsed, err = parsePprofData(path, eventsToParse)
		if err != nil {
			return nil, err
		}
		eventCounts = parsed.eventCounts
		eventType, eventReason = resolveEventType(eventType, eventExplicit, eventCounts)
		sf = parsed.stacksByEvent[eventType]
		if sf == nil {
			sf = &stackFile{}
		}
	default:
		if path == "-" {
			eventsToParse := allEventTypes()
			if eventExplicit {
				eventsToParse = singleEventType(eventType)
			}
			res, err := parseStdin(eventsToParse)
			if err != nil {
				return nil, err
			}
			if res.parsed != nil {
				hasMetadata = true
				parsed = res.parsed
				eventCounts = parsed.eventCounts
				eventType, eventReason = resolveEventType(eventType, eventExplicit, eventCounts)
				sf = parsed.stacksByEvent[eventType]
				if sf == nil {
					sf = &stackFile{}
				}
			} else {
				sf = res.sf
			}
		} else {
			var err error
			sf, hasMetadata, err = openInput(path, eventType)
			if err != nil {
				return nil, err
			}
		}
	}

	// Post-parse validation: reject explicitly-requested unknown events.
	// For structured formats, check against unfiltered metadata counts
	// (parsed.eventCounts) so --from/--to windows don't cause false
	// rejections. For collapsed text (no metadata), unknown events are
	// always invalid since collapsed format has no event types.
	if eventExplicit && !isKnownEventType(opts.eventFlag) {
		validationCounts := eventCounts
		if parsed != nil {
			validationCounts = parsed.eventCounts
		}
		if validationCounts == nil {
			// Collapsed text — no event metadata exists.
			return nil, fmt.Errorf("unknown event type %q (valid: %s)", eventType, validEventTypesString())
		}
		if validationCounts[eventType] == 0 {
			available := make([]string, 0, len(validationCounts))
			for e := range validationCounts {
				available = append(available, e)
			}
			sort.Strings(available)
			if len(available) == 0 {
				return nil, fmt.Errorf("event %q not found (no events in file)", eventType)
			}
			return nil, fmt.Errorf("event %q not found (available: %s)", eventType, strings.Join(available, ", "))
		}
	}

	// Thread filter (skipped for timeline — it does its own).
	if opts.thread != "" && cmd != "timeline" {
		totalBefore := sf.totalSamples
		sf = sf.filterByThread(opts.thread)
		if totalBefore > 0 {
			fmt.Fprintf(os.Stderr, "Thread filter: %s — %d/%d samples (%.1f%%)\n",
				opts.thread, sf.totalSamples, totalBefore, pctOf(sf.totalSamples, totalBefore))
		}
	}

	// Idle filter (skipped for timeline — it does its own).
	if opts.noIdle && cmd != "timeline" {
		totalBefore := sf.totalSamples
		sf = sf.filterIdle()
		if totalBefore > 0 {
			fmt.Fprintf(os.Stderr, "Idle filter: %d/%d samples remain (%.1f%% idle removed)\n",
				sf.totalSamples, totalBefore, pctOf(totalBefore-sf.totalSamples, totalBefore))
		}
	}

	// Event selection info (skipped for info, timeline).
	if hasMetadata && cmd != "info" && cmd != "timeline" {
		printEventSelectionForSingle(eventType, eventReason, eventCounts)
	}

	// Time window echo (skipped for timeline).
	if needTimed && cmd != "timeline" {
		if fromNanos >= 0 && toNanos >= 0 {
			fmt.Fprintf(os.Stderr, "Window: %s to %s\n", formatDuration(fromNanos), formatDuration(toNanos))
		} else if fromNanos >= 0 {
			fmt.Fprintf(os.Stderr, "Window: %s to end\n", formatDuration(fromNanos))
		} else if toNanos >= 0 {
			fmt.Fprintf(os.Stderr, "Window: start to %s\n", formatDuration(toNanos))
		}
	}

	// Idle hint for wall profiles.
	if eventType == "wall" && !opts.noIdle {
		idleCount := 0
		for i := range sf.stacks {
			st := &sf.stacks[i]
			if len(st.frames) > 0 && isIdleLeaf(st.frames[len(st.frames)-1]) {
				idleCount += st.count
			}
		}
		if sf.totalSamples > 0 && float64(idleCount)/float64(sf.totalSamples) > 0.5 {
			fmt.Fprintf(os.Stderr,
				"Hint: %.0f%% of samples have idle leaf frames; consider --no-idle\n",
				pctOf(idleCount, sf.totalSamples))
		}
	}

	// Build stacksByEvent for info cross-event summary.
	var stacksByEvent map[string]*stackFile
	if parsed != nil {
		if opts.thread == "" {
			stacksByEvent = parsed.stacksByEvent
			if opts.noIdle && stacksByEvent != nil {
				filtered := make(map[string]*stackFile, len(stacksByEvent))
				for k, v := range stacksByEvent {
					filtered[k] = v.filterIdle()
				}
				stacksByEvent = filtered
			}
		}
	}

	var spanNanos int64
	if parsed != nil {
		spanNanos = parsed.spanNanos
	}

	return &profileContext{
		sf:            sf,
		parsed:        parsed,
		hasMetadata:   hasMetadata,
		eventType:     eventType,
		eventExplicit: eventExplicit,
		eventCounts:   eventCounts,
		eventReason:   eventReason,
		fromNanos:     fromNanos,
		toNanos:       toNanos,
		spanNanos:     spanNanos,
		stacksByEvent: stacksByEvent,
	}, nil
}

// ---------------------------------------------------------------------------
// Shared flag helpers
// ---------------------------------------------------------------------------

type sharedFlags struct {
	event  string
	thread string
	from   string
	to     string
	noIdle bool
}

func (s *sharedFlags) register(cmd *cobra.Command) {
	cmd.Flags().StringVarP(&s.event, "event", "e", "", "Event type: cpu, wall, alloc, lock, or hardware counter name (default: cpu)")
	cmd.Flags().StringVarP(&s.thread, "thread", "t", "", "Filter to threads matching substring")
	cmd.Flags().StringVar(&s.from, "from", "", "Start of time window (JFR only)")
	cmd.Flags().StringVar(&s.to, "to", "", "End of time window (JFR only)")
	cmd.Flags().BoolVar(&s.noIdle, "no-idle", false, "Remove idle leaf frames")
}

func (s *sharedFlags) toOpts(path, command string) preprocessOpts {
	return preprocessOpts{
		eventFlag: s.event,
		thread:    s.thread,
		fromStr:   s.from,
		toStr:     s.to,
		noIdle:    s.noIdle,
		path:      path,
		command:   command,
	}
}

// ---------------------------------------------------------------------------
// Root command and main
// ---------------------------------------------------------------------------

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:     "ap-query <command> [flags] <file>",
		Short:   "Analyze profiling data (JFR, pprof, or collapsed text)",
		Version: version,
		Long: `ap-query: analyze profiling data (JFR, pprof, or collapsed text)

Input auto-detection:
  .jfr / .jfr.gz           ->  JFR binary (full feature set)
  .pb.gz / .pb / .pprof    ->  pprof protobuf (no timeline/--from/--to)
  everything else           ->  collapsed-stack text (one "frames count" per line)
  -  (stdin)                ->  auto-detect: binary = pprof, text = collapsed

Examples:
  ap-query info profile.jfr
  ap-query hot profile.jfr --event cpu --top 20
  ap-query hot cpu.pb.gz
  ap-query timeline profile.jfr
  ap-query hot profile.jfr --from 5s --to 10s
  ap-query tree profile.jfr -m HashMap.resize --depth 6
  ap-query diff before.jfr after.pb.gz --min-delta 0.5
  ap-query collapse profile.jfr --event wall | ap-query hot -
  echo "A;B;C 10" | ap-query hot -

Run 'ap-query <command> --help' for command-specific help.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.Help()
			return fmt.Errorf("no command specified")
		},
	}
	root.AddCommand(
		newHotCmd(),
		newTreeCmd(),
		newTraceCmd(),
		newCallersCmd(),
		newThreadsCmd(),
		newFilterCmd(),
		newCollapseCmd(),
		newLinesCmd(),
		newTimelineCmd(),
		newInfoCmd(),
		newDiffCmd(),
		newEventsCmd(),
		newScriptCmd(),
		newInitCmd(),
		newUpdateCmd(),
		newVersionCmd(),
	)
	return root
}

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// ---------------------------------------------------------------------------
// update and version commands
// ---------------------------------------------------------------------------

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version and check for updates",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			printVersion(os.Stdout)
		},
	}
}

func newUpdateCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Download and install the latest release",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			cmdUpdate(force)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "Force update even for dev/go-install builds")
	return cmd
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

func cmdUpdate(force bool) {
	if version == "dev" && !force {
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

	if isGoInstall(execPath) && !force {
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

	updateInstalledSkills(execPath)
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
