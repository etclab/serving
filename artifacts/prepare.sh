#!/usr/bin/env bash
# prepare.sh - Idempotent setup for SACMAT 2026 artifact evaluation.
# Checks for each required binary and installs only if missing.

set -u

log()  { printf '[prepare] %s\n' "$*"; }
warn() { printf '[prepare][WARN] %s\n' "$*" >&2; }
err()  { printf '[prepare][ERROR] %s\n' "$*" >&2; }

have() { command -v "$1" >/dev/null 2>&1; }

check_or_install() {
    local bin="$1"
    local version_cmd="$2"
    local install_fn="$3"

    if have "$bin"; then
        log "[OK] '$bin' exists at $(command -v "$bin")"
        log "     version: $(eval "$version_cmd" 2>&1 | head -n 1)"
        return 0
    fi

    log "[MISSING] '$bin' not found - installing..."
    if "$install_fn"; then
        if have "$bin"; then
            log "[INSTALLED] '$bin' now available at $(command -v "$bin")"
            log "     version: $(eval "$version_cmd" 2>&1 | head -n 1)"
        else
            err "'$bin' install reported success but binary still missing"
            return 1
        fi
    else
        err "Failed to install '$bin'"
        return 1
    fi
}

# ----- installers -----

install_apt_pkg() {
    local pkg="$1"
    sudo apt-get update -y
    sudo apt-get install -y "$pkg"
}

install_git()     { install_apt_pkg git; }
install_gnuplot() { install_apt_pkg gnuplot; }
install_cpuid()   { install_apt_pkg cpuid; }

install_docker() {
    local tmp
    tmp="$(mktemp -d)"
    curl -fsSL https://get.docker.com -o "$tmp/get-docker.sh"
    sudo sh "$tmp/get-docker.sh"
    rm -rf "$tmp"
    if ! getent group docker | grep -qw "$USER"; then
        log "Adding user '$USER' to docker group (re-login required to take effect)"
        sudo usermod -aG docker "$USER"
    fi
}

install_kubectl() {
    local k8s_version
    k8s_version="$(curl -L -s https://dl.k8s.io/release/stable.txt)"
    local tmp
    tmp="$(mktemp -d)"
    curl -L -o "$tmp/kubectl" "https://dl.k8s.io/release/${k8s_version}/bin/linux/amd64/kubectl"
    chmod +x "$tmp/kubectl"
    sudo mv "$tmp/kubectl" /usr/local/bin/kubectl
    rm -rf "$tmp"
}

install_minikube() {
    local tmp
    tmp="$(mktemp -d)"
    curl -L -o "$tmp/minikube-linux-amd64" \
        https://storage.googleapis.com/minikube/releases/latest/minikube-linux-amd64
    sudo install "$tmp/minikube-linux-amd64" /usr/local/bin/minikube
    rm -rf "$tmp"
}

install_helm() {
    curl -fsSL https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | bash
}

install_az() {
    curl -sL https://aka.ms/InstallAzureCLIDeb | sudo bash
}

install_func() {
    local tmp
    tmp="$(mktemp -d)"
    curl -L -o "$tmp/func" \
        https://github.com/knative/func/releases/latest/download/func_linux_amd64
    chmod +x "$tmp/func"
    sudo mv "$tmp/func" /usr/local/bin/func
    rm -rf "$tmp"
}

install_kn() {
    local tmp
    tmp="$(mktemp -d)"
    curl -L -o "$tmp/kn" \
        https://github.com/knative/client/releases/latest/download/kn-linux-amd64
    chmod +x "$tmp/kn"
    sudo mv "$tmp/kn" /usr/local/bin/kn
    rm -rf "$tmp"
}

install_ko() {
    local gobin="${GOPATH:-$HOME/go}/bin"
    /usr/local/go/bin/go install github.com/google/ko@latest
    sudo mv "$gobin/ko" /usr/local/bin/ko
}

