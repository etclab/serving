#!/bin/bash

# Build queue-proxy-ego image
#
# Usage:
#   TAG=bench ./build.sh
#   TAG=bench IMAGE_NAME=atosh502/queue-proxy-ego-pre ./build.sh
#   ./build.sh oe   # Use Dockerfile-oe
#
# Note: QCNL volume mount is now controlled via config-deployment ConfigMap.
# Set 'enable-qcnl-volume-mount: "false"' for AKS deployments.

TAG=${TAG:-latest}
IMAGE_NAME=${IMAGE_NAME:-atosh502/queue-proxy-ego}
PUSH=${PUSH:-true}

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
KEYS_DIR="$SCRIPT_DIR/../keys"
PRIVATE_KEY="$KEYS_DIR/private.pem"
DOCKER_FILE="$SCRIPT_DIR/Dockerfile"

if [[ "$1" == "oe" ]]; then
    DOCKER_FILE="$SCRIPT_DIR/Dockerfile-oe"
fi

PROJECT_ROOT="$SCRIPT_DIR/../.."

# switch to project root
cd $PROJECT_ROOT

PUSH_ARG=""
if [[ "$PUSH" == "true" ]]; then
    PUSH_ARG="--push"
fi

DOCKER_BUILDKIT=1 docker build \
    --secret id=signingkey,src=$PRIVATE_KEY \
    --target deploy \
    --tag "${IMAGE_NAME}:${TAG}" \
    $PUSH_ARG \
    -f $DOCKER_FILE \
    ${PROJECT_ROOT}

if [[ "$PUSH" == "true" ]]; then
    docker rmi "${IMAGE_NAME}:${TAG}" --force || true
    docker pull "${IMAGE_NAME}:${TAG}"
fi

minikube image unload "${IMAGE_NAME}:${TAG}" || true
minikube image load "${IMAGE_NAME}:${TAG}"

cd -