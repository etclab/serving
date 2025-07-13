#!/bin/bash

ns=default

kubectl delete -f dev/sample/
# kubectl delete -f dev/baseline/
# kubectl delete -f dev/member/
# kubectl delete -f dev/both/
# kubectl delete -f dev/leader/
kubectl delete -f dev/yaml/lease-roles.yaml

kubectl delete leases --all -n $ns
kubectl delete leases.coordination.k8s.io --all -n $ns
kubectl wait --for=delete leases.coordination.k8s.io --all -n $ns --timeout=300s

kubectl exec -it -n knative-serving etcd-0 -- etcdctl del "" --prefix
kubectl delete secret pre-config -n $ns --ignore-not-found=true
kubectl delete secret service-config -n $ns --ignore-not-found=true

kubectl wait --for=delete pods --all -n $2 --timeout=300s
