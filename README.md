# ap-query

CLI for analyzing async-profiler profiles from:
- JFR files (`.jfr`, `.jfr.gz`)
- Collapsed stack text (files, `.gz`, or stdin `-`)

## What it does

`ap-query` provides quick profiling queries:
- `info`: one-shot summary (events, top threads, top methods)
- `hot`: hottest methods by self time
- `tree`: call tree below a method
- `callers`: caller tree above a method
- `lines`: source-line breakdown for a method
- `threads`: thread sample distribution
- `diff`: compare two profiles
- `collapse`: emit collapsed stack format
- `filter`: keep stacks that pass through a method
- `events`: list available event types in a JFR file

## Build

```bash
go build -o ap-query .
```

## Usage

```bash
ap-query <command> [flags] <file>
```

Common flags:
- `--event`, `-e`: `cpu` (default), `wall`, `alloc`, `lock` (JFR input)
- `-t`: thread substring filter
- `-m`, `--method`: method substring (required by `tree`, `callers`, `lines`, `filter`)
- `--top`: row limit (default `10` for `hot`)
- `--depth`: max depth for `tree`/`callers` (default `4`)
- `--min-pct`: hide small nodes in `tree`/`callers` (default `1.0`)
- `--min-delta`: hide small diffs (default `0.5`)
- `--fqn`: show fully-qualified names
- `--assert-below`: exit non-zero if top self% is above threshold
- `--include-callers`: include caller frames in `filter` output

## Examples

```bash
ap-query info profile.jfr
ap-query hot profile.jfr --event cpu --top 20
ap-query tree profile.jfr -m HashMap.resize --depth 6
ap-query callers profile.jfr -m HashMap.resize
ap-query lines profile.jfr -m HashMap.resize
ap-query diff before.jfr after.jfr --min-delta 0.5
ap-query collapse profile.jfr --event wall | ap-query hot -
echo "A;B;C 10" | ap-query hot -
```

## Notes

- JFR event selection applies only to JFR input.
- `-` means collapsed-stack input from stdin.
- Claude skill: [`profile-jfr`](.claude/skills/profile-jfr/SKILL.md).
