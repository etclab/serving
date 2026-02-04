#!/bin/bash

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Get the repository root (4 levels up from test/performance/benchmarks/func-chain/)
REPO_ROOT="$(cd "$SCRIPT_DIR/../../../.." && pwd)"

source "${SCRIPT_DIR}/../../../../eval/s/env.sh"

export KO_DOCKER_REPO='docker.io/atosh502'

# All available strategies
ALL_STRATEGIES=("knative" "efunction" "rsa-efunction" "member-efunction" "leader-efunction" "both" "both-sig" "both-hash-chain-sig")

# ConfigMap containing queue-sidecar-image config
CONFIGMAP_NAME="config-deployment"
CONFIGMAP_NAMESPACE="knative-serving"

# Set USE_AKS=true when running on AKS (skips local QCNL volume mount)
# Export so child scripts (teardown.sh, deploy-services.sh) can access it
export USE_AKS=${USE_AKS:-false}

# Queue sidecar images for each variant type
# Use AKS-specific images (no QCNL volume mount) when USE_AKS=true
QUEUE_IMAGE_EGO="docker.io/atosh502/queue-proxy-ego:bench"
QUEUE_IMAGE_EGO_PRE="docker.io/atosh502/queue-proxy-ego-pre:bench"
QUEUE_IMAGE_EGO_PRE_HASH_CHAIN="docker.io/atosh502/queue-proxy-ego-pre:latest"

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
    # set_queue_sidecar_image "$ORIGINAL_QUEUE_IMAGE"
}
trap cleanup EXIT

# Get queue image for a given strategy
get_queue_image_for_strategy() {
    local strategy=$1
    case "$strategy" in
        knative)
            echo ""  # No change needed, uses default
            ;;
        efunction)
            echo "$QUEUE_IMAGE_EGO"
            ;;
        rsa-efunction|member-efunction|leader-efunction|both|both-sig)
            echo "$QUEUE_IMAGE_EGO_PRE"
            ;;
        both-hash-chain-sig)
            echo "$QUEUE_IMAGE_EGO_PRE_HASH_CHAIN"
            ;;
        *)
            echo ""
            ;;
    esac
}

# Parse arguments
STRATEGY="${1:-all}"
RATE="${2:-10}"
DURATION="${3:-1m}"
TARGET="${4:-broker-ingress}"
FORMAT_LIMIT="${5:-300}"  # Number of records to process for statistics

timestamp=$(date +%F_%T)
ns=default

# For AKS runs, store artifacts in a separate "aks" subdirectory
if [[ "$USE_AKS" == "true" || "$USE_AKS" == "1" ]]; then
  ARTIFACTS="${SCRIPT_DIR}/run/${timestamp}/aks"
else
  ARTIFACTS="${SCRIPT_DIR}/run/${timestamp}"
fi

mkdir -p "$ARTIFACTS"

# Log all output to a file while also displaying on terminal
LOGFILE="${ARTIFACTS}/benchmark_run.log"
exec > >(tee -a "$LOGFILE") 2>&1

echo "=========================================="
echo "Running func-chain benchmark"
if [[ "$USE_AKS" == "true" || "$USE_AKS" == "1" ]]; then
  echo "Environment: AKS"
else
  echo "Environment: Local"
fi
echo "Strategy: $STRATEGY"
echo "Rate: $RATE req/sec"
echo "Duration: $DURATION"
echo "Target: $TARGET"
echo "Format Limit: $FORMAT_LIMIT records"
echo "Artifacts: $ARTIFACTS"
echo "=========================================="

# Teardown all strategies before starting to ensure clean state
if [[ "${SKIP_INITIAL_TEARDOWN:-false}" != "true" ]]; then
  echo ""
  echo "Performing initial teardown of all strategies..."
  "$SCRIPT_DIR/teardown.sh" all
  echo "Initial teardown complete."
  echo ""
fi

function run_job() {
  local name=$1
  local file=$2
  local rate=$3
  local strategy=$4
  local strategy_dir=$5

  # Cleanup from old runs
  kubectl delete job "$name" -n "$ns" --ignore-not-found=true

  # Start the load test
  RATE=$rate DURATION=$DURATION TARGET=$TARGET STRATEGY=$strategy envsubst < "$file" | ko apply --sbom=none -Bf -

  # Wait for pod to be ready
  sleep 5
  kubectl wait --for=condition=ready -n "$ns" pod --selector=job-name="$name" --timeout=300s

  # Follow logs
  kubectl logs -n "$ns" -f "job.batch/$name"

  # Dump logs to artifact file
  kubectl logs -n "$ns" "job.batch/$name" > "${strategy_dir}/${strategy}_${rate}rps.log"

  # Cleanup
  kubectl delete "job/$name" -n "$ns" --ignore-not-found=true
  kubectl wait --for=delete "job/$name" --timeout=60s -n "$ns" 2>/dev/null || true
}

