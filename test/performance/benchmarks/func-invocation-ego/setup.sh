#!/bin/bash

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

source "${SCRIPT_DIR}/../../../../eval/s/env.sh"

ns=default
FUNCTION_YAML="${SCRIPT_DIR}/func-invocation-ego-setup.yaml"

kubectl delete -f $FUNCTION_YAML
kubectl apply -f $FUNCTION_YAML

kubectl wait --timeout=60s --for=condition=ready ksvc -n "$ns" --all
kubectl wait --timeout=60s --for=condition=available deploy -n "$ns" --all
