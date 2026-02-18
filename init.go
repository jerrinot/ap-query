package main

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

func newInitCmd() *cobra.Command {
	var opts initOpts
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Install agent skill for JFR profiling analysis",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			cmdInit(opts)
		},
	}
	cmd.Flags().StringVar(&opts.asprof, "asprof", "", "Path to asprof binary")
	cmd.Flags().BoolVar(&opts.force, "force", false, "Overwrite existing skill file")
	cmd.Flags().BoolVar(&opts.project, "project", false, "Install to project directory instead of global")
	cmd.Flags().BoolVar(&opts.claude, "claude", false, "Target Claude agent")
	cmd.Flags().BoolVar(&opts.codex, "codex", false, "Target Codex agent")
	cmd.Flags().BoolVar(&opts.stdout, "stdout", false, "Print rendered skill to stdout instead of installing")
	return cmd
}

//go:embed skill_template.md
var skillTemplate string

// skillDir returns the absolute skill directory for an agent.
// Codex uses .agents for project-local installs, .codex (or $CODEX_HOME) for global.
func skillDir(agent, baseDir string, project bool) string {
	switch agent {
	case "claude":
		return filepath.Join(baseDir, ".claude", "skills", "jfr")
	case "codex":
		if project {
			return filepath.Join(baseDir, ".agents", "skills", "jfr")
		}
		base := filepath.Join(baseDir, ".codex")
		if h := os.Getenv("CODEX_HOME"); h != "" {
			base = h
		}
		return filepath.Join(base, "skills", "jfr")
	default:
		panic("unknown agent: " + agent)
	}
}

type initOpts struct {
	asprof  string
	force   bool
	project bool
	claude  bool
	codex   bool
	stdout  bool
}

func cmdInit(opts initOpts) {
	// Resolve ap-query's own path
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot determine ap-query path: %v\n", err)
		os.Exit(1)
	}
	apQueryPath, err := filepath.EvalSymlinks(exe)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot resolve ap-query path: %v\n", err)
		os.Exit(1)
	}

	// Find asprof: explicit flag > PATH/common dirs > ask user (path or download)
	asprofPath := opts.asprof
	if asprofPath != "" {
		asprofPath = expandPath(asprofPath)
		if _, err := os.Stat(asprofPath); err != nil {
			fmt.Fprintf(os.Stderr, "error: asprof not found at %s\n", asprofPath)
			os.Exit(1)
		}
	} else {
		asprofPath = findAsprof()
		if asprofPath != "" {
			fmt.Fprintf(os.Stderr, "Found asprof: %s\n", asprofPath)
		} else {
			asprofPath = promptOrDownloadAsprof(opts.stdout)
		}
	}

	// Render template
	content := strings.ReplaceAll(skillTemplate, "{{AP_QUERY_PATH}}", apQueryPath)
	content = strings.ReplaceAll(content, "{{ASPROF_PATH}}", asprofPath)

	// --stdout: dump and exit
	if opts.stdout {
		fmt.Print(content)
		return
	}

	// Determine base directory
	var baseDir string
	if opts.project {
		baseDir, err = os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: cannot determine working directory: %v\n", err)
			os.Exit(1)
		}
	} else {
		baseDir, err = os.UserHomeDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: cannot determine home directory: %v\n", err)
			os.Exit(1)
		}
	}

	// Determine which agents to target
	targets := resolveTargets(baseDir, opts.claude, opts.codex, opts.project)
	if len(targets) == 0 {
		if opts.project {
			fmt.Fprintln(os.Stderr, "error: no agent configuration found (neither .claude nor .agents exists)")
		} else {
			fmt.Fprintln(os.Stderr, "error: no agent configuration found (neither .claude nor .codex exists)")
		}
		fmt.Fprintln(os.Stderr, "  use --claude or --codex to create one explicitly")
		os.Exit(1)
	}

	// Write skill file for each target
	for _, t := range targets {
		writeSkill(baseDir, t, content, opts.force, opts.project)
	}

	fmt.Fprintf(os.Stderr, "  ap-query: %s\n", apQueryPath)
	fmt.Fprintf(os.Stderr, "  asprof:   %s\n", asprofPath)
}

