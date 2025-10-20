### Creating ego-enlightened `queue-proxy` image
- the image is built and pushed to registry using: `./build.sh`
    - the script uses the private key from: `./dev/keys/private.pem`
    - builds image named: `atosh502/queue-proxy-ego`

### Create a stock `queue-proxy` image (from main branch)
- `KO_DOCKER_REPO=docker.io/atosh502 ko build ./cmd/queue -t og --push --sbom none`
- image: `docker.io/atosh502/queue-39be6f1d08a095bd076a71d288d295b6:og`

#### Appendix
- a lot of errors encountered was due to environment variables and mount volumes not being forwarded inside the enclave; `enclave.json` currently adds (forwards) all the known volumes and environment variables; [`ego's config file reference](https://docs.edgeless.systems/ego/reference/config)
- also there were errors (couldn't get SGX device) due to Seccomp profile assigned to the `queue-proxy`'s container (disallowed root users, no privilege escalation, restricted system calls); for now the security context has been completely disabled