#!/bin/bash

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

STRATEGY="${1:-both}"
ns=default

# All available strategies
ALL_STRATEGIES=("knative" "efunction" "rsa-efunction" "leader-efunction" "member-efunction" "both" "both-sig")

echo "==========================================="
echo "Tearing down func-chain benchmark services"
echo "Strategy: $STRATEGY"
echo "==========================================="

# Validate strategy
case "$STRATEGY" in
  knative|efunction|rsa-efunction|leader-efunction|member-efunction|both|both-sig|all)
    ;;
  *)
    echo "Error: Unknown strategy: $STRATEGY"
    echo "Available strategies: knative, efunction, rsa-efunction, leader-efunction, member-efunction, both, both-sig, all"
    exit 1
    ;;
esac

COMMON_DIR="$SCRIPT_DIR/common"

# Function to teardown a single strategy
teardown_strategy() {
  local strat=$1
  local service_file="$SCRIPT_DIR/services/${strat}.yaml"

  if [[ ! -f "$service_file" ]]; then
    echo "Warning: Service file not found: $service_file, skipping"
    return
  fi

  echo "Deleting services from: $service_file"
  kubectl delete -f "$service_file" --ignore-not-found=true
}

# Teardown strategies
if [[ "$STRATEGY" == "all" ]]; then
  echo "Tearing down all strategies..."
  for strat in "${ALL_STRATEGIES[@]}"; do
    teardown_strategy "$strat"
  done
elif [[ "$STRATEGY" == "member-efunction" ]]; then
  # Member strategy also requires tearing down leader (member cannot exist without leader)
  teardown_strategy "$STRATEGY"
  teardown_strategy "leader-efunction"

  # remove service-config for member
  echo "Removing service-config secret for member..."
  kubectl delete secret service-config -n $ns --ignore-not-found=true
else
  teardown_strategy "$STRATEGY"
fi

# Delete common resources (broker, triggers, autoscaler config)
echo "Deleting common resources from: $COMMON_DIR"
kubectl delete -f "$COMMON_DIR" --ignore-not-found=true

# Delete any running jobs
kubectl delete job -n "$ns" func-chain-job --ignore-not-found=true

# Wait for pods to be deleted
echo "Waiting for pods to be deleted..."
kubectl wait --for=delete pods -l serving.knative.dev/service -n "$ns" --timeout=60s 2>/dev/null || true

# Delete all leases in the namespace
echo "Deleting all leases..."
kubectl delete leases --all -n "$ns" --ignore-not-found=true

# Wait for leases to be released
echo "Waiting for leases to be released..."
kubectl wait --for=delete leases --all -n "$ns" --timeout=60s 2>/dev/null || true

echo "Teardown complete."
