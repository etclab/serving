#!/bin/bash

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

source "${SCRIPT_DIR}/../../../../eval/s/env.sh"

timestamp=$(date +%F_%T)

ns=default
ARTIFACTS="${SCRIPT_DIR}/run/${timestamp}"

mkdir -p "$ARTIFACTS"

function run_job() {
  local name=$1
  local file=$2
  local rate=$3

  # cleanup from old runs
  kubectl delete job "$name" -n "$ns" --ignore-not-found=true

  # start the load test and get the logs
  RATE=$rate envsubst < "$file" | ko apply --sbom=none -Bf -

  # sleep a bit to make sure the job is created
  sleep 5

  # Follow logs to wait for job termination
  kubectl wait --for=condition=ready -n "$ns" pod --selector=job-name="$name" --timeout=-1s
  kubectl logs -n "$ns" -f "job.batch/$name"

  # Dump logs to a file to upload it as CI job artifact
  kubectl logs -n "$ns" "job.batch/$name" >"$ARTIFACTS/$rate.log"

  # clean up
  kubectl delete "job/$name" -n "$ns" --ignore-not-found=true
  kubectl wait --for=delete "job/$name" --timeout=60s -n "$ns"
}

rates=(250 500 750 1000 1250 1500)
for rate in "${rates[@]}"; do
  echo "Running func-invocation-ego-appender.yaml with rate: $rate"

  # cleanup
  "${SCRIPT_DIR}/teardown.sh"

  # setup
  "${SCRIPT_DIR}/pre-config.sh"
  "${SCRIPT_DIR}/setup.sh"

  # run
  run_job func-invocation-appender-ego "${SCRIPT_DIR}/func-invocation-ego-appender.yaml" $rate
done
