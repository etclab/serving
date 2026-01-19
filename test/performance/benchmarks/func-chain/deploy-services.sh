#!/bin/bash

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

STRATEGY="${1:-both}"
ns=default

echo "=========================================="
echo "Deploying func-chain services"
echo "Strategy: $STRATEGY"
echo "=========================================="

# Validate strategy
case "$STRATEGY" in
  knative|efunction|rsa-efunction|leader-efunction|member-efunction|both|both-sig)
    ;;
  *)
    echo "Error: Unknown strategy: $STRATEGY"
    echo "Available strategies: knative, efunction, rsa-efunction, leader-efunction, member-efunction, both, both-sig"
    exit 1
    ;;
esac

# Teardown existing services
"$SCRIPT_DIR/teardown.sh" "$STRATEGY"

# Configure pre-config secrets for the strategy
"$SCRIPT_DIR/pre-config.sh" "$STRATEGY"

# Deploy the services for the selected strategy
SERVICE_FILE="$SCRIPT_DIR/services/${STRATEGY}.yaml"
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

  LEADER_SERVICE_FILE="$SCRIPT_DIR/services/leader-efunction.yaml"

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

echo "=========================================="
echo "Services deployed successfully!"
echo "=========================================="
kubectl get ksvc -n "$ns"
