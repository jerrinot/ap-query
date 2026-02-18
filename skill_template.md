---
name: jfr
description: >
  JVM profiling: JFR, async-profiler, hot methods, flame graph, cpu/wall/alloc/lock,
  performance regression, collapsed stacks.
allowed-tools: Bash, Read, Grep, Glob
---

# JFR Performance Analysis

Analyze async-profiler JFR recordings with `{{AP_QUERY_PATH}}`.
Run `{{AP_QUERY_PATH}} --help` for full command and flag reference.
Run `{{AP_QUERY_PATH}} <command> --help` for command-specific help.

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

1. **Triage**: `{{AP_QUERY_PATH}} info profile.jfr` — events, CPU vs WALL thread-group comparison (when both exist), top threads, top 20 hot methods.
2. **Drill down**: `{{AP_QUERY_PATH}} tree profile.jfr -m HashMap.resize --depth 6 --min-pct 0.5`
   Use `--hide REGEX` with tree, trace, or callers to remove framework/wrapper frames before analysis
   (e.g. `--hide "Thread\.(run|start)"` strips thread boilerplate).
3. **Trace**: `{{AP_QUERY_PATH}} trace profile.jfr -m HashMap.resize` — hottest path from method to leaf.
4. **Callers**: `{{AP_QUERY_PATH}} callers profile.jfr -m HashMap.resize`
5. **Lines**: `{{AP_QUERY_PATH}} lines profile.jfr -m HashMap.resize`
6. **Thread focus**: `{{AP_QUERY_PATH}} hot profile.jfr -t "http-nio" --top 20`
7. **Compare**: `{{AP_QUERY_PATH}} diff before.jfr after.jfr --min-delta 0.5` — REGRESSION/IMPROVEMENT/NEW/GONE.
8. **Timeline**: `{{AP_QUERY_PATH}} timeline profile.jfr` — sample distribution over time.
   Use `--from 12s --to 14s` with any command to zoom into a time window.
   Use `--top 5` to show only the highest-sample buckets; `-m METHOD --pct` for relative percentages.
9. **CI gate**: `{{AP_QUERY_PATH}} hot profile.jfr --assert-below 15.0` — exits 1 if top method >= threshold.
10. **Export**: `{{AP_QUERY_PATH}} collapse profile.jfr` — emit collapsed-stack text for external tools.
11. **Filter**: `{{AP_QUERY_PATH}} filter profile.jfr -m HashMap.resize` — output only stacks passing through a method.
12. **Script**: `{{AP_QUERY_PATH}} script -c 'CODE'` or `{{AP_QUERY_PATH}} script file.star` — Starlark scripting for custom analysis.

## Event types (`--event`)

- **cpu** (default) — on-CPU samples only. Shows where the JVM is burning cycles.
- **wall** — wall-clock samples: includes threads blocked on I/O, locks, sleeps. Use when
  latency matters more than CPU usage (e.g. slow HTTP requests where threads wait on DB).
- **alloc** / **lock** — allocation and lock-contention hotspots.

When unsure, start with `cpu`. Switch to `wall` if the profile shows low CPU but high latency.
Use `--no-idle` with wall to strip idle leaf frames (futex, sleep, park, epoll_wait) and see only active work.

## Time-range filtering (`--from`/`--to`)

Use `--from DURATION` and `--to DURATION` (relative to recording start) to restrict any command
to a time window. JFR only. Values use Go duration syntax: `500ms`, `2s`, `1m30s`.

- `{{AP_QUERY_PATH}} hot profile.jfr --from 12s --to 14s` — hot methods in a 2-second window.
- `{{AP_QUERY_PATH}} timeline profile.jfr --from 5s --to 15s` — timeline of a 10-second slice.

Combine with `timeline` to first identify spikes, then zoom in with `--from`/`--to` on any command.

## Timeline flags

- `--buckets N` — number of time buckets (default: auto ~20).
- `--resolution DURATION` — fixed bucket width (e.g. `1s`, `500ms`). Overrides `--buckets`.
- `-m METHOD` / `--method METHOD` — only count samples where the stack contains METHOD.
- `--no-top-method` — omit per-bucket hot method (by self time) annotation (shown by default).
- `--top N` — show only the N highest-sample buckets (in time order).
- `--pct` — show method's percentage of each bucket's total (requires `--method`).
- Time labels automatically increase precision for sub-second buckets (for example, `4m44.000s-4m44.001s` at `--resolution 1ms`).

## Threads

By default all threads are aggregated. Use `-t THREAD` (substring match) to isolate one
thread or thread group — essential when different threads have different workloads
(e.g. `-t "http-nio"` vs `-t "kafka-consumer"`). The `threads` command shows the sample
distribution across threads to help pick the right filter.
Use `--group` with `threads` to aggregate by normalized name
(e.g. all `pool-1-thread-N` merge into `pool-thread`).

## Output options

Use `--fqn` to show fully-qualified class names (e.g. `java.util.HashMap.resize` instead of
`HashMap.resize`). Available on hot, trace, lines, and diff.

## No-match feedback

When `-m` matches nothing, commands print `no stacks matching '<method>'` with:
- **Fuzzy suggestions** — similar method names from the profile (case-insensitive, up to 5).
- **`$`-expansion hints** — warns about shell variable expansion eating `$` in inner-class names.

If the profile is empty or all samples were removed by filters (`-t`, `--no-idle`, `--from`/`--to`),
commands print `no samples (empty profile or all filtered out)` instead.

## Interpretation

- **Self% ≈ Total%** → leaf method, bottleneck is the method itself.
- **Total% >> Self%** → entry point, drill into `tree` to find real cost.
- Always start with `info`. Quote specific numbers. Mention thread if `-t` was used.

## Starlark scripting (`script`)

For analysis that fixed commands cannot express: cross-event correlation, custom grouping,
compound predicates, multi-file comparison, CI budget enforcement.

Run `{{AP_QUERY_PATH}} script --help` for the full scripting API reference (types, functions, examples).

Key scripting APIs: `Profile.no_idle()` filters idle leaf frames (scripting equivalent of `--no-idle`);
`Bucket.label` returns the formatted time range string for timeline buckets;
`Stack.thread_has(pattern)` substring-matches on thread name; `round(x, decimals=0)` rounds floats;
`pad(value, width)` pads to width (positive=right-align, negative=left-align);
`Profile.summary()` returns a one-line summary string;
`Profile.start`/`.end` return scope boundaries in seconds (0/duration for root profiles, matching split/bucket boundaries for scoped profiles);
`Profile.split()` accepts duration strings (`"5s"`, `"1m30s"`) alongside float seconds;
`diff(a, b, min_delta=0.5, top=0, fqn=False)` compares two Profiles — `top` limits entries per category, `fqn` uses fully-qualified names (changes aggregation granularity).

Windowing patterns (compare time windows within a single recording):
- `timeline()` → `bucket.profile` → `diff()`: regular-interval comparison.
- `split([boundary])` → `diff(parts[0], parts[1])`: ad-hoc boundary comparison.
