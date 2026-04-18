#!/bin/bash

# Get the directory where this script is located
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# Get the repository root (parent of dev/)
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

kubectl exec -it -n knative-serving etcd-0 -- etcdctl del "" --prefix

kubectl delete -f "${SCRIPT_DIR}/yaml/etcd.yaml"

# delete the old pvcs
kubectl delete pvc -l app=etcd -n knative-serving

# create the knative-serving namespace
kubectl create namespace knative-serving || echo "namespace already exists"

kubectl apply -f "${SCRIPT_DIR}/yaml/etcd.yaml"

# wait for etcd pod to exist, then wait for it to be ready
echo "Waiting for etcd to be ready..."
until kubectl get pods -l app=etcd -n knative-serving -o name 2>/dev/null | grep -q .; do
  sleep 1
done
kubectl wait --for=condition=Ready pods -l app=etcd -n knative-serving --timeout=300s
