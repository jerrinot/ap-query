# ap-query

Agent-friendly CLI for analyzing async-profiler JFR recordings.

Designed for AI coding agents (Claude Code, etc.) to autonomously investigate JVM
performance — triage with `info`, drill down with `tree`/`callers`/`lines`,
compare with `diff`, and gate CI with `--assert-below`. All output is plain text,
parseable without extra tooling.

Also accepts collapsed-stack text files and stdin, but prefer JFR — it preserves
event types, line numbers, and thread info.

## Build

```bash
go build -o ap-query .
```

## Usage

```bash
ap-query <command> [flags] <file>
ap-query --help   # full reference
```

## Agent integration

Drop the [`jfr`](.claude/skills/jfr/SKILL.md) skill into your
project's `.claude/skills/` to give Claude Code profiling capabilities. The skill
teaches the agent the analysis workflow and interpretation heuristics.
