#!/bin/bash

# Change to the directory where this script is located
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Get the repository root (4 levels up from test/performance/benchmarks/func-chain/)
REPO_ROOT="$(cd "$SCRIPT_DIR/../../../.." && pwd)"

# Setup knative serving
"$REPO_ROOT/dev/setup.sh"

# Install SGX device plugin
"$REPO_ROOT/dev/sgx/deploy-sgx-plugin.sh"
"$REPO_ROOT/dev/sgx/update-pccs-url.sh"

# Setup etcd for key registry (needed for leader-efunction/member-efunction strategies)
"$REPO_ROOT/dev/setup-etcd.sh"

"$SCRIPT_DIR/pre-config.sh"

CONFIGMAP_NAME="config-deployment"
CONFIGMAP_NAMESPACE="knative-serving"
NEW_IMAGE="atosh502/queue-proxy-ego-pre:latest"

kubectl patch configmap "$CONFIGMAP_NAME" -n "$CONFIGMAP_NAMESPACE" \
    --type merge -p "{\"data\":{\"queue-sidecar-image\":\"$NEW_IMAGE\"}}"

tmux kill-session -t socat-proxy 2>/dev/null || true

# Port forward to access the local PCCS server from within the cluster
tmux new-session -d -s socat-proxy 'socat TCP-LISTEN:8081,bind=192.168.49.1,fork,reuseaddr TCP:127.0.0.1:8081'
