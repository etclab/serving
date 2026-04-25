#!/bin/bash 

# usage: # ./build-function.sh <scaffold-dir> <function-dir> <docker-image-name>
# ./build-function.sh ./scaffold ./appender appender-ego

# find script dir
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
KEYS_DIR="$SCRIPT_DIR/../keys"
PRIVATE_KEY="$KEYS_DIR/private.pem"

SCAFFOLD_DIR="$1"
FUNCTION_DIR="$2"
DOCKER_IMG_NAME="$3"

TAG=${TAG:-latest}
REGISTRY=${REGISTRY:-atosh502}
PUSH=${PUSH:-true}

IMAGE="${REGISTRY}/${DOCKER_IMG_NAME}:${TAG}"

# ensure <scaffold-dir>/f exists
mkdir -p $SCAFFOLD_DIR/f

# copy everything inside appender dir to scaffold/f dir
cp -r $FUNCTION_DIR/* $SCAFFOLD_DIR/f/

PUSH_ARG=""
if [[ "$PUSH" == "true" ]]; then
    PUSH_ARG="--push"
fi

# run the docker build command
cd $SCAFFOLD_DIR
DOCKER_BUILDKIT=1 docker build --secret id=signingkey,src=$PRIVATE_KEY \
    --target deploy -t "${IMAGE}" $PUSH_ARG .

if [[ "$PUSH" == "true" ]]; then
    docker rmi "${IMAGE}" --force || true
    docker pull "${IMAGE}"
fi

minikube image unload "${IMAGE}" || true
minikube image load "${IMAGE}"

cd -