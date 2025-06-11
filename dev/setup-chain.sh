#!/bin/bash

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"

$SCRIPT_DIR/setup-etcd.sh

kubectl apply -f $SCRIPT_DIR/yaml/lease-roles.yaml

kubectl apply -f $SCRIPT_DIR/sample/