### Golang functions in knative deatils
#### About `appender` function
- created with: `func create -l go -t cloudevents appender` (so handles cloud events)
- can read two env vars when function image is deployed:
```yaml
containers:
- image: atosh502/appender:latest
    env:
    - name: MESSAGE
        value: " - Handled by 2"
    - name: TYPE
        value: "samples.http.mod3"
```
- build func with: `func build` (using registry `docker.io/atosh502`)
- run func with: `func run` 
- on a different terminal (while the function is running) 
    - (optionally set the MESSAGE and TYPE env vars with: `export MESSAGE=" - by appender"`)
    - test with: `go test`

### References
- [Function config with `func.yaml`](https://github.com/knative/func/blob/main/docs/reference/func_yaml.md)
- [Developer's Guide](https://github.com/knative/func/blob/main/docs/function-templates/golang.md)
- [`func` cli reference](https://github.com/knative/func/blob/main/docs/reference/func.md)