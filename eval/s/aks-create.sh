#!/bin/bash

az group create --name lambada --location eastus

# Standard_DC8s_v3
az aks create -g lambada --name lambada --generate-ssh-keys --enable-addons confcom --node-vm-size Standard_DC4s_v3

# node pool name = lambada
# az aks nodepool add --name lambada --resource-group lambada --cluster-name lambada  --node-vm-size Standard_DC4s_v3 --node-count 3

az aks get-credentials --resource-group lambada --name lambada --overwrite-existing