#!/bin/bash

set -e

RESOURCE_GROUP="lambada"
CLUSTER_NAME="lambada"
LOCATION="eastus"
VM_SIZE="Standard_DC4s_v3"  # SGX-capable VM
NODE_COUNT=4

echo "=========================================="
echo "Setting up AKS cluster with SGX support..."
echo "=========================================="

# Check if Azure CLI is logged in
echo "Checking Azure CLI login status..."
if ! az account show &>/dev/null; then
    echo "Error: Not logged in to Azure CLI. Please run 'az login' first."
    exit 1
fi

echo "Logged in as: $(az account show --query user.name -o tsv)"
echo "Subscription: $(az account show --query name -o tsv)"

# Check if resource group exists, create if not
echo ""
echo "Checking resource group '$RESOURCE_GROUP'..."
if az group show --name "$RESOURCE_GROUP" &>/dev/null; then
    echo "Resource group '$RESOURCE_GROUP' already exists."
else
    echo "Creating resource group '$RESOURCE_GROUP' in '$LOCATION'..."
    az group create --name "$RESOURCE_GROUP" --location "$LOCATION"
fi

# Check if AKS cluster exists, create if not
echo ""
echo "Checking AKS cluster '$CLUSTER_NAME'..."
if az aks show --resource-group "$RESOURCE_GROUP" --name "$CLUSTER_NAME" &>/dev/null; then
    echo "AKS cluster '$CLUSTER_NAME' already exists."
else
    echo "Creating AKS cluster '$CLUSTER_NAME' with SGX support..."
    echo "  VM Size: $VM_SIZE (SGX-capable)"
    echo "  Node Count: $NODE_COUNT"
    echo "  Addon: confcom (confidential computing)"
    echo "  This may take several minutes..."
    az aks create \
        --resource-group "$RESOURCE_GROUP" \
        --name "$CLUSTER_NAME" \
        --node-count "$NODE_COUNT" \
        --generate-ssh-keys \
        --enable-addons confcom \
        --node-vm-size "$VM_SIZE"
fi

# Get credentials for kubectl
echo ""
echo "Getting AKS credentials..."
az aks get-credentials \
    --resource-group "$RESOURCE_GROUP" \
    --name "$CLUSTER_NAME" \
    --overwrite-existing

# Verify connection
echo ""
echo "Verifying cluster connection..."
kubectl cluster-info

echo ""
echo "Cluster nodes:"
kubectl get nodes -o wide

echo ""
echo "=========================================="
echo "AKS cluster setup complete."
echo "kubectl context is now set to: $(kubectl config current-context)"
echo "=========================================="
