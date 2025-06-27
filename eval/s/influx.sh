#!/bin/bash

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TEST_DIR="$SCRIPT_DIR/../../test"

# Create an InfluxDB with helm
helm repo add influxdata https://helm.influxdata.com/
kubectl create ns influx
helm upgrade --install -n influx local-influx --set persistence.enabled=true,persistence.size=50Gi influxdata/influxdb2
echo "Admin password"
echo $(kubectl get secret local-influx-influxdb2-auth -o "jsonpath={.data['admin-password']}" --namespace influx | base64 --decode)
echo "Admin token"
echo $(kubectl get secret local-influx-influxdb2-auth -o "jsonpath={.data['admin-token']}" --namespace influx | base64 --decode)

kubectl wait --for=condition=Ready pods --all -n influx --timeout=300s

screen -S influx-port-forward -X quit
# Forward the InfluxDB service to your laptop if you want to access the UI:
screen -dmS influx-port-forward kubectl port-forward -n influx svc/local-influx-influxdb2 9080:80

# Set up the expected influxdb config
export INFLUX_URL=http://localhost:9080
export INFLUX_TOKEN=$(kubectl get secret local-influx-influxdb2-auth -o "jsonpath={.data['admin-token']}" --namespace influx | base64 --decode)

sleep 5
# Run the script to initialize the Organization + Buckets in InfluxDB
$TEST_DIR/performance/visualization/setup-influx-db.sh