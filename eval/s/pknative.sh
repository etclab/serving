#!/bin/bash

export KO_DOCKER_REPO='docker.io/atosh502'

# cert-manager related
printf "\n\nInstalling cert-manager...\n"
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.18.0/cert-manager.yaml
kubectl wait --for=condition=Established --all crd
kubectl wait --for=condition=Available -n cert-manager --all deployments

# Knative serving
printf "\n\nInstalling Knative Serving...\n"
kubectl apply \
  -f https://github.com/knative/serving/releases/download/knative-v1.18.1/serving-crds.yaml
kubectl apply \
  -f https://github.com/knative/serving/releases/download/knative-v1.18.1/serving-core.yaml
kubectl wait --for=condition=Available -n knative-serving --all deployments

# networking layer
printf "\n\nInstalling courier...\n"
kubectl apply \
  -f https://github.com/knative/net-kourier/releases/download/knative-v1.18.0/kourier.yaml
kubectl patch configmap/config-network \
  --namespace knative-serving \
  --type merge \
  --patch '{"data":{"ingress-class":"kourier.ingress.networking.knative.dev"}}'
kubectl wait --for=condition=Available -n kourier-system --all deployments


# magic dns
kubectl apply \
  -f https://github.com/knative/serving/releases/download/knative-v1.18.1/serving-default-domain.yaml

# Knative eventing
kubectl apply \
  -f https://github.com/knative/eventing/releases/download/knative-v1.18.1/eventing-crds.yaml
kubectl apply \
  -f https://github.com/knative/eventing/releases/download/knative-v1.18.1/eventing-core.yaml
kubectl wait --for=condition=Available -n knative-eventing --all deployments

# install a channel (messaging layer)
kubectl apply \
  -f https://github.com/knative-extensions/eventing-kafka-broker/releases/download/knative-v1.18.0/eventing-kafka-controller.yaml
kubectl apply \
  -f https://github.com/knative-extensions/eventing-kafka-broker/releases/download/knative-v1.18.0/eventing-kafka-channel.yaml
kubectl wait --for=condition=Available -n knative-eventing --all deployments

# install a broker layer
kubectl apply \
  -f https://github.com/knative-extensions/eventing-kafka-broker/releases/download/knative-v1.18.0/eventing-kafka-broker.yaml
kubectl wait --for=condition=Available -n knative-eventing --all deployments

# TODO: there are some issues with sink/source deployment
# # kafka sink
# kubectl apply \
#   -f https://github.com/knative-extensions/eventing-kafka-broker/releases/download/knative-v1.18.0/eventing-kafka-sink.yaml
# kubectl wait --for=condition=Available -n knative-eventing --all deployments


# # kafka source
# kubectl apply \
#   -f https://github.com/knative-extensions/eventing-kafka-broker/releases/download/knative-v1.18.0/eventing-kafka-source.yaml
# kubectl wait --for=condition=Available -n knative-eventing --all deployments
