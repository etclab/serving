#!/bin/bash

set -e

# Change to the directory where this script is located
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Get the repository root (4 levels up from test/performance/benchmarks/func-deployment/)
REPO_ROOT="$(cd "$SCRIPT_DIR/../../../.." && pwd)"

echo "=========================================="
echo "Setting up cluster for benchmarks..."
echo "=========================================="

cd "$REPO_ROOT"

"$REPO_ROOT/dev/setup-minikube.sh"
"$REPO_ROOT/dev/setup.sh"

# install sgx device plugin
"$REPO_ROOT/dev/sgx/deploy-sgx-plugin.sh"
"$REPO_ROOT/dev/sgx/update-pccs-url.sh"

# install kube-prometheus-stack
"$REPO_ROOT/eval/s/kube-prometheus.sh"

echo "=========================================="
echo "Cluster setup complete."
echo "=========================================="