install_go() {
    local go_version="1.24.3"
    local tarball="go${go_version}.linux-amd64.tar.gz"
    local tmp
    tmp="$(mktemp -d)"
    curl -L -o "$tmp/$tarball" "https://go.dev/dl/$tarball"
    sudo rm -rf /usr/local/go
    sudo tar -C /usr/local -xzf "$tmp/$tarball"
    rm -rf "$tmp"
    export PATH="$PATH:/usr/local/go/bin"
    if ! grep -q '/usr/local/go/bin' "$HOME/.bashrc" 2>/dev/null; then
        echo 'export PATH=$PATH:/usr/local/go/bin' >> "$HOME/.bashrc"
        log "Added /usr/local/go/bin to PATH in ~/.bashrc (re-source or re-login to take effect)"
    fi
}

# ----- main -----

log "Starting artifact prerequisite check"

check_or_install git      "git --version"               install_git
check_or_install gnuplot  "gnuplot --version"           install_gnuplot
check_or_install docker   "docker --version"            install_docker
check_or_install kubectl  "kubectl version --client=true --output=yaml" install_kubectl
check_or_install minikube "minikube version"            install_minikube
check_or_install helm     "helm version"                install_helm
check_or_install func     "func version"                install_func
check_or_install kn       "kn version"                  install_kn
check_or_install go       "go version"                  install_go
check_or_install ko       "ko version"                  install_ko
check_or_install az       "az version"                  install_az
log "NOTE: If you plan to use a remote (e.g. AKS) cluster, run 'az login' to authenticate the Azure CLI before running the benchmark setup scripts."
check_or_install cpuid    "cpuid -v"                    install_cpuid

# SGX capability check (informational, no install).
log "Checking for SGX support via cpuid"
if have cpuid; then
    sgx_out="$(cpuid 2>/dev/null || true)"

    sgx_supported="$(printf '%s\n' "$sgx_out" | grep -m1 -E 'SGX: Software Guard Extensions supported' | awk -F'=' '{gsub(/ /,"",$2); print $2}')"
    sgx1="$(printf '%s\n'   "$sgx_out" | grep -m1 -E 'SGX1 supported'                            | awk -F'=' '{gsub(/ /,"",$2); print $2}')"
    sgx2="$(printf '%s\n'   "$sgx_out" | grep -m1 -E 'SGX2 supported'                            | awk -F'=' '{gsub(/ /,"",$2); print $2}')"
    sgx_lc="$(printf '%s\n' "$sgx_out" | grep -m1 -E 'SGX_LC: SGX launch config supported'       | awk -F'=' '{gsub(/ /,"",$2); print $2}')"
    sgx_keys="$(printf '%s\n' "$sgx_out" | grep -m1 -E 'SGX-KEYS: SGX attestation services'      | awk -F'=' '{gsub(/ /,"",$2); print $2}')"

    log "  SGX supported            : ${sgx_supported:-unknown}"
    log "  SGX1 supported           : ${sgx1:-unknown}"
    log "  SGX2 supported           : ${sgx2:-unknown}"
    log "  SGX_LC (launch config)   : ${sgx_lc:-unknown}"
    log "  SGX-KEYS (attestation)   : ${sgx_keys:-unknown}"

    if [ "$sgx_supported" = "true" ]; then
        log "[OK] CPU reports SGX is supported"
    else
        warn "CPU does NOT report SGX support - SGX-dependent benchmarks will not run on this host"
    fi

    sgx_devs=()
    for d in /dev/sgx_enclave /dev/sgx_provision /dev/sgx_vepc /dev/sgx/enclave /dev/sgx/provision /dev/isgx; do
        [ -e "$d" ] && sgx_devs+=("$d")
    done
    if [ "${#sgx_devs[@]}" -gt 0 ]; then
        log "[OK] SGX device node(s) present: ${sgx_devs[*]}"
    else
        warn "No SGX device nodes found under /dev/sgx* or /dev/isgx"
    fi
fi

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(git -C "$HERE" rev-parse --show-toplevel)"
MICRO_BENCH_SETUP="$REPO_ROOT/test/performance/benchmarks/micro-bench/setup-micro-bench.sh"

if [ -x "$MICRO_BENCH_SETUP" ]; then
    log "Running micro-bench setup: $MICRO_BENCH_SETUP"
    "$MICRO_BENCH_SETUP"
else
    warn "micro-bench setup script not found or not executable: $MICRO_BENCH_SETUP"
fi

log "Done."
