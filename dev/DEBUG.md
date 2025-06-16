- How to view the key-value being stored in etcd
    - `etcd-0` pod doesn't include bash or sh but includes the `etcdctl` client, so
    - Use `kubectl exec -it -n knative-serving etcd-0 -- etcdctl get "" --prefix --keys-only` to display the current keys

- How to verify lease is being transferred or if a pod goes down another pod becomes the leader?
    - Use `kubectl get lease` to get the current leases in `default` namespace
    - Use `kubectl delete lease <lease-name>` to delete the lease
    - After deleting the lease another pod should acquire the lease and become the master

- How do I scale number of replicas of a function?
    - `kn service update <service-name> --scale-min <N>`

- How do I add a static function chain to `etcd`?
    - `kubectl exec -it -n knative-serving etcd-0 -- etcdctl put functionChainStatic/0 first/second/third` 

- How to view traces with zipkin?
    - enable the commented fields under `data` (`backend`, `zipkin-endpoint`, `sample-rate`, and `debug`) in file: `config/core/configmaps/tracing.yaml`
    - deploy zipkin to namespace: `zipkin-monitoring` with: `./dev/setup_zipkin.sh`
    - run: `kubectl proxy` in a terminal to access the k8s api server at port: `8001`
    - port forward `8001` (if developing using vscode)
    - access zipkin using browser at url: `http://localhost:8001/api/v1/namespaces/zipkin-monitoring/services/zipkin:9411/proxy/zipkin/`