#!/bin/bash

set -e

# Change to the directory where this script is located
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Get the repository root (4 levels up from test/performance/benchmarks/func-deployment/)
REPO_ROOT="$(cd "$SCRIPT_DIR/../../../.." && pwd)"

# ConfigMap containing queue-sidecar-image config
CONFIGMAP_NAME="config-deployment"
CONFIGMAP_NAMESPACE="knative-serving"

REPEAT=${REPEAT:-25}

# Set USE_AKS=true when running on AKS (skips local QCNL volume mount)
USE_AKS=${USE_AKS:-false}

# Create a single timestamp for this benchmark run
BENCHMARK_TIMESTAMP=$(date +%F_%T)
export BENCHMARK_TIMESTAMP
echo "Benchmark run timestamp: $BENCHMARK_TIMESTAMP"

# Queue sidecar images for each variant type
QUEUE_IMAGE_EGO="docker.io/atosh502/queue-proxy-ego:bench"
QUEUE_IMAGE_EGO_PRE="docker.io/atosh502/queue-proxy-ego-pre:bench"

# Functions to get/set queue-sidecar-image via kubectl
get_queue_sidecar_image() {
    kubectl get configmap "$CONFIGMAP_NAME" -n "$CONFIGMAP_NAMESPACE" \
        -o jsonpath='{.data.queue-sidecar-image}'
}

set_queue_sidecar_image() {
    local new_image="$1"
    kubectl patch configmap "$CONFIGMAP_NAME" -n "$CONFIGMAP_NAMESPACE" \
        --type merge -p "{\"data\":{\"queue-sidecar-image\":\"$new_image\"}}"
    echo "Set queue-sidecar-image to: $new_image"
}

# Save the original queue-sidecar-image value

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

        # dev/setup.sh re-applies config/core/ which resets
        # config-deployment.enable-qcnl-volume-mount to "true". On AKS the
        # user-container YAML doesn't declare the sgx-default-qcnl-local volume,
        # so the queue-proxy mount injection produces an invalid Deployment.
        if [[ "$USE_AKS" == "true" ]]; then
            kubectl patch configmap "$CONFIGMAP_NAME" -n "$CONFIGMAP_NAMESPACE" \
                --type merge -p '{"data":{"enable-qcnl-volume-mount":"false"}}'
        fi
    fi

    # Run the benchmark
    cd "$SCRIPT_DIR"
    VARIANT="$variant" REPEAT="$REPEAT" USE_AKS="$USE_AKS" ./run-func.sh

    echo "Completed benchmark for variant: $variant"
}

# All variants to run
ALL_VARIANTS=("knative" "efunction" "leader-efunction" "member-efunction")

# Get queue image for a given variant
get_queue_image_for_variant() {
    local variant=$1
    case "$variant" in
        knative)
            echo ""  # No change needed, uses default
            ;;
        efunction)
            echo "$QUEUE_IMAGE_EGO"
            ;;
        leader-efunction|member-efunction)
            echo "$QUEUE_IMAGE_EGO_PRE"
            ;;
        *)
            echo ""
            ;;
    esac
}

# Run benchmarks for each variant with 60s delay between runs
for i in "${!ALL_VARIANTS[@]}"; do
    variant="${ALL_VARIANTS[$i]}"
    queue_image=$(get_queue_image_for_variant "$variant")
    run_variant "$variant" "$queue_image"

    # Add 60s delay between variants (skip after last variant)
    if [[ $i -lt $((${#ALL_VARIANTS[@]} - 1)) ]]; then
        echo "Waiting 60s before next variant..."
        sleep 60
    fi
done

echo ""
echo "=========================================="
echo "All benchmarks complete!"
echo "Results saved to: $SCRIPT_DIR/run/"
echo "=========================================="
