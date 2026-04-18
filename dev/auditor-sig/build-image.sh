#!/usr/bin/env bash

# Build and push the auditor-sig Docker image using ego-go.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

REGISTRY="${KO_DOCKER_REPO:-docker.io/atosh502}"
TAG="${1:-bench}"
IMAGE="${REGISTRY}/auditor-sig:${TAG}"

echo "=== Building auditor-sig ==="
echo "Image: ${IMAGE}"

docker build -t "${IMAGE}" -f "${SCRIPT_DIR}/Dockerfile" "${REPO_ROOT}"

echo ""
echo "Build complete: ${IMAGE}"
echo ""

if [[ "${PUSH:-true}" != "false" ]]; then
  echo "Pushing ${IMAGE}..."
  docker push "${IMAGE}"
  echo "Push complete."
else
  echo "Skipping push (PUSH=false). Run: docker push ${IMAGE}"
fi

docker rmi "${IMAGE}" --force || true
docker pull "${IMAGE}"

minikube image unload "${IMAGE}" || true
minikube image load "${IMAGE}"
