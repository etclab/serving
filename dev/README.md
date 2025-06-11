- What to do with the files in this directory?
    - If a golang dependency is changed for `serving` via `go get` run: `./update-deps.sh`
    - If you changed the `serving` code/components and want to deploy the updated code (and the pods) to the cluster: run `./setup.sh`
    - `sample/` has an example function chain (taken from [here](https://knative.dev/docs/eventing/flows/sequence/sequence-with-broker-trigger/))
    - `./setup-chain.sh` installs the function chain, etcd and the supporting broker, and triggers required for setting up and running the chain.
    - `./setup-zipkin.sh` is for creating the zipkin deployment
    - `./setup-etcd.sh` is for setting up etcd
    
