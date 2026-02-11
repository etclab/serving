#!/usr/bin/env bash

# Auditor run script
# Runs the standalone auditor that discovers completed per-flow chains,
# verifies them, and anchors batches onto the global audit chain.

set -euo pipefail

# Default configuration
ETCD_ENDPOINTS="${ETCD_ENDPOINTS:-localhost:2379}"
KEYS_DIR="${KEYS_DIR:-dev/client}"
FUNCTION_CHAIN="${FUNCTION_CHAIN:-validate-fun/vote-fun/count-vote-fun/display-fun}"
POLL_INTERVAL="${POLL_INTERVAL:-200ms}"
BATCH_SIZE="${BATCH_SIZE:-5}"
BATCH_TIMEOUT="${BATCH_TIMEOUT:-2s}"
WRITER_ID="${WRITER_ID:-auditor}"

# Change to repository root
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
cd "${REPO_ROOT}"

# exposing etcd externally for auditor to access
echo "=== Exposing etcd externally ==="

# Apply the external service
kubectl apply -f "./dev/yaml/etcd-external.yaml"

# Wait for service to be ready
echo "Waiting for service to be ready..."
sleep 2

# Get the NodePort and IP
NODE_PORT=$(kubectl get svc etcd-external -n knative-serving -o jsonpath='{.spec.ports[0].nodePort}')
NODE_IP=$(kubectl get nodes -o jsonpath='{.items[0].status.addresses[?(@.type=="InternalIP")].address}')
ETCD_ENDPOINTS="${NODE_IP}:${NODE_PORT}"

echo "=== Running Auditor ==="
echo "etcd endpoints:  ${ETCD_ENDPOINTS}"
echo "keys directory:  ${KEYS_DIR}"
echo "function chain:  ${FUNCTION_CHAIN}"
echo "poll interval:   ${POLL_INTERVAL}"
echo "batch size:      ${BATCH_SIZE}"
echo "batch timeout:   ${BATCH_TIMEOUT}"
echo "writer ID:       ${WRITER_ID}"
echo ""

# Run the auditor
exec go run ./dev/auditor/ \
  -etcd-endpoints "${ETCD_ENDPOINTS}" \
  -keys-dir "${KEYS_DIR}" \
  -function-chain "${FUNCTION_CHAIN}" \
  -poll-interval "${POLL_INTERVAL}" \
  -batch-size "${BATCH_SIZE}" \
  -batch-timeout "${BATCH_TIMEOUT}" \
  -writer-id "${WRITER_ID}"
