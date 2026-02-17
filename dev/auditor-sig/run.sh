#!/usr/bin/env bash

# Auditor-sig run script
# Runs the standalone BGLS signature auditor that polls completed flow records,
# verifies aggregate signatures, and anchors batches onto the global audit chain.

set -euo pipefail

# Default configuration
ETCD_ENDPOINTS="${ETCD_ENDPOINTS:-localhost:2379}"
KEYS_DIR="${KEYS_DIR:-dev/client}"
POLL_INTERVAL="${POLL_INTERVAL:-200ms}"
BATCH_SIZE="${BATCH_SIZE:-5}"
BATCH_TIMEOUT="${BATCH_TIMEOUT:-2s}"
WRITER_ID="${WRITER_ID:-auditor}"

# Change to repository root
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
cd "${REPO_ROOT}"

# Exposing etcd externally for auditor to access
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

echo "=== Running Auditor-Sig (BGLS Signature Verifier) ==="
echo "etcd endpoints:  ${ETCD_ENDPOINTS}"
echo "keys directory:  ${KEYS_DIR}"
echo "poll interval:   ${POLL_INTERVAL}"
echo "batch size:      ${BATCH_SIZE}"
echo "batch timeout:   ${BATCH_TIMEOUT}"
echo "writer ID:       ${WRITER_ID}"
echo ""

# Run the auditor-sig
exec go run ./dev/auditor-sig/ \
  -etcd-endpoints "${ETCD_ENDPOINTS}" \
  -keys-dir "${KEYS_DIR}" \
  -poll-interval "${POLL_INTERVAL}" \
  -batch-size "${BATCH_SIZE}" \
  -batch-timeout "${BATCH_TIMEOUT}" \
  -writer-id "${WRITER_ID}"
