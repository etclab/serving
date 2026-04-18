#!/usr/bin/env bash
# Prepare this machine to run the microbenchmark figures (fig5, fig6).
#
# What this does:
#   * installs gnuplot + python3 via apt (Ubuntu/Debian)
#   * initializes the ncircle git submodule
#   * verifies go, gnuplot, python3, and git are on PATH
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(git -C "$HERE" rev-parse --show-toplevel)"

log() { printf "==> %s\n" "$*"; }
die() { printf "error: %s\n" "$*" >&2; exit 1; }

install_system_packages() {
    if ! command -v apt-get >/dev/null 2>&1; then
        die "apt-get not found; this script targets Ubuntu/Debian. Install gnuplot and python3 manually."
    fi
    log "installing gnuplot and python3 via apt-get"
    sudo apt-get update
    sudo apt-get install -y gnuplot python3
}

init_submodules() {
    log "initializing ncircle git submodule"
    git -C "$REPO_ROOT" submodule update --init --recursive \
        "test/performance/benchmarks/micro-bench/ncircle"
}

verify_tools() {
    for t in go gnuplot python3 git; do
        if ! command -v "$t" >/dev/null 2>&1; then
            die "$t is not on PATH; install it before running the figure scripts"
        fi
    done
    log "go=$(go env GOVERSION) gnuplot=$(gnuplot --version) python3=$(python3 --version)"
}

install_system_packages
init_submodules
verify_tools

cat <<'EOF'

Setup complete. To produce the microbenchmark figures:
  cd proxy-re-encrypt-comparison && ./run-fig5.sh
  cd sign-verify-signature-schemes && ./run-fig6.sh
EOF
