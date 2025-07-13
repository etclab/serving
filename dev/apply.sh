#!/bin/bash

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

"${SCRIPT_DIR}"/pre-config.sh
# "${SCRIPT_DIR}"/service-config.sh

kubectl apply -f dev/yaml/lease-roles.yaml

kubectl apply -f dev/sample/
# kubectl apply -f dev/baseline/
# kubectl apply -f dev/leader/
# kubectl apply -f dev/both/

kubectl wait --for=condition=ready services.serving.knative.dev --all --timeout=300s
kubectl wait --for=condition=ready pods --all --timeout=300s

# wait until the leader pods are ready
# kubectl apply -f dev/member/

# kubectl wait --for=condition=ready services.serving.knative.dev --all --timeout=300s
# kubectl wait --for=condition=ready pods --all --timeout=300s