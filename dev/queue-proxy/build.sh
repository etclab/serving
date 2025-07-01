#!/bin/bash

TAG=${TAG:-latest}

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
KEYS_DIR="$SCRIPT_DIR/../keys"
PRIVATE_KEY="$KEYS_DIR/private.pem"
DOCKER_FILE="$SCRIPT_DIR/Dockerfile"

PROJECT_ROOT="$SCRIPT_DIR/../.."

# switch to project root 
cd $PROJECT_ROOT

DOCKER_BUILDKIT=1 docker build \
    --secret id=signingkey,src=$PRIVATE_KEY \
    --target deploy \
    --tag "atosh502/queue-proxy-ego:${TAG}" \
    --push \
    -f $DOCKER_FILE \
    ${PROJECT_ROOT}

cd - 