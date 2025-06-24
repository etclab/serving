#!/bin/bash

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# this configmap loads the updated PCCS URL into the pods requesting SGX resources
kubectl apply -f "$SCRIPT_DIR/sgx-default-qcnl-local.yaml"