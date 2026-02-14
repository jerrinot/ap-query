# Plan: Implement `trace` command

## Overview

Add a new `trace` command that follows the single hottest child at each level from a matched method all the way down to the leaf. Unlike `tree` (which is depth-limited and shows all children), `trace` shows only the hottest path — one frame per level — with no depth limit. At branching points where siblings are skipped, the output annotates how many siblings exist and what the next-hottest one is, so the user knows they're at a decision point.

## Behavioral Specification

```
ap-query trace profile.jfr -m "IndexUpdateRunner" --event wall --min-pct 0.5
```

Output:
```
[25.7%] IndexUpdateRunner.indexOneFileHandleExceptions
  [25.2%] FileBasedIndexImpl.removeDataFromIndicesForFile  (+2 siblings, next: 0.3% MapManager.update)
    [24.7%] FileBasedIndexImpl.removeFileDataFromIndices
      [22.1%] IndexImpl.processUpdate  (+1 sibling, next: 2.5% IndexImpl.validate)
        ...
                    [2.4%] LockedCacheWrapper.read  ← self=1.4%
```

### Event selection (`--event` / `-e`)

The `trace` command supports the global `--event` / `-e` flag (cpu, wall, alloc, lock) — same as all other commands. This is already handled in `main.go`'s global flag parsing before the command switch: `openInput(path, eventType)` filters JFR events by type. The `trace` case just needs to be wired into the existing flow (no extra work in `trace.go` itself). The usage text and examples should show `-e` with trace.

### Branching annotation format

When the chosen child has siblings (other children of the same parent above min-pct), the line is annotated:

- **1 sibling**: `(+1 sibling, next: 3.2% Other.method)`
- **N siblings**: `(+N siblings, next: 3.2% Other.method)` — "next" is the second-hottest child.
- **No siblings** (single child): no annotation — clean line.
- **All siblings below min-pct**: no annotation — they were filtered out.

This tells the user: "I chose this path, but there were other options; here's the strongest alternative."

### Key behaviors:
1. **`-m` is required** — unlike `tree`, which can show all roots, `trace` needs a starting method.
2. **`--event` / `-e`** — select event type (cpu, wall, alloc, lock). Defaults to cpu. Handled by the global flag flow in `main.go` — the `stackFile` passed to `cmdTrace` is already filtered to the selected event.
3. **Follows the hottest child at each level** — at each node, pick the child with the highest sample count.
4. **Branching visualization** — when siblings exist above min-pct, annotate the chosen line with sibling count and next-hottest alternative.
5. **No depth limit** — traverse all the way to the leaf.
6. **`--min-pct`** — stop if the hottest child falls below this threshold (default: 0.5). Also used to filter siblings for the annotation (siblings below min-pct are not counted).
7. **Self-time annotation** — annotate the leaf (last node) with `← self=X.X%` when it has self-time samples >= min-pct.
8. **Multiple matched methods** — if `-m` matches multiple method names, print a header like `# matched N methods: ...` (same as `tree`), then show the trace for each root, sorted by sample count descending.
9. **`--fqn`** — show fully-qualified names if present.

## Implementation Steps

### Step 1: Create `trace.go`

New file with `cmdTrace(sf *stackFile, method string, minPct float64, fqn bool)`.

The function will:
1. Call `aggregatePaths()` (same as `tree`) to build a `pathTree` from the matched method downward.
2. Walk the `pathTree` greedily: at each node, collect all children, sort by sample count descending. Pick the top child.
3. Print one line per level with increasing indent. If there are siblings above min-pct, append the annotation `(+N siblings, next: X.X% Name)`.
4. Annotate the leaf with self-time if non-zero and >= min-pct.
5. Stop when no children exist or the hottest child's percentage falls below `minPct`.

Note: `cmdTrace` receives a `*stackFile` that is already filtered by event type and thread (handled in `main.go`). The function itself does not need to know about event types.

This reuses the existing `pathTree` infrastructure (no new data structures needed).

