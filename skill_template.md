---
name: jfr
description: >
  JVM profiling: JFR, async-profiler, hot methods, flame graph, cpu/wall/alloc/lock,
  performance regression, collapsed stacks.
allowed-tools: Bash, Read, Grep, Glob
---

# JFR Performance Analysis

Analyze async-profiler JFR recordings with `{{AP_QUERY_PATH}}` (a Go binary, NOT a Python package —
do not pip install it).
Run `{{AP_QUERY_PATH}} --help` for full command and flag reference.

Always prefer JFR format over collapsed text. JFR preserves event types (cpu/wall/alloc/lock),
line numbers, and thread info — collapsed text loses event separation and may lack line data.
If the user has collapsed text, `{{AP_QUERY_PATH}}` accepts it, but suggest re-profiling with
`{{ASPROF_PATH}} -o jfr` if they need deeper analysis.

## Profiling

Use `{{ASPROF_PATH}}` to record profiles. Common invocations:

- CPU profiling: `{{ASPROF_PATH}} -d 30 -o jfr -f profile.jfr <pid>`
- Wall-clock:    `{{ASPROF_PATH}} -d 30 -e wall -o jfr -f profile.jfr <pid>`
- Allocations:   `{{ASPROF_PATH}} -d 30 -e alloc -o jfr -f profile.jfr <pid>`
- Lock contention: `{{ASPROF_PATH}} -d 30 -e lock -o jfr -f profile.jfr <pid>`

## Workflow

1. **Triage**: `{{AP_QUERY_PATH}} info profile.jfr` — events, top threads, top 20 hot methods.
2. **Drill down**: `{{AP_QUERY_PATH}} tree profile.jfr -m HashMap.resize --depth 6 --min-pct 0.5`
   Use `--hide REGEX` with tree, trace, or callers to remove framework/wrapper frames before analysis
   (e.g. `--hide "Thread\.(run|start)"` strips thread boilerplate).
3. **Trace**: `{{AP_QUERY_PATH}} trace profile.jfr -m HashMap.resize` — hottest path from method to leaf.
4. **Callers**: `{{AP_QUERY_PATH}} callers profile.jfr -m HashMap.resize`
5. **Lines**: `{{AP_QUERY_PATH}} lines profile.jfr -m HashMap.resize`
6. **Thread focus**: `{{AP_QUERY_PATH}} hot profile.jfr -t "http-nio" --top 20`
7. **Compare**: `{{AP_QUERY_PATH}} diff before.jfr after.jfr --min-delta 0.5` — REGRESSION/IMPROVEMENT/NEW/GONE.
8. **CI gate**: `{{AP_QUERY_PATH}} hot profile.jfr --assert-below 15.0` — exits 1 if top method >= threshold.
9. **Export**: `{{AP_QUERY_PATH}} collapse profile.jfr` — emit collapsed-stack text for external tools.
10. **Filter**: `{{AP_QUERY_PATH}} filter profile.jfr -m HashMap.resize` — output only stacks passing through a method.

## Event types (`--event`)

- **cpu** (default) — on-CPU samples only. Shows where the JVM is burning cycles.
- **wall** — wall-clock samples: includes threads blocked on I/O, locks, sleeps. Use when
  latency matters more than CPU usage (e.g. slow HTTP requests where threads wait on DB).
- **alloc** / **lock** — allocation and lock-contention hotspots.

When unsure, start with `cpu`. Switch to `wall` if the profile shows low CPU but high latency.

## Threads

By default all threads are aggregated. Use `-t THREAD` (substring match) to isolate one
thread or thread group — essential when different threads have different workloads
(e.g. `-t "http-nio"` vs `-t "kafka-consumer"`). The `threads` command shows the sample
distribution across threads to help pick the right filter.

## Output options

Use `--fqn` to show fully-qualified class names (e.g. `java.util.HashMap.resize` instead of
`HashMap.resize`). Available on hot, trace, lines, and diff.

## Interpretation

- **Self% ≈ Total%** → leaf method, bottleneck is the method itself.
- **Total% >> Self%** → entry point, drill into `tree` to find real cost.
- Always start with `info`. Quote specific numbers. Mention thread if `-t` was used.
