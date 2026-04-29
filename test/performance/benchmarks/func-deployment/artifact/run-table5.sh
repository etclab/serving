#!/bin/bash
# Run the func-deployment benchmark for all variants and emit Table 5
# (avg time to deploy each function configuration) to ./table5.dat.

set -e

ARTIFACT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BENCH_DIR="$(cd "$ARTIFACT_DIR/.." && pwd)"

REPEAT=${REPEAT:-3}
USE_AKS=${USE_AKS:-true}

echo "Running func-deployment benchmark (REPEAT=$REPEAT, USE_AKS=$USE_AKS)..."
REPEAT="$REPEAT" USE_AKS="$USE_AKS" "$BENCH_DIR/run-benchmark.sh"

echo ""
echo "Extracting Table 5..."
python3 "$ARTIFACT_DIR/extract-table5.py" \
    --run-dir "$BENCH_DIR/run" \
    --out-file "$ARTIFACT_DIR/table5.dat"
