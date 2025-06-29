#!/bin/bash

export KO_DOCKER_REPO='docker.io/atosh502'

kubectl delete -f ./third_party/cert-manager-latest/cert-manager.yaml --wait --ignore-not-found

ko delete --selector knative.dev/crd-install=true -Rf ./config/core/ --wait --ignore-not-found

ko delete -Rf ./config/core/ --wait --ignore-not-found

ko delete -f config/post-install/default-domain.yaml --ignore-not-found --wait

kubectl delete -f ./third_party/kourier-latest/kourier.yaml --wait --ignore-not-found
