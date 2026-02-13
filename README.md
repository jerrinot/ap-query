# ap-query

Agent-friendly CLI for analyzing async-profiler JFR recordings.

Designed for AI coding agents (Claude Code, etc.) to autonomously investigate JVM
performance — triage with `info`, drill down with `tree`/`callers`/`lines`,
compare with `diff`, and gate CI with `--assert-below`. All output is plain text,
parseable without extra tooling.

Also accepts collapsed-stack text files and stdin, but prefer JFR — it preserves
event types, line numbers, and thread info.

## Install

```bash
# One-liner (downloads latest release)
curl -fsSL https://raw.githubusercontent.com/jerrinot/ap-query/master/install.sh | sh

# Or with Go
go install github.com/jerrinot/ap-query@latest
```

## Usage

```bash
ap-query <command> [flags] <file>
ap-query --help   # full reference
```

## Agent integration

Drop the [`jfr`](.claude/skills/jfr/SKILL.md) skill into your project's
`.claude/skills/` to give Claude Code profiling capabilities. The skill teaches
the agent the analysis workflow and interpretation heuristics.

## Release

Tag a version to trigger a release build:

```bash
git tag v0.1.0 && git push --tags
```

This builds binaries for linux/darwin x amd64/arm64 via GoReleaser and publishes
them as a GitHub release.
