#!/bin/bash

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../../.." && pwd)"

STRATEGY="${1:-both}"
ns=default

# AKS mode: use services/aks/ directory (no QCNL volume mounts)
# Set USE_AKS=true to enable (consistent with run-benchmark.sh)
# Export so child scripts (teardown.sh) can access it
export USE_AKS="${USE_AKS:-false}"
if [[ "$USE_AKS" == "1" || "$USE_AKS" == "true" ]]; then
  SERVICES_DIR="$SCRIPT_DIR/services/aks"
  echo "=========================================="
  echo "Deploying func-chain services (AKS mode)"
  echo "Strategy: $STRATEGY"
  echo "=========================================="
  # Ensure QCNL volume mount is disabled for AKS
  echo "Ensuring QCNL volume mount is disabled for AKS..."
  kubectl patch configmap config-deployment -n knative-serving \
    --type merge -p '{"data":{"enable-qcnl-volume-mount":"false"}}'
else
  SERVICES_DIR="$SCRIPT_DIR/services"
  echo "=========================================="
  echo "Deploying func-chain services"
  echo "Strategy: $STRATEGY"
  echo "=========================================="
fi

# Validate strategy
case "$STRATEGY" in
  knative|efunction|rsa-efunction|leader-efunction|member-efunction|both|both-sig|both-hash-chain-sig)
    ;;
  *)
    echo "Error: Unknown strategy: $STRATEGY"
    echo "Available strategies: knative, efunction, rsa-efunction, leader-efunction, member-efunction, both, both-sig, both-hash-chain-sig"
    exit 1
    ;;
esac

# Teardown existing services
"$SCRIPT_DIR/teardown.sh" "$STRATEGY"

# Configure pre-config secrets for the strategy
"$SCRIPT_DIR/pre-config.sh" "$STRATEGY"

# Deploy the services for the selected strategy
# For knative strategy, always use the base services directory (no SGX volumes needed)
if [[ "$STRATEGY" == "knative" ]]; then
  SERVICE_FILE="$SCRIPT_DIR/services/${STRATEGY}.yaml"
else
  SERVICE_FILE="$SERVICES_DIR/${STRATEGY}.yaml"
fi
COMMON_DIR="$SCRIPT_DIR/common"

if [[ ! -f "$SERVICE_FILE" ]]; then
  echo "Error: Service file not found: $SERVICE_FILE"
  exit 1
fi

# Deploy common resources (broker, triggers, autoscaler config)
echo "Deploying common resources from: $COMMON_DIR"
kubectl apply -f "$COMMON_DIR"

# For member-efunction: deploy leader first, wait for ready, then install service-config and deploy member
if [[ "$STRATEGY" == "member-efunction" ]]; then
  # Install service-config for member (shared lease group with leader)
  echo "Installing service-config for member..."
  "$SCRIPT_DIR/service-config.sh"

  LEADER_SERVICE_FILE="$SERVICES_DIR/leader-efunction.yaml"

  # Deploy leader services first
  echo "Deploying leader services from: $LEADER_SERVICE_FILE"
  kubectl apply -f "$LEADER_SERVICE_FILE"

  # Wait for leader services to be ready
  echo "Waiting for all services to be ready..."
  kubectl wait --for=condition=Ready ksvc --all -n "$ns" --timeout=300s

  # Deploy member services
  echo "Deploying member services from: $SERVICE_FILE"
  kubectl apply -f "$SERVICE_FILE"
else
  # Deploy services normally for other strategies
  echo "Deploying services from: $SERVICE_FILE"
  kubectl apply -f "$SERVICE_FILE"
fi

# Wait for all services in the namespace to be ready
echo "Waiting for all services to be ready..."
kubectl wait --for=condition=Ready ksvc --all -n "$ns" --timeout=300s

# Deploy audit-sink for strategies that use BGLS signature verification
if [[ "$STRATEGY" == "both-hash-chain-sig" ]]; then
  echo "Deploying audit-sink for BGLS signature auditing..."
  ko apply --sbom=none -Bf "$REPO_ROOT/dev/yaml/audit-sink.yaml"
  echo "Waiting for audit-sink to be ready..."
  kubectl wait --for=condition=available deployment/audit-sink -n "$ns" --timeout=120s
fi

echo "=========================================="
echo "Services deployed successfully!"
echo "=========================================="
kubectl get ksvc -n "$ns"
