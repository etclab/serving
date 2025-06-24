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

#### How to configure PCCS url for your pods?
- By default the `pccs_url` in pods points to the `https://localhost:8081/sgx/certification/v4/`. Since we're running pods inside a minikube cluster the localhost url isn't accessible, so we update it to: `https://host.minikube.internal:8081/sgx/certification/v4/`. This allows pods to access the pccs service running on host at `localhost:8081`.
- Run: `./update-pccs-url.sh` to load a configmap that updates the `pccs_url` to `https://host.minikube.internal:8081/sgx/certification/v4/` and `use_secure_cert` to `false` in file: `/etc/sgx-default-qcnl.conf` in the pods

#### How to configure the local PCCS (`shs2`) for mininikube?
- Check if `pccs.service` is running with: `sudo systemctl status pccs`
- Check logs with: `sudo journalctl -u pccs.service -f`
- Since we're using `minikube` the default address host's `pccs.service` listen's on won't be reachable from `minikube` pods even after using the `host.minikube.internal:8081` url. 
- To enable `pccs.service` to accept requests from `minikube` pods, we need to make it listen on the `minikube` host’s IP address.
- First, find the ip of `host.minikube.internal` by going into minikube node with `minikube ssh` and running: `ping host.minikube.internal`. Let's say the ip is `192.168.58.1`
- Next, set this ip (`192.168.58.1`) instead of `127.0.0.1` in the `hosts` key of file: `/opt/intel/sgx-dcap-pccs/config/default.json`. Finally, restart the `pccs.service` with: `sudo systemctl status pccs`
- Alternatives: 
    - [docker image of PCCS provided by edgeless](https://docs.edgeless.systems/ego/reference/attest#your-own-pccs)
    - running a separate `pccs.service` at a different port for `minikube` pods

### References
- [intel sgx device plugin for kubernetes](https://github.com/intel/intel-device-plugins-for-kubernetes/blob/main/cmd/sgx_plugin/README.md)
    - the first installation (Installation using the Operator) doesn't work, in fact any installation using `node-feature-discovery` doesn't work
    - the second approach of second installation ([Installation Using kubectl](https://github.com/intel/intel-device-plugins-for-kubernetes/blob/main/cmd/sgx_plugin/README.md#installation-using-kubectl) - the one that uses the daemonset instead of NFD) works, see file: `./deploy-sgx-plugin.sh` for exact commands 
- [emojivoto](https://github.com/edgelesssys/emojivoto)
    - simply deploying the command mentioned in the [above installation doc](https://github.com/intel/intel-device-plugins-for-kubernetes/blob/main/cmd/sgx_plugin/README.md#installation-using-kubectl) doesn't work unless a specific version of the kustomize yaml is specified (`0.32.1`); this was taken from how cluster for `emojivoto` demo app was setup


