# SACMAT 2026 Artifact Evaluation

All benchmarks are run on Linux/Ubuntu machines. 

## 1. Download the artifacts
- Download artifact source files using: 
    ```bash
    git clone https://github.com/etclab/serving.git
    cd serving
    git switch ae-sacmat26
    ```

## 2. Setup/Installation
- Install all the required software/packages with:
    ```bash
    ./artifacts/prepare.sh
    source ~/.bashrc
    ```

## 3. Build images (Optional)
- All required images are publicly available from `docker.io/atosh502/*` so this step can be skipped.
- Images can be built by running (set a different Docker username with `--registry` flag; requires `docker login`): 
    ```bash
    ./artifacts/build-all-images.sh --registry <docker-username> --push
    ```
<details>
    <summary>More details</summary>

#### Prerequisites
- Docker registry login is needed only if you push (every script defaults to `--push`). For purely local minikube runs, pass `--no-push` to the func-chain wrapper or skip building entirely and pull pre-built images from `docker.io/atosh502/*`.
- Default registry is `atosh502` (Docker Hub). Override via `REGISTRY=` (`build-images.sh`), `IMAGE_NAME=` (`dev/queue-proxy/build.sh`), or `KO_DOCKER_REPO=` (ko + `auditor-sig`).

#### Image build reference
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

## 4. Azure Kubernetes Service (AKS) Cluster setup
A two-node Azure Kubernetes Cluster has been setup for running the benchmarks. Please follow the instructions and credentials mentioned in the HotCRP comment to update your local `~/.kube/config` file.

Expected output after successful AKS cluster setup:
```bash
apoudel01@node0:~/serving$ kubectl cluster-info
Kubernetes control plane is running at https://lambada-lambada-3d1fab-gqd9l1pb.hcp.eastus.azmk8s.io:443
CoreDNS is running at https://lambada-lambada-3d1fab-gqd9l1pb.hcp.eastus.azmk8s.io:443/api/v1/namespaces/kube-system/services/kube-dns:dns/proxy
Metrics-server is running at https://lambada-lambada-3d1fab-gqd9l1pb.hcp.eastus.azmk8s.io:443/api/v1/namespaces/kube-system/services/https:metrics-server:/proxy

To further debug and diagnose cluster problems, use 'kubectl cluster-info dump'.
```

## 5. Figure 8 (Section 7.2 Application Macrobenchmark)
- Compares end-to-end function-chain latency for the `emojivoto` application.

### Generating figure 8
- 
    ```bash
    cd test/performance/benchmarks/func-chain
    ./setup-benchmark-aks.sh
    # re-plot only (reuse .data files already in artifact/)
    SKIP_BENCHMARK=true ./artifact/run-fig8.sh 100 5m
    # run complete benchmark on Azure Kubernetes Service (takes ~30-40 mins)
    USE_AKS=true ./artifact/run-fig8.sh 100 30s
    cd -
    ```
- Final output is generated at `test/performance/benchmarks/func-chain/artifact/fig8.pdf`.


## 6. Figure 7 (Section 7.1 Stress Tests -> Function invocation)
- Measures single-function invocation latency using vegeta load generator.

### Generating figure 7
-
    ```bash
    # uses the same cluster setup as Figure 8 so can be skipped
    # cd test/performance/benchmarks/func-chain
    # ./setup-benchmark-aks.sh
    # cd -

    cd test/performance/benchmarks/func-invocation-artifact
    # re-plot only (reuse *.data files already in artifact/)
    SKIP_BENCHMARK=true ./run-fig7.sh
    # run complete benchmark on Azure Kubernetes Service (takes ~30-40 mins)
    USE_AKS=true RATES="100 200 300 400" DURATION=1m ./run-fig7.sh
    cd - 
    ```
- Final output is generated at `test/performance/benchmarks/func-invocation-artifact/fig7.pdf`.


## 7. Table 5 (Section 7.1 Stress tests -> Function Deployment Time)
- Measures the average time (in seconds) to deploy different function configurations.

### Generating Table 5
-
    ```bash
    # uses the same cluster setup as Figure 8 so can be skipped
    # cd test/performance/benchmarks/func-chain
    # ./setup-benchmark-aks.sh
    # cd -

    cd test/performance/benchmarks/func-deployment
    # run complete benchmark on Azure Kubernetes Service (takes <30 mins)
    USE_AKS=true REPEAT=3 ./artifact/run-table5.sh
    cd - 
    ```
- Final output is generated at `test/performance/benchmarks/func-deployment/artifact/table5.dat`.


## 8. Microbenchmarks
### 8.a. Generating figure 5 (Section 6.4 Cryptographic Schemes)
- Measures and compares the time for proxy re-encryption algorithms across schemes. 
    ```bash
    # micro-benchmarks don't requires AKS cluster and can be run locally
    cd test/performance/benchmarks/micro-bench/proxy-re-encrypt-comparison
    ./run-fig5.sh
    cd -
    ```
- Final output is generated at `test/performance/benchmarks/micro-bench/proxy-re-encrypt-comparison/fig5.pdf`.


### 8.b. Generating figure 6 (Section 6.4 Cryptographic Schemes)
- Compares signature generation and verification times for aggregate and multi-signature schemes. The ncircle sweep is slow (>30+ min); the script reuses an existing `ncircle-bench.txt` unless `--fresh-ncircle` is passed.
    ```bash
    # micro-benchmarks don't requires AKS cluster and can be run locally
    cd test/performance/benchmarks/micro-bench/sign-verify-signature-schemes
    ./run-fig6.sh
    cd -
    ```
- Final output is generated at `test/performance/benchmarks/micro-bench/sign-verify-signature-schemes/fig6.pdf`.
