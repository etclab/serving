#!/bin/bash

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

kubectl delete -f "$SCRIPT_DIR/func.yaml" || true

kubectl delete leases.coordination.k8s.io -n default --all || true

kubectl exec -it -n knative-serving etcd-0 -- etcdctl del "" --prefix

# Clear sealed state files from minikube node
minikube ssh -- sudo rm -rf /var/lib/sealed-state/*
