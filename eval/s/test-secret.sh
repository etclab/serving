#!/bin/bash

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

source ${SCRIPT_DIR}/env.sh

kubectl delete secret performance-test-config -n default --ignore-not-found=true
kubectl create secret generic performance-test-config -n default \
  --from-literal=influxurl="${INFLUX_URL}" \
  --from-literal=influxtoken="${INFLUX_TOKEN}" \
  --from-literal=jobname="${JOB_NAME}" \
  --from-literal=buildid="${BUILD_ID}" \
  --from-literal=systemnamespace="${SYSTEM_NAMESPACE}"