#!/bin/bash

# Get the directory where this script is located
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# Get the repository root (parent of dev/)
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

kubectl delete -f "${SCRIPT_DIR}/yaml/etcd.yaml"

# delete the old pvcs
kubectl delete pvc -l app=etcd -n knative-serving

# create the knative-serving namespace
kubectl create namespace knative-serving || echo "namespace already exists"

kubectl apply -f "${SCRIPT_DIR}/yaml/etcd.yaml"
