#!/bin/bash

ns=zipkin-monitoring

kubectl create namespace zipkin-monitoring

kubectl delete deployment zipkin -n "$ns" --ignore-not-found=true
kubectl create deployment zipkin --image openzipkin/zipkin -n "$ns"

kubectl wait --timeout=60s --for=condition=available deploy -n "$ns" --all

kubectl expose deployment zipkin --type ClusterIP --port 9411 -n "$ns"