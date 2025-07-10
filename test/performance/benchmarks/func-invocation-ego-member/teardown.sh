#!/bin/bash

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

ns=default
FUNCTION_YAML="${SCRIPT_DIR}/ego-leader.yaml"
FUNCTION_MEMBER_YAML="${SCRIPT_DIR}/ego-member.yaml"

kubectl delete secret pre-config -n $ns --ignore-not-found=true
kubectl delete secret service-config -n $ns --ignore-not-found=true

kubectl delete leases.coordination.k8s.io --all -n $ns
kubectl wait --for=delete leases.coordination.k8s.io --all -n $ns --timeout=300s

kubectl exec -it -n knative-serving etcd-0 -- etcdctl del "" --prefix

kubectl delete -f $FUNCTION_YAML
kubectl delete -f $FUNCTION_MEMBER_YAML
kubectl wait --for=delete pods --all -n $ns --timeout=300s

