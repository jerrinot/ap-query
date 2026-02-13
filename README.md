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

`ap-query init` installs a **skill** — a markdown file that teaches AI coding
agents (Claude Code, Codex) how to autonomously profile JVMs and analyze JFR
recordings: triage hot methods, drill into call trees, compare before/after,
and interpret the results.

The `init` command auto-detects `asprof` and `ap-query` paths and embeds them
into the skill file — no manual path configuration needed. It also auto-detects
which agents are installed (`~/.claude`, `~/.agents`) and writes to all of them.

### Install

```bash
ap-query init              # global — auto-detects installed agents
ap-query init --project    # project-local — auto-detects installed agents
```

### Options

| Flag | Description |
|------|-------------|
| `--asprof PATH` | Explicit path to `asprof` (skips auto-detection and interactive prompt) |
| `--project` | Install into the current directory instead of home |
| `--force` | Overwrite existing skill files |
| `--claude` | Install only for Claude Code (`.claude/skills/jfr/`) |
| `--codex` | Install only for Codex (`.agents/skills/jfr/`) |
| `--stdout` | Dump rendered skill to stdout (for piping into custom agents) |

### How it works

When you ask your agent about JVM performance (e.g. "profile this JFR file",
"why is this endpoint slow"), the skill's keyword triggers activate it
automatically. The skill teaches the agent the `ap-query` workflow and
interpretation heuristics — you don't need to explain the tool or its flags.

## Release

Tag a version to trigger a release build:

```bash
git tag v0.1.0 && git push --tags
```

This builds binaries for linux/darwin x amd64/arm64 via GoReleaser and publishes
them as a GitHub release.
