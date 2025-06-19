#!/bin/bash

# sets up sgx device plugin and related components on minikube

# daemonset to advertise EPC capacity to the API server
kubectl delete -k https://github.com/intel/intel-device-plugins-for-kubernetes/deployments/sgx_plugin/overlays/epc-register/?ref=v0.27.1

# sgx admission webhook
kubectl delete -k https://github.com/intel/intel-device-plugins-for-kubernetes/deployments/sgx_admissionwebhook/overlays/default-with-certmanager/?ref=v0.27.1