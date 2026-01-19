#!/bin/bash

# wait for cert-manager CRDs to be ready (installed by dev/setup.sh)
echo "Waiting for cert-manager CRDs to be ready..."
kubectl wait --for=condition=Established crd/certificates.cert-manager.io --timeout=60s
kubectl wait --for=condition=Established crd/issuers.cert-manager.io --timeout=60s

# sets up sgx device plugin and related components on minikube

# why only v0.27.1? works
# daemonset to advertise EPC capacity to the API server
kubectl apply -k https://github.com/intel/intel-device-plugins-for-kubernetes/deployments/sgx_plugin/overlays/epc-register/?ref=v0.27.1

# sgx admission webhook
kubectl apply -k https://github.com/intel/intel-device-plugins-for-kubernetes/deployments/sgx_admissionwebhook/overlays/default-with-certmanager/?ref=v0.27.1