### Step 2: Wire into `main.go`

1. Add `case "trace":` to the switch in `main()`.
2. Require `-m` flag (like `callers`).
3. Parse `--min-pct` (default 0.5) and `--fqn`.
4. Call `cmdTrace(sf, method, minPct, fqn)`.
5. Update `usage()`:
   - Add `trace` to the command list with description.
   - Add example: `ap-query trace profile.jfr -m HashMap.resize --event cpu --min-pct 0.5`

The global `--event` / `-e` flag is already parsed and used to build the `stackFile` before the switch — no extra flag handling needed for trace.

### Step 3: Add comprehensive tests to `main_test.go`

#### Unit tests (using `makeStackFile`):

1. **TestCmdTraceBasicHotPath** — Simple linear chain A→B→C (no branching), verify it follows all the way with no sibling annotations.
2. **TestCmdTraceBranching** — A→B(70) and A→C(30), verify it picks B and annotates `(+1 sibling, next: 30.0% C.c)`.
3. **TestCmdTraceBranchingMultipleSiblings** — A→B(50), A→C(30), A→D(20), verify `(+2 siblings, next: 30.0% C.c)`.
4. **TestCmdTraceDeepChain** — 10+ frames deep, verify no depth limit.
5. **TestCmdTraceSelfTimeAnnotation** — Verify leaf gets `← self=X.X%`.
6. **TestCmdTraceMinPct** — Verify it stops when child drops below threshold.
7. **TestCmdTraceNoMatch** — Method not found → "no frames matching".
8. **TestCmdTraceEmpty** — Empty stackFile → no output.
9. **TestCmdTraceMultipleMatches** — Multiple methods match → header + both traced.
10. **TestCmdTraceSingleFrameMethod** — Method is the leaf (no children) → single line, self-time annotation.
11. **TestCmdTraceEqualChildren** — Two children with identical sample counts → deterministic pick (lexicographic tiebreak on name).
12. **TestCmdTraceMinPctZero** — min-pct=0 should trace all the way down.
13. **TestCmdTraceMinPctHighCutsEarly** — High min-pct cuts off early in the trace.
14. **TestCmdTraceFQN** — Verify `--fqn` shows fully-qualified names.
15. **TestCmdTracePercentageCorrectness** — Verify percentages are relative to total samples, not parent.
16. **TestCmdTraceSelfTimeOnlyOnLeaf** — Self-time annotation only appears on the final node.
17. **TestCmdTraceSelfTimeBelowMinPctHidden** — Self-time annotation hidden when self-pct < min-pct.
18. **TestCmdTraceNoSiblingAnnotationWhenSingle** — No annotation when there's only one child (straight path).
19. **TestCmdTraceSiblingsBelowMinPctNotCounted** — Siblings below min-pct are excluded from the sibling count and annotation.
20. **TestCmdTraceMatchAtLeaf** — Matched method is already the leaf of all stacks → single line output.
21. **TestCmdTraceMultipleRootsWithDifferentDepths** — Matched method appears in stacks of varying depth.
22. **TestCmdTraceBranchingSiblingAnnotationFormat** — Verify exact format of the sibling annotation string.

#### JFR integration tests (event selection tested via different JFR fixtures):

23. **TestJFRTraceCommand** — Run trace against `cpu.jfr` with a known method, verify output structure.
24. **TestJFRTraceWithThreadFilter** — Filter to specific thread, then trace.
25. **TestJFRTraceWallEvent** — Run trace against `wall.jfr` (event=wall), verify it picks up wall-clock samples, not cpu.
26. **TestJFRTraceMultiEventFile** — Run trace against `multi.jfr` with different `--event` values, verify different results for cpu vs wall vs alloc.

## Files Modified

- **NEW**: `trace.go` — `cmdTrace` function
- **MODIFIED**: `main.go` — add `trace` case + update usage
- **MODIFIED**: `main_test.go` — add all tests listed above
