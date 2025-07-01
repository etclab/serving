#!/bin/bash

# this script deploys kube-prometheus stack in the cluster
# including prometheus, grafana and `kube-state-metrics`

helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
helm repo update

RELEASE_NAME=kube-prometheus
NAMESPACE=monitoring

helm uninstall $RELEASE_NAME --wait --ignore-not-found --namespace $NAMESPACE

helm install $RELEASE_NAME prometheus-community/kube-prometheus-stack \
    --wait \
    --namespace $NAMESPACE \
    --create-namespace

kubectl wait --for=condition=Ready pods --all -n $NAMESPACE --timeout=300s

# expose grafana port 
screen -S grafana-port-forward -X quit
screen -dmS grafana-port-forward kubectl port-forward service/kube-prometheus-grafana 3000:80 --namespace=$NAMESPACE

# expose prometheus port
screen -S prometheus-port-forward -X quit
screen -dmS prometheus-port-forward kubectl port-forward service/kube-prometheus-kube-prome-prometheus 3001:9090 --namespace=$NAMESPACE

# expose kube-state-metrics port
screen -S ksm-port-forward -X quit
screen -dmS ksm-port-forward kubectl port-forward service/kube-prometheus-kube-state-metrics 3002:8080 --namespace=$NAMESPACE