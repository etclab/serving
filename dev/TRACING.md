- Apply `setup-zipkin.sh` with: `./dev/setup-zipkin.sh`
- Configure zipkin in file `config/core/configmaps/tracing.yaml`
```
data:
  backend: "zipkin"
  zipkin-endpoint: "http://zipkin.zipkin-monitoring.svc.cluster.local:9411/api/v2/spans"
  sample-rate: "1"
  debug: "true"
```
- Run: `kubectl proxy` serving metrics at `8001`
- Forward ports in vscode if necessary
- Goto url to access zipkin dashboard: 
```
http://localhost:8001/api/v1/namespaces/<namespace>/services/zipkin:9411/proxy/zipkin/
```

- References
    - https://knative.dev/docs/serving/accessing-traces/#configuring-traces
    - https://knative.dev/docs/eventing/accessing-traces/