#!/bin/bash

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="${SCRIPT_DIR}/../../../../"

cd $ROOT_DIR

./dev/setup-minikube.sh

# Pin the ko repo for dev/setup.sh's ko apply calls. DOCKER_USER lets the
# caller redirect Knative control-plane image pushes to their own Docker Hub.
DOCKER_USER="${DOCKER_USER:-atosh502}"
export KO_DOCKER_REPO="${KO_DOCKER_REPO:-docker.io/${DOCKER_USER}}"

./dev/setup.sh
./dev/sgx/deploy-sgx-plugin.sh
./dev/sgx/update-pccs-url.sh
./eval/s/kube-prometheus.sh
./eval/s/influx.sh
./eval/s/test-secret.sh

cd -