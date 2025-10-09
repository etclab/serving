#!/bin/bash

az aks nodepool delete --resource-group lambada --cluster-name lambada --name lambada --no-wait

az aks delete --resource-group lambada --name lambada --yes --no-wait

az group delete --name lambada --yes --no-wait --verbose