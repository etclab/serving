#!/bin/bash

ns=zipkin-monitoring

kubectl create namespace "$ns" --dry-run=client -o yaml | kubectl apply -f -

kubectl delete deployment zipkin -n "$ns" --ignore-not-found=true
kubectl create deployment zipkin --image openzipkin/zipkin -n "$ns"

kubectl wait --timeout=60s --for=condition=available deploy -n "$ns" --all

kubectl get service zipkin -n "$ns" >/dev/null 2>&1 || \
  kubectl expose deployment zipkin --type ClusterIP --port 9411 -n "$ns"