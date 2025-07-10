#!/bin/bash

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

source "${SCRIPT_DIR}/../../../../eval/s/env.sh"

ns=default
FUNCTION_YAML_LEADER="${SCRIPT_DIR}/ego-leader.yaml"
FUNCTION_YAML_MEMBER="${SCRIPT_DIR}/ego-member.yaml"

kubectl delete -f $FUNCTION_YAML_LEADER
kubectl apply -f $FUNCTION_YAML_LEADER

kubectl wait --timeout=60s --for=condition=ready ksvc -n "$ns" --all
kubectl wait --timeout=60s --for=condition=available deploy -n "$ns" --all

kubectl delete -f $FUNCTION_YAML_MEMBER
kubectl apply -f $FUNCTION_YAML_MEMBER

kubectl wait --timeout=60s --for=condition=ready ksvc -n "$ns" --all
kubectl wait --timeout=60s --for=condition=available deploy -n "$ns" --all