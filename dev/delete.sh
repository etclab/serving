#!/bin/bash

kubectl delete -f dev/sample/
kubectl delete -f dev/yaml/lease-roles.yaml
kubectl delete leases --all -n default