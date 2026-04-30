#!/bin/bash

set -e

# Change to the directory where this script is located
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Get the repository root (4 levels up from test/performance/benchmarks/func-deployment/)
REPO_ROOT="$(cd "$SCRIPT_DIR/../../../.." && pwd)"

echo "=========================================="
echo "Setting up AKS cluster for benchmarks..."
echo "=========================================="

# Setup AKS cluster with SGX support (idempotent)
"$SCRIPT_DIR/setup-aks-cluster.sh"

echo ""
echo "=========================================="
echo "Deploying Knative Serving..."
echo "=========================================="

cd "$REPO_ROOT"

# Pin the ko repo for dev/setup.sh's ko apply calls. DOCKER_USER lets the
# caller redirect Knative control-plane image pushes to their own Docker Hub.
DOCKER_USER="${DOCKER_USER:-atosh502}"
export KO_DOCKER_REPO="${KO_DOCKER_REPO:-docker.io/${DOCKER_USER}}"

# Deploy Knative Serving and related components
"$REPO_ROOT/dev/setup.sh"

# Note: SGX device plugin is NOT deployed separately.
# AKS with confcom addon provides SGX support automatically.

# Note: update-pccs-url.sh is NOT needed for AKS.

echo ""
echo "=========================================="
echo "Installing kube-prometheus-stack..."
echo "=========================================="

# Install kube-prometheus-stack (Prometheus, Grafana, kube-state-metrics)
# Port forwarding works the same for remote AKS - kubectl tunnels through the API server
"$REPO_ROOT/eval/s/kube-prometheus.sh"

echo ""
echo "=========================================="
echo "Cluster setup complete."
echo ""
echo "Port forwards are running in screen sessions:"
echo "  - Grafana:          localhost:3000 -> grafana:80"
echo "  - Prometheus:       localhost:3001 -> prometheus:9090"
echo "  - kube-state-metrics: localhost:3002 -> ksm:8080"
echo ""
echo "To verify Prometheus:"
echo "  curl http://localhost:3001/api/v1/status/runtimeinfo"
echo ""
echo "To run the benchmark:"
echo "  cd $SCRIPT_DIR"
echo "  VARIANT=efunction ./run-func.sh"
echo "=========================================="
