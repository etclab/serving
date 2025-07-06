#!/bin/bash

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

ns=default
FUNCTION_YAML="${SCRIPT_DIR}/func-invocation-setup.yaml"

kubectl delete -f $FUNCTION_YAML
kubectl wait --for=delete pods --all -n $ns --timeout=300s

