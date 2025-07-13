#!/bin/bash

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

ns=default

EMOJI_YAML="${SCRIPT_DIR}/emoji-svc.yaml"
VOTING_YAML="${SCRIPT_DIR}/voting-svc.yaml"
WEB_YAML="${SCRIPT_DIR}/web-svc.yaml"

kubectl delete secret pre-config -n $ns --ignore-not-found=true
kubectl delete secret service-config -n $ns --ignore-not-found=true

kubectl delete leases.coordination.k8s.io --all -n $ns
kubectl wait --for=delete leases.coordination.k8s.io --all -n $ns --timeout=300s

kubectl exec -it -n knative-serving etcd-0 -- etcdctl del "" --prefix

kubectl delete -f $EMOJI_YAML
kubectl delete -f $VOTING_YAML
kubectl delete -f $WEB_YAML

kubectl wait --for=delete pods --all -n $ns --timeout=300s

