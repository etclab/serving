#### How to deploy intel sgx device plugin inside minikube cluster?
- while running on `shs2` with SGX and SGX FLC enabled [more here](https://docs.edgeless.systems/ego/getting-started/troubleshoot#hardware)
- ensure `docker` driver is used: `minikube config set driver docker`
- create minikube with `./dev/setup-minikube.sh`
- install knative and cert-manager with: `./dev/setup.sh` (intel sgx plugin requires `cert-manager` to be installed)
- install the device plugin and epc register with: `./dev/sgx/deploy-sgx-plugin.sh` (to uninstall use: `./dev/sgx/remove-sgx-plugin.sh`)

#### How to verify intel sgx device is working?
- ssh into minikube and list `/dev/sgx*` devices
```bash
$ minikube ssh
docker@knative:~$ ls -al /dev/sgx*
crw-rw---- 1 root systemd-resolve 10, 125 Jun 19 02:33 /dev/sgx_enclave
crw-rw---- 1 root            1062 10, 126 Jun 19 02:33 /dev/sgx_provision
crw-rw---- 1 root systemd-resolve 10, 124 Jun 19 02:33 /dev/sgx_vepc
docker@knative:~$ 
```
- check if `intel-sgx-plugin-xxxxx` and `sgx-node-init-xxxxx` pods are deployed in `kube-system` namespace
```bash
$ kubectl get pods -n kube-system
NAME                              READY   STATUS    RESTARTS      AGE
...
intel-sgx-plugin-dfsp4            1/1     Running   0             10m
...
sgx-node-init-sc2tk               1/1     Running   0             10m
...
```
- below output confirms that sgx plugin was deployed and detected if enclave/epc is allocated to the k8s node: `knative`
```bash
$ kubectl describe node knative | grep sgx.intel.com
                    sgx.intel.com/capable=true
  sgx.intel.com/enclave:    110
  sgx.intel.com/epc:        120840192
  sgx.intel.com/provision:  110
  sgx.intel.com/enclave:    110
  sgx.intel.com/epc:        120840192
  sgx.intel.com/provision:  110
  sgx.intel.com/enclave    0            0
  sgx.intel.com/epc        0            0
  sgx.intel.com/provision  0            0
```

#### How to configure a local PCCS for your host?
- Use [docker image provided by edgeless](https://docs.edgeless.systems/ego/reference/attest#your-own-pccs)
- Run PCCS with: `docker run -p 8081:8081 --name pccs -d ghcr.io/edgelesssys/pccs`
- Ensure `pccs_url` in `/etc/sgx_default_qcnl.conf` points to: `https://localhost:8081/sgx/certification/v4/`

### References
- [intel sgx device plugin for kubernetes](https://github.com/intel/intel-device-plugins-for-kubernetes/blob/main/cmd/sgx_plugin/README.md)
    - the first installation (Installation using the Operator) doesn't work, in fact any installation using `node-feature-discovery` doesn't work
    - the second approach of second installation ([Installation Using kubectl](https://github.com/intel/intel-device-plugins-for-kubernetes/blob/main/cmd/sgx_plugin/README.md#installation-using-kubectl) - the one that uses the daemonset instead of NFD) works, see file: `./deploy-sgx-plugin.sh` for exact commands 
- [emojivoto](https://github.com/edgelesssys/emojivoto)
    - simply deploying the command mentioned in the [above installation doc](https://github.com/intel/intel-device-plugins-for-kubernetes/blob/main/cmd/sgx_plugin/README.md#installation-using-kubectl) doesn't work unless a specific version of the kustomize yaml is specified (`0.32.1`); this was taken from how cluster for `emojivoto` demo app was setup


