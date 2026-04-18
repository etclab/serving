#!/bin/bash

az aks delete --resource-group lambada --name lambada --yes --no-wait

az group delete --name lambada --yes --no-wait --verbose