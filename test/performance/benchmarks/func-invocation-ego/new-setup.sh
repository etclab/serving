#!/bin/bash

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="${SCRIPT_DIR}/../../../../"

cd $ROOT_DIR

./dev/setup-minikube.sh
./dev/setup.sh
./dev/sgx/deploy-sgx-plugin.sh
./dev/sgx/update-pccs-url.sh
./eval/s/kube-prometheus.sh
./eval/s/influx.sh
./eval/s/test-secret.sh

cd -