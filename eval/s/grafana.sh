#!/bin/bash


SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
YAML_DIR="$SCRIPT_DIR/../y"
NAMESPACE=grafana 

kubectl create namespace $NAMESPACE

kubectl apply -f $YAML_DIR/grafana.yaml --namespace=$NAMESPACE

kubectl wait --for=condition=Ready pods --all -n $NAMESPACE --timeout=300s

screen -S grafana-port-forward -X quit
# Forward the InfluxDB service to your laptop if you want to access the UI:
screen -dmS grafana-port-forward kubectl port-forward service/grafana 3000:3000 --namespace=$NAMESPACE

