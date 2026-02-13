---
name: profile-jfr
description: >
  Use when the user wants to analyze Java/JVM performance using async-profiler JFR files
  or collapsed stack traces. Covers profiling analysis, hot method identification,
  flame graph interpretation, performance regression detection, and optimization guidance.
  Trigger phrases: "profile", "JFR", "flame graph", "hot methods", "performance",
  "async-profiler", "cpu profile", "wall clock", "allocation profile", "lock contention".
allowed-tools: Bash, Read, Grep, Glob
---

# JFR Performance Analysis

Analyze async-profiler JFR recordings with `ap-query`.
Run `ap-query --help` for full command and flag reference.
If not in PATH, ask the user for its location.

Always prefer JFR format over collapsed text. JFR preserves event types (cpu/wall/alloc/lock),
line numbers, and thread info — collapsed text loses event separation and may lack line data.
If the user has collapsed text, `ap-query` accepts it, but suggest re-profiling with
`-o jfr` if they need deeper analysis.

## Workflow

1. **Triage**: `ap-query info profile.jfr` — events, top threads, top 10 hot methods.
2. **Drill down**: `ap-query tree profile.jfr -m HashMap.resize --depth 6 --min-pct 0.5`
3. **Callers**: `ap-query callers profile.jfr -m HashMap.resize`
4. **Lines**: `ap-query lines profile.jfr -m HashMap.resize`
5. **Thread focus**: `ap-query hot profile.jfr -t "http-nio" --top 20`
6. **Compare**: `ap-query diff before.jfr after.jfr --min-delta 0.5` — REGRESSION/IMPROVEMENT/NEW/GONE.
7. **CI gate**: `ap-query hot profile.jfr --assert-below 15.0` — exits 1 if top method >= threshold.

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

## Interpretation

- **Self% ≈ Total%** → leaf method, bottleneck is the method itself.
- **Total% >> Self%** → entry point, drill into `tree` to find real cost.
- Always start with `info`. Quote specific numbers. Mention thread if `-t` was used.
