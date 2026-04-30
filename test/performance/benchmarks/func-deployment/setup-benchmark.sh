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

# Pin the ko repo for dev/setup.sh's ko apply calls. DOCKER_USER lets the
# caller redirect Knative control-plane image pushes to their own Docker Hub.
DOCKER_USER="${DOCKER_USER:-atosh502}"
export KO_DOCKER_REPO="${KO_DOCKER_REPO:-docker.io/${DOCKER_USER}}"

"$REPO_ROOT/dev/setup.sh"

# install sgx device plugin
"$REPO_ROOT/dev/sgx/deploy-sgx-plugin.sh"
"$REPO_ROOT/dev/sgx/update-pccs-url.sh"

# install kube-prometheus-stack
"$REPO_ROOT/eval/s/kube-prometheus.sh"

echo "=========================================="
echo "Cluster setup complete."
echo "=========================================="
