#!/bin/bash

# Port forward Zipkin service for trace collection
# Run this in the background before running benchmarks, or use ensure_zipkin_port_forward function

ZIPKIN_NAMESPACE="${ZIPKIN_NAMESPACE:-zipkin-monitoring}"
ZIPKIN_LOCAL_PORT="${ZIPKIN_LOCAL_PORT:-9411}"

kubectl port-forward deployment/zipkin -n "$ZIPKIN_NAMESPACE" "${ZIPKIN_LOCAL_PORT}:9411"