// resolveTargets decides which agent directories to install to.
// If explicit flags are set, use those (creating dirs as needed).
// Otherwise auto-detect which agent config dirs exist under baseDir.
func resolveTargets(baseDir string, claude, codex, project bool) []string {
	if claude || codex {
		var targets []string
		if claude {
			targets = append(targets, "claude")
		}
		if codex {
			targets = append(targets, "codex")
		}
		return targets
	}

	// Auto-detect: check which agent root dirs exist
	var targets []string
	for _, agent := range []string{"claude", "codex"} {
		// Config root is 2 levels above the skill dir (above skills/jfr)
		configDir := filepath.Dir(filepath.Dir(skillDir(agent, baseDir, project)))
		if _, err := os.Stat(configDir); err == nil {
			targets = append(targets, agent)
		}
	}
	return targets
}

func writeSkill(baseDir, agent, content string, force, project bool) {
	dir := skillDir(agent, baseDir, project)
	if err := os.MkdirAll(dir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot create directory %s: %v\n", dir, err)
		os.Exit(1)
	}
	skillPath := filepath.Join(dir, "SKILL.md")
	if _, err := os.Stat(skillPath); err == nil && !force {
		fmt.Fprintf(os.Stderr, "error: %s already exists (use --force to overwrite)\n", skillPath)
		os.Exit(1)
	}
	if err := os.WriteFile(skillPath, []byte(content), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot write %s: %v\n", skillPath, err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "Skill installed: %s\n", skillPath)
}

func findAsprof() string {
	// Try PATH first
	p, err := exec.LookPath("asprof")
	if err == nil {
		abs, err := filepath.Abs(p)
		if err == nil {
			return abs
		}
		return p
	}

	// Search common directories
	for _, dir := range asprofSearchDirs() {
		candidate := filepath.Join(dir, "asprof")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return ""
}

func asprofSearchDirs() []string {
	dirs := []string{
		"/opt/async-profiler/bin",
		"/opt/homebrew/bin",
		"/usr/local/bin",
	}

	home, err := os.UserHomeDir()
	if err == nil {
		dirs = append(dirs,
			filepath.Join(home, ".ap-query", "bin"),
			filepath.Join(home, ".sdkman", "candidates", "java", "current", "bin"),
			filepath.Join(home, ".local", "bin"),
		)
	}
	return dirs
}

// promptOrDownloadAsprof asks the user to provide a path or download automatically.
// In non-interactive mode (stdout), it downloads directly.
func promptOrDownloadAsprof(nonInteractive bool) string {
	if nonInteractive {
		// --stdout mode: no prompt, just download
		fmt.Fprintln(os.Stderr, "asprof not found, downloading async-profiler...")
		p, err := downloadAsprof()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			fmt.Fprintln(os.Stderr, "  install async-profiler manually and use --asprof PATH")
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "Installed asprof: %s\n", p)
		return p
	}

	fmt.Fprintln(os.Stderr, "asprof not found.")
	fmt.Fprintf(os.Stderr, "Enter path to asprof, or press Enter to download automatically: ")
	scanner := bufio.NewScanner(os.Stdin)
	if scanner.Scan() {
		p := strings.TrimSpace(scanner.Text())
		if p != "" {
			p = expandPath(p)
			if _, err := os.Stat(p); err != nil {
				fmt.Fprintf(os.Stderr, "error: asprof not found at %s\n", p)
				os.Exit(1)
			}
			return p
		}
	}

	// Empty input or EOF: download
	fmt.Fprintln(os.Stderr, "Downloading async-profiler...")
	result, err := downloadAsprof()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		fmt.Fprintln(os.Stderr, "  install async-profiler manually and use --asprof PATH")
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "Installed asprof: %s\n", result)
	return result
}

// downloadAsprof fetches the latest async-profiler release and extracts it to ~/.ap-query/.
// Returns the absolute path to the asprof binary.
func downloadAsprof() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %v", err)
	}

	// Get latest release tag
	tag, err := asprofLatestTag()
	if err != nil {
		return "", fmt.Errorf("cannot check latest async-profiler version: %v", err)
	}
	ver := strings.TrimPrefix(tag, "v")

	// Build download URL
	url, isTarGz := asprofDownloadURL(tag, ver)

	// Download
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return "", fmt.Errorf("downloading %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("downloading %s: HTTP %d", url, resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading download: %v", err)
	}

	// Extract to ~/.ap-query/
	installDir := filepath.Join(home, ".ap-query")
	if isTarGz {
		err = extractTarGz(data, installDir)
	} else {
		err = extractZip(data, installDir)
	}
	if err != nil {
		return "", fmt.Errorf("extracting async-profiler: %v", err)
	}

	asprofPath := filepath.Join(installDir, "bin", "asprof")
	if _, err := os.Stat(asprofPath); err != nil {
		return "", fmt.Errorf("asprof binary not found after extraction")
	}
	return asprofPath, nil
}

