# ap-query

CLI for analyzing [async-profiler](https://github.com/async-profiler/async-profiler) JFR recordings.

Built for coding agents (Claude Code, Codex, etc.), not for human-first
interactive profiling workflows. Born out of frustration from watching agents
struggle with raw profiling data - burning tokens, polluting context, and
still getting lost. 

## Quick Start

### 1) Install `ap-query`

```bash
# one-liner (latest release)
curl -fsSL https://raw.githubusercontent.com/jerrinot/ap-query/master/install.sh | sh

# or with Go
go install github.com/jerrinot/ap-query@latest
```

### 2) Install the agent skill

```bash
# global skill installation for all projects
ap-query init
# or just for the current project directory
ap-query init --project

```

### 3) Ask your agent to run the workflow

```text
Profile this program with async-profiler and analyze the result with ap-query.
Start with hotspot triage, then drill down and report concrete next actions.
```

## Detailed Guide

### Agent Integration

`ap-query init` writes a `SKILL.md` file for your agent so it knows how to
profile/analyze JVM recordings with `ap-query`.

The command auto-detects `asprof` and `ap-query` paths and embeds them into the
skill file. If `asprof` is missing, it can download async-profiler to
`~/.ap-query/`.

Some agents may auto-activate the skill based on prompt context. If not, ask
explicitly to use `ap-query`.

### Install Modes

- Project-local: `ap-query init --project --codex` or `ap-query init --project --claude`
- Global: `ap-query init --codex` or `ap-query init --claude`

### Input Types

- `.jfr` / `.jfr.gz`: parsed as JFR binary (supports `--event cpu|wall|alloc|lock`)
- other files: parsed as collapsed-stack text (`frames;... count`)
- `-`: read collapsed text from stdin

### `init` Options

| Flag | Description |
|------|-------------|
| `--asprof PATH` | Explicit path to `asprof` (skips auto-detection and interactive prompt) |
| `--project` | Install into the current directory instead of home |
| `--force` | Overwrite existing skill files |
| `--claude` | Install only for Claude Code (`.claude/skills/jfr/`) |
| `--codex` | Install only for Codex (`.agents/skills/jfr/`) |
| `--stdout` | Dump rendered skill to stdout (for piping into custom agents) |

### Full Reference

```bash
ap-query --help
```
