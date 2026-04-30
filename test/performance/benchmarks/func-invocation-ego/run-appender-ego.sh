#!/bin/bash

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

source "${SCRIPT_DIR}/../../../../eval/s/env.sh"

# USE_AKS=true skips test-secret.sh (the AKS performance-test-config secret is
# created once by func-chain/setup-benchmark-aks.sh and already includes the
# influxurl/influxtoken keys this benchmark needs).
export USE_AKS="${USE_AKS:-false}"
if [[ "$USE_AKS" != "true" && "$USE_AKS" != "1" ]]; then
  "${SCRIPT_DIR}/../../../../eval/s/test-secret.sh"
fi

timestamp=$(date +%F_%T)

ns=default
if [[ "$USE_AKS" == "true" || "$USE_AKS" == "1" ]]; then
  ARTIFACTS="${SCRIPT_DIR}/run/${timestamp}/aks"
else
  ARTIFACTS="${SCRIPT_DIR}/run/${timestamp}"
fi

mkdir -p "$ARTIFACTS"

# Configurable rates and duration. Set RATES (space-separated) and DURATION
# in the environment to override the defaults.
DURATION="${DURATION:-5m}"
RATES_STR="${RATES:-250 500 750 1000 1250 1500}"
read -ra rates <<< "$RATES_STR"

function run_job() {
  local name=$1
  local file=$2
  local rate=$3

  # cleanup from old runs
  kubectl delete job "$name" -n "$ns" --ignore-not-found=true

  # start the load test and get the logs
  RATE=$rate DURATION=$DURATION envsubst < "$file" | ko apply --sbom=none -Bf -

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

echo "Rates: ${rates[*]}  Duration: $DURATION"
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

# Final teardown so the appender-ego ksvc isn't left deployed after the last rate.
echo "Final teardown..."
"${SCRIPT_DIR}/teardown.sh"
