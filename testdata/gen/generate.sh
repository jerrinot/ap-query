#!/usr/bin/env bash
#
# Generate JFR test fixtures using async-profiler.
# Prerequisites: java (17+), async-profiler with libasyncProfiler.so
#
# Usage: ./generate.sh [/path/to/libasyncProfiler.so]
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
TESTDATA_DIR="$(dirname "$SCRIPT_DIR")"
WORKLOAD="$SCRIPT_DIR/Workload.java"

# Find libasyncProfiler.so
if [[ $# -ge 1 ]]; then
    AP_LIB="$1"
elif [[ -n "${ASPROF_LIB:-}" ]]; then
    AP_LIB="$ASPROF_LIB"
else
    # Try common locations
    for candidate in \
        "$HOME/devel/tools/async-profiler-4.3-linux-x64/lib/libasyncProfiler.so" \
        "$HOME/.ap-query/lib/libasyncProfiler.so" \
        "/usr/local/lib/libasyncProfiler.so" \
        "/opt/async-profiler/lib/libasyncProfiler.so"; do
        if [[ -f "$candidate" ]]; then
            AP_LIB="$candidate"
            break
        fi
    done
fi

if [[ -z "${AP_LIB:-}" || ! -f "$AP_LIB" ]]; then
    echo "ERROR: libasyncProfiler.so not found."
    echo "Usage: $0 /path/to/libasyncProfiler.so"
    echo "Or set ASPROF_LIB environment variable."
    exit 1
fi

echo "Using async-profiler: $AP_LIB"
echo "Output directory: $TESTDATA_DIR"

# Compile workload
echo "Compiling Workload.java..."
javac -d "$SCRIPT_DIR" "$WORKLOAD"

# Helper: profile with given agent options and output file
profile() {
    local outfile="$1"
    local agent_opts="$2"
    local basename
    basename="$(basename "$outfile")"

    echo "Generating $basename ($agent_opts)..."
    java -agentpath:"$AP_LIB"="$agent_opts" \
         -cp "$SCRIPT_DIR" Workload

    if [[ ! -f "$outfile" ]]; then
        echo "ERROR: $outfile was not created"
        exit 1
    fi

    local size
    size=$(stat --printf='%s' "$outfile" 2>/dev/null || stat -f '%z' "$outfile")
    echo "  → $basename: ${size} bytes"
}

# Generate single-event JFR files
profile "$TESTDATA_DIR/cpu.jfr"   "start,event=cpu,file=$TESTDATA_DIR/cpu.jfr"
profile "$TESTDATA_DIR/wall.jfr"  "start,event=wall,file=$TESTDATA_DIR/wall.jfr"
profile "$TESTDATA_DIR/alloc.jfr" "start,event=alloc,file=$TESTDATA_DIR/alloc.jfr"
profile "$TESTDATA_DIR/lock.jfr"  "start,event=lock,file=$TESTDATA_DIR/lock.jfr"

# Hardware counter event (maps to ExecutionSample/cpu in JFR)
profile "$TESTDATA_DIR/branch-misses.jfr" "start,event=branch-misses,file=$TESTDATA_DIR/branch-misses.jfr"

# Multi-event: cpu + alloc + lock + wall
profile "$TESTDATA_DIR/multi.jfr" "start,cpu,alloc,lock,wall,file=$TESTDATA_DIR/multi.jfr"

# Gzip files larger than 500KB
echo ""
echo "Checking file sizes..."
for f in "$TESTDATA_DIR"/*.jfr; do
    size=$(stat --printf='%s' "$f" 2>/dev/null || stat -f '%z' "$f")
    if (( size > 500000 )); then
        echo "  Compressing $(basename "$f") (${size} bytes)..."
        gzip -f "$f"
        gzsize=$(stat --printf='%s' "$f.gz" 2>/dev/null || stat -f '%z' "$f.gz")
        echo "  → $(basename "$f").gz: ${gzsize} bytes"
    fi
done

# Verify each fixture
echo ""
echo "Verifying fixtures..."
AP_QUERY="${AP_QUERY:-go run $SCRIPT_DIR/../..}"
for f in "$TESTDATA_DIR"/cpu.jfr* "$TESTDATA_DIR"/wall.jfr* "$TESTDATA_DIR"/alloc.jfr* \
         "$TESTDATA_DIR"/lock.jfr* "$TESTDATA_DIR"/branch-misses.jfr* "$TESTDATA_DIR"/multi.jfr*; do
    if [[ -f "$f" ]]; then
        echo "  $(basename "$f"):"
        $AP_QUERY events "$f" 2>&1 | sed 's/^/    /'
    fi
done

echo ""
echo "Done! Fixtures written to $TESTDATA_DIR/"
ls -lh "$TESTDATA_DIR"/*.jfr* 2>/dev/null