function run_benchmark_for_strategy() {
  local strategy=$1
  local strategy_dir="${ARTIFACTS}/${strategy}"
  local queue_image

  echo ""
  echo "=========================================="
  echo "Running benchmark for strategy: $strategy"
  echo "=========================================="

  # Create strategy-specific subfolder
  mkdir -p "$strategy_dir"

  # Update queue-sidecar-image based on strategy
  queue_image=$(get_queue_image_for_strategy "$strategy")
  if [[ -n "$queue_image" ]]; then
    current_image=$(get_queue_sidecar_image)
    if [[ "$current_image" != "$queue_image" ]]; then
      set_queue_sidecar_image "$queue_image"
    else
      echo "queue-sidecar-image already set to $queue_image"
    fi
  else
    echo "Using default queue-sidecar-image for strategy: $strategy"
  fi

  # echo "Running dev/setup.sh to apply configuration..."
  # "$REPO_ROOT/dev/setup.sh"

  # Deploy services for the strategy
  if [[ "${SKIP_DEPLOY:-false}" != "true" ]]; then
    USE_AKS="$USE_AKS" "$SCRIPT_DIR/deploy-services.sh" "$strategy"
    sleep 10
  fi

  # Run the benchmark job
  run_job func-chain-job "${SCRIPT_DIR}/func-chain-job.yaml" "$RATE" "$strategy" "$strategy_dir"

  # Extract traces from Zipkin
  if [[ "${SKIP_TRACES:-false}" != "true" ]]; then
    echo "Extracting traces for strategy: $strategy"
    python3 "${SCRIPT_DIR}/parse-traces.py" "$strategy" "$strategy_dir" --rate "$RATE" --duration "$DURATION" --clear

    # Format traces into statistics
    local csv_file="${strategy_dir}/${strategy}_${RATE}rps_${DURATION}_traces.csv"
    local data_file="${strategy_dir}/${strategy}_${RATE}rps_${DURATION}_traces.data"
    if [[ -f "$csv_file" ]]; then
      echo "Formatting traces for strategy: $strategy (limit: $FORMAT_LIMIT records)"
      python3 "${SCRIPT_DIR}/format.py" "$csv_file" "$data_file" --limit "$FORMAT_LIMIT"
    else
      echo "Warning: CSV file not found: $csv_file, skipping formatting"
    fi
  fi

  # Teardown services after benchmark
  if [[ "${SKIP_TEARDOWN:-false}" != "true" ]]; then
    echo "Tearing down services for strategy: $strategy"
    "$SCRIPT_DIR/teardown.sh" "$strategy"
  fi

  echo "Completed benchmark for strategy: $strategy"
}

# Determine which strategies to run
if [[ "$STRATEGY" == "all" ]]; then
  STRATEGIES_TO_RUN=("${ALL_STRATEGIES[@]}")
else
  STRATEGIES_TO_RUN=("$STRATEGY")
fi

# Run benchmarks for each strategy
for strategy in "${STRATEGIES_TO_RUN[@]}"; do
  run_benchmark_for_strategy "$strategy"
  sleep 60  # Wait between strategies
done

echo ""
echo "=========================================="
echo "All benchmarks completed!"
echo "Results saved to: $ARTIFACTS"
echo ""
echo "Directory structure:"
echo "  $ARTIFACTS/"
echo "    benchmark_run.log (full run output)"
for strategy in "${STRATEGIES_TO_RUN[@]}"; do
  echo "    ${strategy}/"
  echo "      ${strategy}_${RATE}rps.log"
  if [[ "${SKIP_TRACES:-false}" != "true" ]]; then
    echo "      ${strategy}_${RATE}rps_${DURATION}_traces.json (raw Zipkin traces)"
    echo "      ${strategy}_${RATE}rps_${DURATION}_traces.csv (parsed latencies)"
    echo "      ${strategy}_${RATE}rps_${DURATION}_traces.data (formatted statistics)"
  fi
done
echo "=========================================="

# Reset QCNL volume mount setting if running in AKS mode
if [[ "$USE_AKS" == "true" || "$USE_AKS" == "1" ]]; then
  echo ""
  echo "Resetting QCNL volume mount setting to default (enabled)..."
  kubectl patch configmap config-deployment -n knative-serving \
    --type merge -p '{"data":{"enable-qcnl-volume-mount":"true"}}'
fi
