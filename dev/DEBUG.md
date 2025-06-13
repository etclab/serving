- How to view the key-value being stored in etcd
    - `etcd-0` pod doesn't include bash or sh but includes the `etcdctl` client, so
    - Use `kubectl exec -it -n knative-serving etcd-0 -- etcdctl get "" --prefix --keys-only` to display the current keys

- How to verify lease is being transferred or if a pod goes down another pod becomes the leader?
    - Use `kubectl get lease` to get the current leases in `default` namespace
    - Use `kubectl delete lease <lease-name>` to delete the lease
    - After deleting the lease another pod should acquire the lease and become the master

- How do I scale number of replicas of a function?
    - `kn service update <service-name> --scale-min <N>`