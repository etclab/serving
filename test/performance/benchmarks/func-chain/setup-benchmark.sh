#!/bin/bash

set -e

# Change to the directory where this script is located
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Get the repository root (4 levels up from test/performance/benchmarks/func-chain/)
REPO_ROOT="$(cd "$SCRIPT_DIR/../../../.." && pwd)"

echo "=========================================="
echo "Setting up cluster for func-chain benchmark..."
echo "=========================================="

cd "$REPO_ROOT"

# Setup minikube cluster
"$REPO_ROOT/dev/setup-minikube.sh"

# Pin the ko repo for dev/setup.sh's ko apply calls. DOCKER_USER lets the
# caller redirect Knative control-plane image pushes to their own Docker Hub.
DOCKER_USER="${DOCKER_USER:-atosh502}"
export KO_DOCKER_REPO="${KO_DOCKER_REPO:-docker.io/${DOCKER_USER}}"

# Setup knative serving
"$REPO_ROOT/dev/setup.sh"

# Install SGX device plugin
"$REPO_ROOT/dev/sgx/deploy-sgx-plugin.sh"
"$REPO_ROOT/dev/sgx/update-pccs-url.sh"

# Setup etcd for key registry (needed for leader-efunction/member-efunction strategies)
"$REPO_ROOT/dev/setup-etcd.sh"

# Setup Zipkin for tracing
"$REPO_ROOT/dev/setup-zipkin.sh"

# Create performance-test-config secret
source "$REPO_ROOT/eval/s/env.sh"

kubectl delete secret performance-test-config -n default --ignore-not-found=true
kubectl create secret generic performance-test-config -n default \
  --from-literal=systemnamespace="${SYSTEM_NAMESPACE:-knative-serving}" \
  --from-literal=jobname="${JOB_NAME:-local}" \
  --from-literal=buildid="${BUILD_ID:-local}"

tmux kill-session -t socat-proxy 2>/dev/null || true
# Port forward to access the local PCCS server from within the cluster
tmux new-session -d -s socat-proxy 'socat TCP-LISTEN:8081,bind=192.168.49.1,fork,reuseaddr TCP:127.0.0.1:8081'

echo "=========================================="
echo "Cluster setup complete."
echo "=========================================="
echo ""
echo "Next steps:"
echo "  1. Deploy services: ./deploy-services.sh <strategy>"
echo "  2. Run benchmark:   ./run-benchmark.sh <strategy> <rate> <duration>"
echo ""
echo "Available strategies: knative, efunction, rsa-efunction, leader-efunction, member-efunction, both, both-sig, both-hash-chain-sig"
