#!/bin/bash

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

ns=default
EMOJI_YAML="${SCRIPT_DIR}/emoji-svc.yaml"
VOTING_YAML="${SCRIPT_DIR}/voting-svc.yaml"
WEB_YAML="${SCRIPT_DIR}/web-svc.yaml"

"${SCRIPT_DIR}"/teardown.sh
"${SCRIPT_DIR}"/pre-config.sh

kubectl apply -f $EMOJI_YAML
kubectl apply -f $VOTING_YAML

kubectl wait --timeout=60s --for=condition=ready ksvc -n "$ns" --all
kubectl wait --timeout=60s --for=condition=available deploy -n "$ns" --all

kubectl apply -f $WEB_YAML

kubectl wait --timeout=60s --for=condition=ready ksvc -n "$ns" --all
kubectl wait --timeout=60s --for=condition=available deploy -n "$ns" --all
