#!/bin/bash

kubectl delete -f dev/yaml/etcd.yaml

# delete the old pvcs
kubectl delete pvc -l app=etcd -n knative-serving

# create the knative-serving namespace
kubectl create namespace knative-serving || echo "namespace already exists"

kubectl apply -f dev/yaml/etcd.yaml