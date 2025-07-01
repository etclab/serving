#!/bin/bash

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

timestamp=$(date +%F_%T)
RUN_DIR="$SCRIPT_DIR/run/${timestamp}"

mkdir -p "${RUN_DIR}"

LOG_FILE="${RUN_DIR}/run-func-deploy.log"
exec > >(tee -a "$LOG_FILE") 2>&1

DATA_FILE="${RUN_DIR}/run-func-deploy.data"
echo "pod_name,pod_scheduled_time,pod_ready_time" > "$DATA_FILE"

REPEAT=${REPEAT:-10}

export KO_DOCKER_REPO=docker.io/atosh502

NAMESPACE="func-deployment-benchmark"

kubectl create namespace $NAMESPACE

# deploy the sample function once to ensure the image is pulled and cached
kubectl apply -f ${SCRIPT_DIR}/sample-function.yaml -n $NAMESPACE
kubectl wait --for=condition=ready services.serving.knative.dev --all -n $NAMESPACE --timeout=300s
kubectl wait --for=condition=ready pods --all -n $NAMESPACE --timeout=300s

for (( i=1; i<=REPEAT; i++ ))
    do
        echo "--- Iteration $i ---"

        # delete the function
        echo "Deleting the function..."
        kubectl delete -f ${SCRIPT_DIR}/sample-function.yaml -n $NAMESPACE
        kubectl wait --for=delete pods --all -n $NAMESPACE --timeout=300s

        # deploy the function
        echo "Deploying the function..."
        kubectl apply -f ${SCRIPT_DIR}/sample-function.yaml -n $NAMESPACE
        kubectl wait --for=condition=ready services.serving.knative.dev --all -n $NAMESPACE --timeout=300s
        kubectl wait --for=condition=ready pods --all -n $NAMESPACE --timeout=300s

        # extract the pod name
        POD_NAME=$(kubectl get pods \
            -l serving.knative.dev/service=autoscale \
            -o jsonpath='{.items[0].metadata.name}' \
            -n $NAMESPACE
        )

        echo "Waiting for metrics for pod '$POD_NAME' in namespace '$NAMESPACE'..."
        POD_SCHEDULED_TIME=''
        POD_READY_TIME=''

        while true; do
            # prometheus is exposed at port 3001
            url="http://localhost:3001/api/v1/query"

            scheduled_time_query="kube_pod_status_scheduled_time{namespace=\"$NAMESPACE\",pod=\"$POD_NAME\"}"
            POD_SCHEDULED_TIME=$(curl -s $url --data-urlencode "query=$scheduled_time_query" | jq -r '.data.result[0].value[1]')

            ready_time_query="kube_pod_status_ready_time{namespace=\"$NAMESPACE\",pod=\"$POD_NAME\"}"
            POD_READY_TIME=$(curl -s $url --data-urlencode "query=$ready_time_query" | jq -r '.data.result[0].value[1]')

            # Check if we got both timestamps
            if [[ -n "$POD_SCHEDULED_TIME" && "$POD_SCHEDULED_TIME" != "null" \
                && -n "$POD_READY_TIME" && "$POD_READY_TIME" != "null" ]]; then
                echo "Got the timestamps $POD_SCHEDULED_TIME $POD_READY_TIME"
                break
            else
                echo "    Timestamps not available yet. Retrying in 5 seconds..."
                sleep 5
            fi
        done

        # save to file
        echo "$POD_NAME,$POD_SCHEDULED_TIME,$POD_READY_TIME" >> "$DATA_FILE"

        echo ""
    done

echo "Results saved to: $DATA_FILE"
echo "Logs saved to: $LOG_FILE"

