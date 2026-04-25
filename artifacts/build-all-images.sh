#!/usr/bin/env bash
# build-all-images.sh - Build and push all images required for SACMAT 2026
# artifact evaluation. Consolidates the per-section build commands documented
# in ARTIFACT.md "Image build reference".
#
# Usage:
#   ./build-all-images.sh [--registry REGISTRY] [--no-push]
#
# Options:
#   -r, --registry REG   Docker Hub username / registry prefix (default: atosh502).
#                        Images are tagged as <REG>/<image>:<tag>.
#   --no-push            Build images locally without pushing to the registry.
#                        Default is to push (registry login required).
#   -h, --help           Show this help.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

REGISTRY="atosh502"
PUSH="true"

usage() { sed -n '2,15p' "${BASH_SOURCE[0]}"; }

while [[ $# -gt 0 ]]; do
    case "$1" in
        -r|--registry) REGISTRY="$2"; shift 2 ;;
        --no-push)     PUSH="false"; shift ;;
        --push)        PUSH="true";  shift ;;
        -h|--help)     usage; exit 0 ;;
        *) echo "Unknown option: $1" >&2; usage; exit 1 ;;
    esac
done

log() { printf '[build-all-images] %s\n' "$*"; }

log "Registry: $REGISTRY"
log "Push:     $PUSH"

# Subscript flag conventions differ; build helpers per tool.
push_flag()    { [[ "$PUSH" == "true" ]] && echo "--push" || echo ""; }
no_push_flag() { [[ "$PUSH" == "true" ]] && echo ""       || echo "--no-push"; }

# ----------------------------------------------------------------------------
# Section 1: Function images for the four chain functions
#   <REG>/{validate-fun, vote-fun, count-vote-fun, display-fun}:bench  (EFunction)
#   <REG>/{validate-fun, vote-fun, count-vote-fun, display-fun}:stock  (Knative)
# ----------------------------------------------------------------------------
log "Section 1: chain function images (sgx + stock)"
(
    cd "$REPO_ROOT/test/performance/benchmarks/func-chain"
    ./build-images.sh --type sgx   -r "$REGISTRY" $(no_push_flag)
    ./build-images.sh --type stock -r "$REGISTRY" $(no_push_flag)
)

# ----------------------------------------------------------------------------
# Section 2: Appender function images
#   <REG>/appender:{bench,bench-no-log}    (Knative)
#   <REG>/appender-ego:{latest,bench}      (EFunction)
# ----------------------------------------------------------------------------
log "Section 2: appender function images"
(
    cd "$REPO_ROOT/dev/functions"
    func build --path ./appender --image "${REGISTRY}/appender:bench"        $(push_flag)
    func build --path ./appender --image "${REGISTRY}/appender:bench-no-log" $(push_flag)
    REGISTRY="$REGISTRY" PUSH="$PUSH" TAG=latest ./build-function.sh ./scaffold ./appender appender-ego
    REGISTRY="$REGISTRY" PUSH="$PUSH" TAG=bench  ./build-function.sh ./scaffold ./appender appender-ego
)

# ----------------------------------------------------------------------------
# Section 3: queue-proxy sidecar images
#   <REG>/queue-proxy-ego-pre:{latest,bench}      (PRE-enabled EFunction configs)
#   <REG>/queue-39be6f1d08a095bd076a71d288d295b6:og  (Knative configs, built from main)
#   <REG>/queue-proxy-ego:bench                   (plain EFunction config)
# ----------------------------------------------------------------------------
log "Section 3: queue-proxy sidecar images"

# queue-proxy-ego-pre:{latest,bench} -- built on current branch (ae-sacmat26)
PUSH="$PUSH" TAG=latest IMAGE_NAME="${REGISTRY}/queue-proxy-ego-pre" "$REPO_ROOT/dev/queue-proxy/build.sh"
PUSH="$PUSH" TAG=bench  IMAGE_NAME="${REGISTRY}/queue-proxy-ego-pre" "$REPO_ROOT/dev/queue-proxy/build.sh"

# queue-39be6f1d08a095bd076a71d288d295b6:og + queue-proxy-ego:bench -- built from main
log "Switching to main to build queue:og and queue-proxy-ego:bench"
(
    cd "$REPO_ROOT"
    git switch main
    KO_DOCKER_REPO="docker.io/${REGISTRY}" ko build --tags og $(push_flag) --sbom none ./cmd/queue

    PUSH="$PUSH" TAG=bench IMAGE_NAME="${REGISTRY}/queue-proxy-ego" ./dev/queue-proxy/build.sh

    log "Switching back to ae-sacmat26"
    git switch ae-sacmat26
)

# ----------------------------------------------------------------------------
# Section 4: Hash chain verification / auditor images
#   <REG>/audit-sink:bench
#   <REG>/auditor-sig:bench
# ----------------------------------------------------------------------------
log "Section 4: auditor / audit-sink images"
(
    cd "$REPO_ROOT"
    docker build -t "${REGISTRY}/audit-sink:bench" -f dev/audit-sink/Dockerfile .
    if [[ "$PUSH" == "true" ]]; then
        docker push "${REGISTRY}/audit-sink:bench"
    fi
)
(
    cd "$REPO_ROOT/dev/auditor-sig"
    KO_DOCKER_REPO="docker.io/${REGISTRY}" PUSH="$PUSH" ./build-image.sh bench
)

log "All images built."
