#!/bin/bash

set -e

# Change to the directory where this script is located
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Get the repository root (4 levels up from test/performance/benchmarks/func-chain/)
REPO_ROOT="$(cd "$SCRIPT_DIR/../../../.." && pwd)"

echo "=========================================="
echo "Setting up AKS cluster for func-chain benchmark..."
echo "=========================================="

# cluster has already been setup; 
# skip setup here as it requires loggin in
# # Setup AKS cluster with SGX support (idempotent)
# "$SCRIPT_DIR/setup-aks-cluster.sh"

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

# Setup Knative Eventing (required for Broker/Trigger resources)
echo "Installing Knative Eventing v1.16.5..."
kubectl apply -f https://github.com/knative/eventing/releases/download/knative-v1.16.5/eventing-crds.yaml
kubectl wait --for=condition=Established --all crd --timeout=60s
kubectl apply -f https://github.com/knative/eventing/releases/download/knative-v1.16.5/eventing-core.yaml
kubectl apply -f https://github.com/knative/eventing/releases/download/knative-v1.16.5/in-memory-channel.yaml
kubectl apply -f https://github.com/knative/eventing/releases/download/knative-v1.16.5/mt-channel-broker.yaml
kubectl wait --for=condition=Ready pods --all -n knative-eventing --timeout=300s
echo "Knative Eventing installation complete."

# Apply Azure QCNL ConfigMap (points to Azure ACC cache for DCAP attestation)
echo "Applying Azure QCNL ConfigMap..."
kubectl apply -f "$REPO_ROOT/dev/sgx/sgx-default-qcnl-azure.yaml"

# Note: SGX device plugin is NOT deployed separately.
# AKS with confcom addon provides SGX support automatically.

echo ""
echo "=========================================="
echo "Setting up etcd for key registry..."
echo "=========================================="

# Setup etcd for key registry (needed for leader-efunction/member-efunction strategies)
"$REPO_ROOT/dev/setup-etcd.sh"

echo ""
echo "=========================================="
echo "Setting up Zipkin for tracing..."
echo "=========================================="

# Setup Zipkin for tracing
"$REPO_ROOT/dev/setup-zipkin.sh"

echo ""
echo "=========================================="
echo "Installing kube-prometheus-stack..."
echo "=========================================="

# Install kube-prometheus-stack (Prometheus, Grafana, kube-state-metrics)
# Port forwarding works the same for remote AKS - kubectl tunnels through the API server
"$REPO_ROOT/eval/s/kube-prometheus.sh"

echo ""
echo "=========================================="
echo "Installing InfluxDB (required by func-invocation* benchmarks)..."
echo "=========================================="

# Install InfluxDB via helm and initialize the Knativetest org + knative-serving
# bucket. eval/s/influx.sh sed-rewrites INFLUX_TOKEN in eval/s/env.sh on success,
# so we re-source env.sh below to pick up the fresh token before creating the
# performance-test-config secret.
"$REPO_ROOT/eval/s/influx.sh"

echo ""
echo "=========================================="
echo "Creating performance-test-config secret..."
echo "=========================================="

# Re-source env.sh AFTER influx.sh so INFLUX_TOKEN reflects the freshly-issued
# admin token written into env.sh by the helm install above.
source "$REPO_ROOT/eval/s/env.sh"

kubectl delete secret performance-test-config -n default --ignore-not-found=true
kubectl create secret generic performance-test-config -n default \
  --from-literal=systemnamespace="${SYSTEM_NAMESPACE:-knative-serving}" \
  --from-literal=jobname="${JOB_NAME:-local}" \
  --from-literal=buildid="${BUILD_ID:-local}" \
  --from-literal=influxurl="${INFLUX_URL}" \
  --from-literal=influxtoken="${INFLUX_TOKEN}"

echo ""
echo "=========================================="
echo "Cluster setup complete."
echo "=========================================="
echo ""
echo "Next steps:"
echo "  1. Deploy services: USE_AKS=true ./deploy-services.sh <strategy>"
echo "  2. Run benchmark:   USE_AKS=true ./run-benchmark.sh <strategy> <rate> <duration>"
echo ""
echo "Note: Azure QCNL ConfigMap has been applied for AKS DCAP attestation."
echo ""
echo "Available strategies: knative, efunction, rsa-efunction, leader-efunction, member-efunction, both, both-sig, both-hash-chain-sig"
