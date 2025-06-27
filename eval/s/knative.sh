#!/bin/bash

export KO_DOCKER_REPO='docker.io/atosh502'

# cert-manager related
printf "\n\nInstalling cert-manager...\n"
kubectl apply -f ./third_party/cert-manager-latest/cert-manager.yaml
kubectl wait --for=condition=Established --all crd
kubectl wait --for=condition=Available -n cert-manager --all deployments

# knative serving related
printf "\n\nInstalling Knative Serving...\n"
ko apply --selector knative.dev/crd-install=true -Rf ./config/core/
kubectl wait --for=condition=Established --all crd

ko apply -Rf ./config/core/

# Optional steps
# Run post-install job to set up a nice sslip.io domain name.  This only works
# if your Kubernetes LoadBalancer has an IPv4 address.
ko delete -f config/post-install/default-domain.yaml --ignore-not-found
ko apply -f config/post-install/default-domain.yaml

printf "\n\nInstalling courier...\n"
kubectl apply -f ./third_party/kourier-latest/kourier.yaml

kubectl patch configmap/config-network \
  -n knative-serving \
  --type merge \
  -p '{"data":{"ingress.class":"kourier.ingress.networking.knative.dev"}}'

kubectl rollout restart deployment -n knative-serving