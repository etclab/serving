#!/bin/bash

# kn quickstart minikube

# minikube profile knative

minikube stop -p knative

minikube delete -p knative

minikube start -p knative \
    --driver=docker \
    --memory=32768 \
    --cpus=16 \
    --container-runtime=containerd \
    --kubernetes-version=v1.33.1 \
    --bootstrapper=kubeadm \
    --extra-config=kubelet.authentication-token-webhook=true \
    --extra-config=kubelet.authorization-mode=Webhook \
    --extra-config=scheduler.bind-address=0.0.0.0 \
    --extra-config=controller-manager.bind-address=0.0.0.0

minikube addons disable metrics-server -p knative

minikube profile knative

minikube tunnel -p knative