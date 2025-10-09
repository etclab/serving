```bash
# -> start - setup an aks cluster with 3 nodes
# cluster name = lambada
az aks create -g lambada --name lambada --generate-ssh-keys --enable-addons confcom --node-vm-size Standard_DC4s_v3

# node pool name = lambada
az aks nodepool add --name lambada --resource-group lambada --cluster-name lambada  --node-vm-size Standard_DC4s_v3 --node-count 3

az aks get-credentials --resource-group lambada --name lambada
# -> end

az aks nodepool delete --resource-group lambada --cluster-name lambada --name lambada

az aks delete --resource-group lambada --name lambada --yes

az group delete --name lambada

```