# SACMAT 2026 Artifact Evaluation

All benchmarks are run on Linux/Ubuntu machines. 

## Setup/Installation
- Install all the required software/packages with:
    ```bash
    ./artifacts/prepare.sh
    source ~/.bashrc
    ```

## Download the artifacts
- Download artifact source files using: 
    ```bash
    git clone https://github.com/etclab/serving.git
    cd serving
    git switch ae-sacmat26
    ```

## Build images (Optional)
- All required images are publicly available from `docker.io/atosh502/*` so this step can be skipped.
- Images can be built by running (set a different Docker username with `--registry` flag; requires `docker login`): 
    ```bash
    ./artifacts/build-all-images.sh --registry <docker-username> --push
    ```
<details>
    <summary>More details</summary>

### Prerequisites
- Docker registry login is needed only if you push (every script defaults to `--push`). For purely local minikube runs, pass `--no-push` to the func-chain wrapper or skip building entirely and pull pre-built images from `docker.io/atosh502/*`.
- Default registry is `atosh502` (Docker Hub). Override via `REGISTRY=` (`build-images.sh`), `IMAGE_NAME=` (`dev/queue-proxy/build.sh`), or `KO_DOCKER_REPO=` (ko + `auditor-sig`).

### Image build reference
- Build function images for the four functions
    - `{validate-fun, vote-fun, count-vote-fun, display-fun}:bench` -> all EFunction (functions running on enclaves) configs use images with `:bench` tag
    - `{validate-fun, vote-fun, count-vote-fun, display-fun}:stock` -> Knative function configs use images with `:stock` tag
        ```bash
        cd test/performance/benchmarks/func-chain
        ./build-images.sh --type sgx
        ./build-images.sh --type stock
        cd -
        ```
- Build function images for the appender function
    - `appender:bench-no-log` image is used for Knative function configs
    - `appender-ego:latest` image is used for EFunction (functions running on enclaves) configs
        ```bash
        cd dev/functions
        func build --path ./appender --image atosh502/appender:bench --push
        func build --path ./appender --image atosh502/appender:bench-no-log --push
        TAG=latest ./build-function.sh ./scaffold ./appender appender-ego
        TAG=bench ./build-function.sh ./scaffold ./appender appender-ego
        cd -
        ```
- Build docker images for `queue-proxy` sidecar
    - `queue-proxy-ego:bench` image is used for EFunction config
    - `queue-proxy-ego-pre:{bench,latest}` image is used for EFunction configs that use proxy re-encryption (Lambada Leader, Follower, hash chain verification)
    - `queue-39be6f1d08a095bd076a71d288d295b6:og` image is used for Knative function configs
        ```bash
        # in branch ae-sacmat26
        TAG=latest IMAGE_NAME=atosh502/queue-proxy-ego-pre ./dev/queue-proxy/build.sh
        TAG=bench IMAGE_NAME=atosh502/queue-proxy-ego-pre ./dev/queue-proxy/build.sh

        # builds queue-39be6f1d08a095bd076a71d288d295b6:og image
        git switch main
        KO_DOCKER_REPO=docker.io/atosh502 ko build --tags og --push --sbom none ./cmd/queue

        # builds queue-proxy-ego:bench image
        TAG=bench ./dev/queue-proxy/build.sh

        git switch ae-sacmat26
        ```
- Hash chain verification/auditor related images
    - `auditor-sig:bench` and `audit-sink:bench` images
        ```bash
        docker build -t atosh502/audit-sink:bench -f dev/audit-sink/Dockerfile . && docker push atosh502/audit-sink:bench
        cd dev/auditor-sig 
        ./build-image.sh bench
        cd -
        ```
</details>



## Figure 8 (Section 7.2 Application Macrobenchmark)
- Compares end-to-end function-chain latency for the `emojivoto` workflow across every strategy (`knative`, `efunction`, `rsa-efunction`, `leader-efunction`, `member-efunction`, `both`, `both-sig`, `both-hash-chain-sig`). Runs the benchmark for each strategy, extracts per-strategy `*_traces.data`, and renders `fig8.pdf`.

### Step 1: Setup Kubernetes cluster
- Cluster setup can be done either on a SGX-enabled local machine or on a remote Azure Kubernetes cluster (AKS) consisting of SGX-enabled nodes (`Standard_DC4s_v3`).
    ```bash
    cd test/performance/benchmarks/func-chain
    # on a SGX-enabled local machine
    ./setup-benchmark.sh
    # or on AKS
    ./setup-benchmark-aks.sh
    cd - 
    ```

### Step 2: Run benchmark and render Figure 8
- `artifact/run-fig8.sh` drives the full flow: it runs the benchmark across all strategies, copies the resulting `*_traces.data` files into `artifact/`, and renders `artifact/fig8.pdf` via gnuplot. Defaults are `RATE=100` and `DURATION=5m` (matching the paper).
    ```bash
    cd test/performance/benchmarks/func-chain
    # re-plot only (reuse .data files already in artifact/)
    SKIP_BENCHMARK=true ./artifact/run-fig8.sh 100 5m
    # complete re-run and plot figures for benchmark
    ./artifact/run-fig8.sh 10 1m
    cd - 
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
    cd - 
    ```

### Step 2: Run benchmark and produce Table 5
```bash
cd test/performance/benchmarks/func-deployment
REPEAT=5 ./artifact/run-table5.sh
cd - 
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
    cd - 
    ```

### Step 2: Run benchmark and render Figure 7
- `artifact/run-fig7.sh` accepts an optional strategy argument (`all` by default, or one of `stock|enclave|rsa|samba|lambada-member`). The `rsa` strategy requires an RSA private key; if neither `RSA_SK` nor `RSA_SK_FILE` is exported, the script extracts the test key from `func-chain/pre-config.sh`.
    ```bash
    cd test/performance/benchmarks/func-invocation-artifact
    # re-plot only (reuse *.data files already in artifact/)
    SKIP_BENCHMARK=true ./run-fig7.sh
    # complete re-run for all strategies and plot
    ./run-fig7.sh
    cd - 
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