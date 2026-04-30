#!/bin/bash

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

source "${SCRIPT_DIR}/utils.sh"

# Supported variants: knative, efunction, leader-efunction, member-efunction
VARIANT=${VARIANT:-efunction}

# Set USE_AKS=true when running on AKS (skips local QCNL volume mount)
USE_AKS=${USE_AKS:-false}

# Defaults (override in case block as needed)
MIN_SCALE="1"
MAX_SCALE="1"
USE_SGX=true
CONTAINER_IMAGE="docker.io/atosh502/appender-ego:bench"

# Map variant to configuration
case "$VARIANT" in
    knative)
        SERVICE_NAME="appender"
        CONTAINER_IMAGE="docker.io/atosh502/appender:bench"
        USE_SGX=false
        ;;
    efunction)
        SERVICE_NAME="appender-ego"
        ;;
    leader-efunction)
        SERVICE_NAME="appender-ego-leader"
        ;;
    member-efunction)
        SERVICE_NAME="appender-ego-member"
        MIN_SCALE="2"
        MAX_SCALE="2"
        ;;
    *)
        echo "Error: Unknown variant '$VARIANT'"
        echo "Supported variants: knative, efunction, leader-efunction, member-efunction"
        exit 1
        ;;
esac

# Use BENCHMARK_TIMESTAMP if set (from run-benchmark.sh), otherwise create new timestamp
timestamp=${BENCHMARK_TIMESTAMP:-$(date +%F_%T)}

# For AKS runs, store artifacts in a separate "aks" subdirectory
if [[ "$USE_AKS" == "true" ]]; then
    RUN_DIR="$SCRIPT_DIR/run/${timestamp}/aks/${VARIANT}"
else
    RUN_DIR="$SCRIPT_DIR/run/${timestamp}/${VARIANT}"
fi

mkdir -p "${RUN_DIR}"

# Generate the YAML using heredoc
FUNCTION_YAML="${RUN_DIR}/${VARIANT}.yaml"

cat > "$FUNCTION_YAML" <<EOF
# Generated from run-func.sh for variant: $VARIANT
apiVersion: serving.knative.dev/v1
kind: Service
metadata:
  name: $SERVICE_NAME
spec:
  template:
    metadata:
      annotations:
        autoscaling.knative.dev/minScale: "$MIN_SCALE"
        autoscaling.knative.dev/maxScale: "$MAX_SCALE"
        autoscaling.knative.dev/targetBurstCapacity: "-1"
    spec:
EOF

# Add QCNL volume only for non-AKS environments (local minikube)
if [[ "$USE_AKS" != "true" ]]; then
cat >> "$FUNCTION_YAML" <<EOF
      volumes:
      - name: sgx-default-qcnl-local-volume
        configMap:
          name: sgx-default-qcnl-local
EOF
fi

cat >> "$FUNCTION_YAML" <<EOF
      containers:
      - image: $CONTAINER_IMAGE
        readinessProbe:
          periodSeconds: 1
EOF

# Add SGX resources if needed
if $USE_SGX; then
cat >> "$FUNCTION_YAML" <<EOF
        resources:
          limits:
            sgx.intel.com/epc: "512Ki"
            sgx.intel.com/enclave: 1
            sgx.intel.com/provision: 1
EOF
fi

# Add QCNL volumeMount only for non-AKS environments (local minikube)
if [[ "$USE_AKS" != "true" ]]; then
cat >> "$FUNCTION_YAML" <<EOF
        volumeMounts:
          - name: sgx-default-qcnl-local-volume
            mountPath: /etc/sgx_default_qcnl.conf
            subPath: sgx_default_qcnl.conf
EOF
fi

# Add the rest of the container spec
cat >> "$FUNCTION_YAML" <<EOF
      containerConcurrency: 0
EOF

echo "Running with variant: $VARIANT"
echo "Generated YAML file: $FUNCTION_YAML"
echo "Service name: $SERVICE_NAME"

LOG_FILE="${RUN_DIR}/${VARIANT}.log"
exec > >(tee -a "$LOG_FILE") 2>&1

DATA_FILE="${RUN_DIR}/${VARIANT}.data"
echo "pod_name,pod_scheduled_time,pod_ready_time" > "$DATA_FILE"

REPEAT=${REPEAT:-2}

export KO_DOCKER_REPO="${KO_DOCKER_REPO:-"docker.io/atosh502"}"

NAMESPACE="default"

# Cleanup function to delete the deployed function on exit
cleanup() {
    echo "Cleaning up: deleting function..."
    delete_function "$FUNCTION_YAML" $NAMESPACE 2>/dev/null || true
}
trap cleanup EXIT

kubectl create namespace $NAMESPACE

# deploy the sample function once to ensure the image is pulled and cached
echo "Deploying the function for the first time..."
deploy_function "$FUNCTION_YAML" $NAMESPACE

POD_NAME=''

for (( i=1; i<=REPEAT; i++ ))
    do
        echo "--- Iteration $i ---"

        # delete the function
        echo "Deleting the function..."
        delete_function "$FUNCTION_YAML" $NAMESPACE

        sleep 10

        # For leader and member variants, clean up lease and etcd
        if [[ "$VARIANT" == "leader-efunction" || "$VARIANT" == "member-efunction" ]]; then
            # delete the lease
            echo "Deleting the lease..."
            kubectl delete leases.coordination.k8s.io --all -n $NAMESPACE
            sleep 1

            # delete all keys in etcd - so that we don't fetch previous leader function pks
            echo "Deleting all keys in etcd..."
            kubectl exec -it -n knative-serving etcd-0 -- etcdctl del "" --prefix
            sleep 1
        fi

        # deploy the function
        echo "Deploying the function..."
        deploy_function "$FUNCTION_YAML" $NAMESPACE

        # extract the pod name based on variant
        if [[ "$VARIANT" == "member-efunction" ]]; then
            # For member variant, find the member pod (not the leader)
            LEADER_POD_NAME=$(kubectl get leases.coordination.k8s.io \
                -o jsonpath='{.items[0].spec.holderIdentity}' \
                -n $NAMESPACE
            )
            echo "Leader pod name: $LEADER_POD_NAME"

            ALL_PODS=($(kubectl get pods \
                -n $NAMESPACE \
                -l serving.knative.dev/service=${SERVICE_NAME} \
                -o jsonpath='{.items[*].metadata.name}'
                )
            )

            MEMBER_POD_NAMES=()
            for pod in "${ALL_PODS[@]}"; do
                if [[ "$pod" != "$LEADER_POD_NAME" ]]; then
                    MEMBER_POD_NAMES+=("$pod")
                fi
            done

            POD_NAME="${MEMBER_POD_NAMES[0]}"
            echo "Member pod name: $POD_NAME"
        else
            # For other variants, get the first pod
            POD_NAME=$(kubectl get pods \
                -l serving.knative.dev/service=${SERVICE_NAME} \
                -o jsonpath='{.items[0].metadata.name}' \
                -n $NAMESPACE
            )
        fi

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

# Cleanup is handled by the trap on EXIT