func asprofLatestTag() (string, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get("https://api.github.com/repos/async-profiler/async-profiler/releases/latest")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("GitHub API returned HTTP %d", resp.StatusCode)
	}
	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", err
	}
	if release.TagName == "" {
		return "", fmt.Errorf("empty tag in GitHub response")
	}
	return release.TagName, nil
}

// asprofDownloadURL returns the download URL and whether it's a tar.gz (vs zip).
func asprofDownloadURL(tag, ver string) (string, bool) {
	base := "https://github.com/async-profiler/async-profiler/releases/download/" + tag + "/"
	if runtime.GOOS == "darwin" {
		return base + "async-profiler-" + ver + "-macos.zip", false
	}
	arch := "x64"
	if runtime.GOARCH == "arm64" {
		arch = "arm64"
	}
	return base + "async-profiler-" + ver + "-linux-" + arch + ".tar.gz", true
}

// extractTarGz extracts a tar.gz archive into destDir, flattening the top-level directory.
// e.g. async-profiler-4.3-linux-x64/bin/asprof → destDir/bin/asprof
func extractTarGz(data []byte, destDir string) error {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		// Strip top-level directory
		parts := strings.SplitN(hdr.Name, "/", 2)
		if len(parts) < 2 || parts[1] == "" {
			continue
		}
		rel := parts[1]

		target := filepath.Join(destDir, rel)
		if !strings.HasPrefix(target, filepath.Clean(destDir)+string(filepath.Separator)) {
			continue // skip paths that escape destDir
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
		}
	}
	return nil
}

// extractZip extracts a zip archive into destDir, flattening the top-level directory.
func extractZip(data []byte, destDir string) error {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return err
	}

	for _, f := range zr.File {
		// Strip top-level directory
		parts := strings.SplitN(f.Name, "/", 2)
		if len(parts) < 2 || parts[1] == "" {
			continue
		}
		rel := parts[1]

		target := filepath.Join(destDir, rel)
		if !strings.HasPrefix(target, filepath.Clean(destDir)+string(filepath.Separator)) {
			continue // skip paths that escape destDir
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, f.Mode())
		if err != nil {
			rc.Close()
			return err
		}
		if _, err := io.Copy(out, rc); err != nil {
			out.Close()
			rc.Close()
			return err
		}
		out.Close()
		rc.Close()
	}
	return nil
}

func expandPath(p string) string {
	if strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			p = filepath.Join(home, p[2:])
		}
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return abs
}

// asprofRe matches profiling command lines like `/path/to/asprof -d 30 ...`
var asprofRe = regexp.MustCompile("^`(.+?)\\s+-d\\s+30")

// extractAsprofFromSkill parses a rendered SKILL.md and extracts the asprof
// binary path from profiling command lines (e.g. "`/usr/bin/asprof -d 30 ...`").
func extractAsprofFromSkill(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		// Match lines like "- CPU profiling: `/path/to/asprof -d 30 ..."
		idx := strings.Index(line, "`")
		if idx < 0 {
			continue
		}
		sub := line[idx:]
		if m := asprofRe.FindStringSubmatch(sub); m != nil {
			return m[1]
		}
	}
	return ""
}

// updateInstalledSkills scans global skill directories for existing SKILL.md
// files and re-runs init on the new binary to regenerate them.
func updateInstalledSkills(execPath string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}

	agents := []struct {
		name string
		flag string
	}{
		{"claude", "--claude"},
		{"codex", "--codex"},
	}

	for _, agent := range agents {
		dir := skillDir(agent.name, home, false)
		skillPath := filepath.Join(dir, "SKILL.md")

		data, err := os.ReadFile(skillPath)
		if err != nil {
			continue // not installed for this agent
		}
		content := string(data)

		// Verify this is our skill by checking frontmatter
		if !strings.Contains(content, "name: jfr") {
			continue
		}

		// Try extracting asprof path from existing rendered content;
		// fall back to auto-detection if missing or stale.
		asprofPath := extractAsprofFromSkill(content)
		if asprofPath != "" {
			if _, err := os.Stat(asprofPath); err != nil {
				asprofPath = ""
			}
		}
		if asprofPath == "" {
			asprofPath = findAsprof()
		}
		if asprofPath == "" {
			fmt.Fprintf(os.Stderr, "warning: cannot determine asprof path for %s skill, skipping\n", agent.name)
			continue
		}

		cmd := exec.Command(execPath, "init", "--force", agent.flag, "--asprof", asprofPath)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to update %s skill: %v\n", agent.name, err)
		}
	}
}
