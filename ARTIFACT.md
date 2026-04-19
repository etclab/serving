# SACMAT 2026 Artifact Evaluation

## Prerequisites
- All benchmarks are run on Linux/Ubuntu machines and require the following pre-requisites: 
    - [docker](https://docs.docker.com/desktop/setup/install/linux/ubuntu/)
    - [kubectl](https://kubernetes.io/docs/tasks/tools/install-kubectl-linux/)
    - [minikube](https://minikube.sigs.k8s.io/docs/start/?arch=%2Flinux%2Fx86-64%2Fstable%2Fbinary+download)
    - [azure cli](https://learn.microsoft.com/en-us/cli/azure/install-azure-cli-linux?view=azure-cli-latest&pivots=apt)
    - [SGX enabled nodes](https://docs.edgeless.systems/ego/getting-started/troubleshoot#hardware)

## Figure 8 (Section 7.2 Application Macrobenchmark)
- Compares end-to-end function-chain latency for the `emojivoto` workflow across every strategy (`knative`, `efunction`, `rsa-efunction`, `leader-efunction`, `member-efunction`, `both`, `both-sig`, `both-hash-chain-sig`). Runs the benchmark for each strategy, extracts per-strategy `*_traces.data`, and renders `fig8.pdf`.

### Step 0: Build images (Optional)
- The four `emojivoto` functions (`validate-fun`, `vote-fun`, `count-vote-fun`, `display-fun`) need to be built and signed with `ego-go` before they can run on SGX enclaves. All required images have already been built and are publicly available on Docker Hub (under `docker.io/atosh502/*`), so this step can be skipped.
- To rebuild the images with a custom registry/tag, use the helper script:
    ```bash
    cd test/performance/benchmarks/func-chain
    ./build-images.sh --type sgx           # SGX-enabled images, tag :bench
    ./build-images.sh --type stock         # vanilla (non-SGX) images, tag :stock
    ```

### Step 1: Setup Kubernetes cluster
- Cluster setup can be done either on a SGX-enabled local machine or on a remote Azure Kubernetes cluster (AKS) consisting of SGX-enabled nodes (`Standard_DC4s_v3`).
    ```bash
    cd test/performance/benchmarks/func-chain
    # on a SGX-enabled local machine
    ./setup-benchmark.sh
    # or on AKS
    ./setup-benchmark-aks.sh
    ```

### Step 2: Run benchmark and render Figure 8
- `artifact/run-fig8.sh` drives the full flow: it runs the benchmark across all strategies, copies the resulting `*_traces.data` files into `artifact/`, and renders `artifact/fig8.pdf` via gnuplot. Defaults are `RATE=100` and `DURATION=5m` (matching the paper).
    ```bash
    cd test/performance/benchmarks/func-chain
    # re-plot only (reuse .data files already in artifact/)
    SKIP_BENCHMARK=true ./artifact/run-fig8.sh 100 5m
    # complete re-run and plot figures for benchmark
    ./artifact/run-fig8.sh 10 1m
    ```
- Output: `test/performance/benchmarks/func-chain/artifact/fig8.pdf`.

## Table 5 (Section 7.1 Stress tests -> Function Deployment Time)
- Measures the average time (in seconds) to deploy each function configuration: `Knative Function`, `EFunction`, `Lambada Leader EFunction`, and `Lambada Follower EFunction`. For every iteration the benchmark deletes and re-deploys the service, then reads `kube_pod_status_scheduled_time` and `kube_pod_status_ready_time` from Prometheus to compute the per-pod ready-after-scheduled delta. `artifact/run-table5.sh` drives the full flow: it runs the benchmark across all four variants, then invokes `artifact/extract-table5.py` to write `artifact/table5.dat`.

### Step 1: Setup Kubernetes cluster
- Cluster setup can be done either on a SGX-enabled local machine or on a remote Azure Kubernetes cluster consisting of SGX-enabled nodes (`Standard_DC4s_v3`). The setup also deploys `kube-prometheus-stack` (the benchmark expects Prometheus reachable at `localhost:3001` for `kube_pod_status_*` metrics).
    ```bash
    cd test/performance/benchmarks/func-deployment
    # on a SGX-enabled local machine
    ./setup-benchmark.sh
    # or on AKS
    ./setup-benchmark-aks.sh
    ```

### Step 2: Run benchmark and produce Table 5
```bash
cd test/performance/benchmarks/func-deployment
REPEAT=5 ./artifact/run-table5.sh
```
- Output: `test/performance/benchmarks/func-deployment/artifact/table5.dat`.


## Figure 7 (Section 7.1 Stress Tests -> Function invocation)
- Measures single-function invocation latency under a vegeta load sweep (rates `250, 500, 750, 1000, 1250, 1500` rps) across five strategies: `stock` (vanilla Knative function), `enclave` (EFunction in SGX with no encryption), `rsa` (RSA-encrypted EFunction), `lambada-leader` (LAMBADA Leader EFunction with proxy re-encryption), and `lambada-member` (LAMBADA Follower EFunction). For each (strategy, rate) pair the runner deploys the appropriate Knative service, runs vegeta for 5 min, captures the per-rate `Latencies` line, and writes one `<strategy>.data` file per strategy. `artifact/run-fig7.sh` drives the full flow and renders `artifact/fig7.pdf` via gnuplot.

### Step 1: Setup Kubernetes cluster
- Cluster setup can be done either on a SGX-enabled local machine or on a remote Azure Kubernetes cluster consisting of SGX-enabled nodes (`Standard_DC4s_v3`). Reuses the `func-chain` setup since the same Knative + SGX stack is required.
    ```bash
    cd test/performance/benchmarks/func-chain
    # on a SGX-enabled local machine
    ./setup-benchmark.sh
    # or on AKS
    ./setup-benchmark-aks.sh
    ```

### Step 2: Run benchmark and render Figure 7
- `artifact/run-fig7.sh` accepts an optional strategy argument (`all` by default, or one of `stock|enclave|rsa|samba|lambada-member`). The `rsa` strategy requires an RSA private key; if neither `RSA_SK` nor `RSA_SK_FILE` is exported, the script extracts the test key from `func-chain/pre-config.sh`.
    ```bash
    cd test/performance/benchmarks/func-invocation-artifact
    # re-plot only (reuse *.data files already in artifact/)
    SKIP_BENCHMARK=true ./run-fig7.sh
    # complete re-run for all strategies and plot
    ./run-fig7.sh
    ```
- Output: `test/performance/benchmarks/func-invocation-artifact/fig7.pdf` plus one `<strategy>.data` file per strategy in the same directory.


## Microbenchmarks
- Required packages: `go` (>= 1.24), `gnuplot`, `python3`, `git`.
- Run the setup script once. It installs `gnuplot` and `python3` via `apt-get`, initializes the `ncircle` git submodule, and verifies all required tools are on PATH:
    ```bash
    cd test/performance/benchmarks/micro-bench
    ./setup-micro-bench.sh
    ```

### Figure 5 (Section 6.4 Cryptographic Schemes)
- Measures and compares the time for proxy re-encryption algorithms across schemes. Runs the `ncircle` and `cryptofun` Go benchmarks, extracts `fig5.dat`, and renders `fig5.pdf`.
    ```bash
    cd test/performance/benchmarks/micro-bench/proxy-re-encrypt-comparison
    ./run-fig5.sh
    ```

### Figure 6 (Section 6.4 Cryptographic Schemes)
- Compares signature generation and verification times for aggregate and multi-signature schemes as the number of signatures grows. Runs the `ncircle` aggsig/multisig and `cryptofun` sign/verify benchmarks, extracts `fig6.dat`, and renders `fig6.pdf`. The ncircle sweep is slow (~30+ min); the script reuses an existing `ncircle-bench.txt` unless `--fresh-ncircle` is passed.
    ```bash
    cd test/performance/benchmarks/micro-bench/sign-verify-signature-schemes
    ./run-fig6.sh
    ```