#!/bin/bash

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

ns=default

ko delete -f ${SCRIPT_DIR}/dataplane-probe-setup.yaml
ko apply --sbom=none -Bf ${SCRIPT_DIR}/dataplane-probe-setup.yaml

kubectl wait --timeout=60s --for=condition=ready ksvc -n "$ns" --all
kubectl wait --timeout=60s --for=condition=available deploy -n "$ns" deployment