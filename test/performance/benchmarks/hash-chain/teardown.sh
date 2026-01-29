#!/bin/bash

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Get the repository root (4 levels up from test/performance/benchmarks/func-chain/)
REPO_ROOT="$(cd "$SCRIPT_DIR/../../../.." && pwd)"

kubectl delete -f "$SCRIPT_DIR/func.yaml" || true

kubectl delete leases.coordination.k8s.io -n default --all || true

kubectl exec -it -n knative-serving etcd-0 -- etcdctl del "" --prefix

"$REPO_ROOT/dev/setup-etcd.sh"

# Clear sealed state files from minikube node
minikube ssh -- sudo rm -rf /var/lib/sealed-state/*
