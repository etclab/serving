#!/bin/bash

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

ns=default
FUNCTION_YAML="${SCRIPT_DIR}/func-invocation-ego-setup.yaml"

kubectl delete secret static-pre-keys -n $ns --ignore-not-found=true

kubectl delete leases.coordination.k8s.io --all -n $ns
kubectl wait --for=delete leases.coordination.k8s.io --all -n $ns --timeout=300s

kubectl exec -it -n knative-serving etcd-0 -- etcdctl del "" --prefix

kubectl delete -f $FUNCTION_YAML
kubectl wait --for=delete pods --all -n $ns --timeout=300s

