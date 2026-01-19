#!/bin/bash
# Build function images for func-chain benchmark
# Builds SGX-enabled and/or vanilla (stock) images with configurable tags

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FUNCTIONS_DIR="$SCRIPT_DIR/../../../../dev/functions"
SCAFFOLD_DIR="$FUNCTIONS_DIR/scaffold"
KEYS_DIR="$SCRIPT_DIR/../../../../dev/keys"
PRIVATE_KEY="$KEYS_DIR/private.pem"

# Docker registry (can be overridden via environment variable)
REGISTRY="${REGISTRY:-atosh502}"

# Functions to build
FUNCTIONS=(
    "validate-fun"
    "vote-fun"
    "count-vote-fun"
    "display-fun"
)

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

usage() {
    echo "Usage: $0 [OPTIONS]"
    echo ""
    echo "Build function images for func-chain benchmark."
    echo ""
    echo "Options:"
    echo "  --type TYPE         Build type: 'sgx', 'stock' (vanilla), or 'all' (default: all)"
    echo "  -t, --tag TAG       Image tag name (default: 'bench' for sgx, 'stock' for stock)"
    echo "  -f, --function NAME Build specific function (can be repeated)"
    echo "  -r, --registry REG  Docker registry (default: atosh502)"
    echo "  --no-push           Build without pushing to registry"
    echo "  -h, --help          Show this help message"
    echo ""
    echo "Examples:"
    echo "  $0                              # Build all functions (sgx:bench + stock:stock)"
    echo "  $0 --type sgx                   # Build only SGX-enabled images with :bench tag"
    echo "  $0 --type stock                 # Build only vanilla images with :stock tag"
    echo "  $0 --type sgx -t v1.0           # Build SGX images with custom :v1.0 tag"
    echo "  $0 -f validate-fun --type sgx   # Build only validate-fun SGX image"
    echo "  $0 --no-push                    # Build all but don't push"
}

log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# Build SGX-enabled image using EGo
build_sgx_image() {
    local func_name=$1
    local tag=$2
    local func_dir="$FUNCTIONS_DIR/$func_name"

    if [[ ! -d "$func_dir" ]]; then
        log_error "Function directory not found: $func_dir"
        return 1
    fi

    if [[ ! -f "$PRIVATE_KEY" ]]; then
        log_error "Private key not found: $PRIVATE_KEY"
        log_error "Generate with: openssl genrsa -out $PRIVATE_KEY 3072"
        return 1
    fi

    log_info "Building SGX image: ${REGISTRY}/${func_name}:${tag}"

    # Ensure scaffold/f directory exists
    mkdir -p "$SCAFFOLD_DIR/f"

    # Copy function code to scaffold/f
    cp -r "$func_dir"/* "$SCAFFOLD_DIR/f/"

    # Build the Docker image
    pushd "$SCAFFOLD_DIR" > /dev/null

    if [[ "$NO_PUSH" == "true" ]]; then
        DOCKER_BUILDKIT=1 docker build \
            --secret id=signingkey,src="$PRIVATE_KEY" \
            --target deploy \
            -t "${REGISTRY}/${func_name}:${tag}" .
    else
        DOCKER_BUILDKIT=1 docker build \
            --secret id=signingkey,src="$PRIVATE_KEY" \
            --target deploy \
            -t "${REGISTRY}/${func_name}:${tag}" \
            --push .
    fi

    docker rmi "${REGISTRY}/${func_name}:${tag}"
    docker pull "${REGISTRY}/${func_name}:${tag}"

    popd > /dev/null

    log_info "Successfully built: ${REGISTRY}/${func_name}:${tag}"
}

# Build vanilla (stock) image without SGX using func CLI
build_stock_image() {
    local func_name=$1
    local tag=$2
    local func_dir="$FUNCTIONS_DIR/$func_name"

    if [[ ! -d "$func_dir" ]]; then
        log_error "Function directory not found: $func_dir"
        return 1
    fi

    if ! command -v func &> /dev/null; then
        log_error "func CLI not found. Install from: https://knative.dev/docs/functions/install-func/"
        return 1
    fi

    log_info "Building stock image: ${REGISTRY}/${func_name}:${tag}"

    local image="${REGISTRY}/${func_name}:${tag}"

    if [[ "$NO_PUSH" == "true" ]]; then
        func build --path "$func_dir" --image "$image"
    else
        func build --path "$func_dir" --image "$image" --push
    fi

    docker rmi "${image}"
    docker pull "${image}"

    log_info "Successfully built: ${image}"
}

# Parse arguments
BUILD_TYPE="all"
TAG=""
SELECTED_FUNCTIONS=()
NO_PUSH="false"

while [[ $# -gt 0 ]]; do
    case $1 in
        --type)
            BUILD_TYPE="$2"
            shift 2
            ;;
        -t|--tag)
            TAG="$2"
            shift 2
            ;;
        -f|--function)
            SELECTED_FUNCTIONS+=("$2")
            shift 2
            ;;
        -r|--registry)
            REGISTRY="$2"
            shift 2
            ;;
        --no-push)
            NO_PUSH="true"
            shift
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        *)
            log_error "Unknown option: $1"
            usage
            exit 1
            ;;
    esac
done

# Validate build type
if [[ "$BUILD_TYPE" != "sgx" && "$BUILD_TYPE" != "stock" && "$BUILD_TYPE" != "all" ]]; then
    log_error "Invalid build type: $BUILD_TYPE. Must be 'sgx', 'stock', or 'all'"
    exit 1
fi

# Use all functions if none specified
if [[ ${#SELECTED_FUNCTIONS[@]} -eq 0 ]]; then
    SELECTED_FUNCTIONS=("${FUNCTIONS[@]}")
fi

# Validate selected functions
for func in "${SELECTED_FUNCTIONS[@]}"; do
    if [[ ! " ${FUNCTIONS[*]} " =~ " ${func} " ]]; then
        log_error "Unknown function: $func"
        log_error "Available functions: ${FUNCTIONS[*]}"
        exit 1
    fi
done

log_info "Building images for: ${SELECTED_FUNCTIONS[*]}"
log_info "Build type: $BUILD_TYPE"
[[ -n "$TAG" ]] && log_info "Tag override: $TAG"
log_info "Registry: $REGISTRY"
[[ "$NO_PUSH" == "true" ]] && log_info "Push: disabled"

# Build images
for func in "${SELECTED_FUNCTIONS[@]}"; do
    if [[ "$BUILD_TYPE" == "sgx" || "$BUILD_TYPE" == "all" ]]; then
        # Use custom tag if provided, otherwise default to "bench"
        sgx_tag="${TAG:-bench}"
        build_sgx_image "$func" "$sgx_tag"
    fi

    if [[ "$BUILD_TYPE" == "stock" || "$BUILD_TYPE" == "all" ]]; then
        # Use custom tag if provided, otherwise default to "stock"
        stock_tag="${TAG:-stock}"
        build_stock_image "$func" "$stock_tag"
    fi

    sleep 10
done

log_info "Build complete!"
