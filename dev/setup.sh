#!/bin/bash

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

export KO_DOCKER_REPO="${KO_DOCKER_REPO:-docker.io/atosh502}"

# cert-manager related
printf "\n\nInstalling cert-manager...\n"
kubectl apply -f "$REPO_ROOT/third_party/cert-manager-latest/cert-manager.yaml"
kubectl wait --for=condition=Established --all crd
kubectl wait --for=condition=Available -n cert-manager --all deployments

# knative serving related
printf "\n\nInstalling Knative Serving...\n"
ko apply --selector knative.dev/crd-install=true -Rf "$REPO_ROOT/config/core/"
kubectl wait --for=condition=Established --all crd

ko apply -Rf "$REPO_ROOT/config/core/"

# Optional steps
# Run post-install job to set up a nice sslip.io domain name.  This only works
# if your Kubernetes LoadBalancer has an IPv4 address.
ko delete -f "$REPO_ROOT/config/post-install/default-domain.yaml" --ignore-not-found
ko apply -f "$REPO_ROOT/config/post-install/default-domain.yaml"

printf "\n\nInstalling courier...\n"
kubectl apply -f "$REPO_ROOT/third_party/kourier-latest/kourier.yaml"

kubectl patch configmap/config-network \
  -n knative-serving \
  --type merge \
  -p '{"data":{"ingress.class":"kourier.ingress.networking.knative.dev"}}'

"$REPO_ROOT/dev/setup-etcd.sh"

kubectl apply -f "$REPO_ROOT/dev/yaml/lease-roles.yaml"

# adds a sample function chain to
kubectl wait --for=condition=ready pod/etcd-0 -n knative-serving --timeout=90s
kubectl exec -it -n knative-serving etcd-0 -- etcdctl put functionChainStatic/0 first/second/third

kubectl rollout restart deployment -n knative-serving