#!/usr/bin/env bash
# Run the func-chain benchmark across every strategy, collect the
# resulting *_traces.data files into artifact/, and render fig8.pdf.
#
# Usage:
#   ./run-fig8.sh [RATE] [DURATION]
#
# Environment:
#   USE_AKS=true   Run against a remote AKS cluster (same semantics as
#                  run-benchmark.sh). When set, run-benchmark.sh writes
#                  artifacts under run/<ts>/aks/.
#   SKIP_BENCHMARK=true  Skip the benchmark step and just collect + plot
#                        from the latest run directory (useful when
#                        iterating on the plot).
#
# Produces:
#   artifact/<strategy>_<RATE>rps_<DURATION>_traces.data   (one per strategy)
#   artifact/fig8.pdf
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BENCH_DIR="$(cd "$HERE/.." && pwd)"

RATE="${1:-100}"
DURATION="${2:-5m}"

if ! command -v gnuplot >/dev/null 2>&1; then
    echo "error: gnuplot not found on PATH" >&2
    exit 1
fi

if [[ "${SKIP_BENCHMARK:-false}" != "true" ]]; then
    echo "==> running benchmark for all strategies (rate=${RATE}, duration=${DURATION})"
    eval $(minikube docker-env)
    "$BENCH_DIR/run-benchmark.sh" all "$RATE" "$DURATION"

    echo "==> collecting *_traces.data into $HERE"
    python3 "$HERE/extract-fig8-data.py" \
        --artifact-dir "$HERE" \
        --rate "$RATE" \
        --duration "$DURATION"
else
    echo "==> SKIP_BENCHMARK=true; skipping benchmark and trace collection, re-plotting from existing $HERE/*.data"
fi

echo "==> rendering fig8.pdf with gnuplot"
(cd "$HERE" && gnuplot -e "rate='${RATE}'; duration='${DURATION}'" fig8.gpi)

echo "done. wrote:"
echo "  $HERE/fig8.pdf"
