#!/bin/bash

set -e

# Change to the directory where this script is located
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Get the repository root (4 levels up from test/performance/benchmarks/func-deployment/)
REPO_ROOT="$(cd "$SCRIPT_DIR/../../../.." && pwd)"

DEPLOYMENT_YAML="$REPO_ROOT/config/core/configmaps/deployment.yaml"
REPEAT=${REPEAT:-50}

# Set USE_AKS=true when running on AKS (skips local QCNL volume mount)
USE_AKS=${USE_AKS:-false}

# Create a single timestamp for this benchmark run
BENCHMARK_TIMESTAMP=$(date +%F_%T)
export BENCHMARK_TIMESTAMP
echo "Benchmark run timestamp: $BENCHMARK_TIMESTAMP"

# Queue sidecar images for each variant type
# Use AKS-specific images (no QCNL volume mount) when USE_AKS=true
if [[ "$USE_AKS" == "true" ]]; then
    QUEUE_IMAGE_EGO="docker.io/atosh502/queue-proxy-ego:bench-aks"
    QUEUE_IMAGE_EGO_PRE="docker.io/atosh502/queue-proxy-ego-pre:bench-aks"
else
    QUEUE_IMAGE_EGO="docker.io/atosh502/queue-proxy-ego:bench"
    QUEUE_IMAGE_EGO_PRE="docker.io/atosh502/queue-proxy-ego-pre:bench"
fi

# Save the original queue-sidecar-image value
get_queue_sidecar_image() {
    grep -E "^  queue-sidecar-image:" "$DEPLOYMENT_YAML" | sed 's/.*: //'
}

set_queue_sidecar_image() {
    local new_image="$1"
    sed -i "s|^  queue-sidecar-image:.*|  queue-sidecar-image: $new_image|" "$DEPLOYMENT_YAML"
    echo "Set queue-sidecar-image to: $new_image"
}

ORIGINAL_QUEUE_IMAGE=$(get_queue_sidecar_image)
echo "Original queue-sidecar-image: $ORIGINAL_QUEUE_IMAGE"

# Cleanup function to restore original image on exit
cleanup() {
    echo "Restoring original queue-sidecar-image..."
    set_queue_sidecar_image "$ORIGINAL_QUEUE_IMAGE"
}
trap cleanup EXIT

# --- Run benchmarks for each variant ---
run_variant() {
    local variant="$1"
    local queue_image="$2"

    echo ""
    echo "=========================================="
    echo "Running benchmark for variant: $variant"
    echo "=========================================="

    # Update queue-sidecar-image if specified
    if [[ -n "$queue_image" ]]; then
        current_image=$(get_queue_sidecar_image)
        if [[ "$current_image" != "$queue_image" ]]; then
            set_queue_sidecar_image "$queue_image"
        else
            echo "queue-sidecar-image already set to $queue_image"
        fi
        echo "Running dev/setup.sh to apply queue-sidecar-image change..."
        "$REPO_ROOT/dev/setup.sh"
    fi

    # Run the benchmark
    cd "$SCRIPT_DIR"
    VARIANT="$variant" REPEAT="$REPEAT" USE_AKS="$USE_AKS" ./run-func.sh

    echo "Completed benchmark for variant: $variant"
}

# Run each variant
# knative: no queue-sidecar-image change needed (uses default/og image)
# run_variant "knative" ""

# # efunction: uses queue-proxy-ego
# run_variant "efunction" "$QUEUE_IMAGE_EGO"

# # leader-efunction: uses queue-proxy-ego-pre
run_variant "leader-efunction" "$QUEUE_IMAGE_EGO_PRE"

# # # member-efunction: uses queue-proxy-ego-pre (same as leader)
# run_variant "member-efunction" "$QUEUE_IMAGE_EGO_PRE"

echo ""
echo "=========================================="
echo "All benchmarks complete!"
echo "Results saved to: $SCRIPT_DIR/run/"
echo "=========================================="
