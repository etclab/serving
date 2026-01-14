#!/bin/bash

# Build queue-proxy-ego image
#
# For local/minikube (includes QCNL volume mount):
#   TAG=bench ./build.sh
#
# For Azure AKS (no QCNL volume mount):
#   TAG=bench-aks ./build.sh
#
# For PRE-enabled builds:
#   TAG=bench IMAGE_NAME=atosh502/queue-proxy-ego-pre ./build.sh

TAG=${TAG:-latest}
IMAGE_NAME=${IMAGE_NAME:-atosh502/queue-proxy-ego}

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
KEYS_DIR="$SCRIPT_DIR/../keys"
PRIVATE_KEY="$KEYS_DIR/private.pem"
DOCKER_FILE="$SCRIPT_DIR/Dockerfile"

if [[ "$1" == "oe" ]]; then
    DOCKER_FILE="$SCRIPT_DIR/Dockerfile-oe"
fi

PROJECT_ROOT="$SCRIPT_DIR/../.."
QUEUE_GO="$PROJECT_ROOT/pkg/reconciler/revision/resources/queue.go"

# Automatically handle QCNL volume mount line based on TAG
# For AKS builds, we comment out the line (no QCNL volume needed)
# For local builds, we uncomment the line (QCNL volume needed)
QCNL_LINE="c.VolumeMounts = append(c.VolumeMounts, sgxDefaultQcnlVolumeMount)"

if [[ "$TAG" == *"aks"* ]]; then
    echo "AKS build detected - commenting out QCNL volume mount line..."
    sed -i "s|^\t${QCNL_LINE}|\t// ${QCNL_LINE}|" "$QUEUE_GO"
else
    echo "Local build detected - ensuring QCNL volume mount line is uncommented..."
    sed -i "s|^\t// ${QCNL_LINE}|\t${QCNL_LINE}|" "$QUEUE_GO"
fi

# switch to project root
cd $PROJECT_ROOT

DOCKER_BUILDKIT=1 docker build \
    --secret id=signingkey,src=$PRIVATE_KEY \
    --target deploy \
    --tag "${IMAGE_NAME}:${TAG}" \
    --push \
    -f $DOCKER_FILE \
    ${PROJECT_ROOT}

cd - 