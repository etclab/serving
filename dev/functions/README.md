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


#### How to convert the above `appender` function to run on enclaves using `ego`?
- Knative function by default consist of only a single function (like `Handle()`) and have no `main()` entrypoint/method
- so we copy the Knative function into a `main` scaffold (within directory `scaffold/f`); the `main() scaffold` also includes the `Dockerfile` and the `enclave.json` files
- `Dockerfile` - build, signs, and packages the function binary in a docker file for running
- `enclave.json` - has ego specific configs like heapsize, private key and env vars; `env` is important because by default `ego` will not forward the environment variables added by Knative to the function running inside the enclave
- ensure rsa `private.pem` and `public.pem` exists in the `dev/functions` directory; [see Appendix](#appendix)
- command to build the docker image and push it to registry (from within scaffold folder): 
```bash
DOCKER_BUILDKIT=1 docker build --secret id=signingkey,src=../private.pem --target deploy -t "atosh502/appender-ego-sim" --push .
```
- Or use the following script from `functions` directory to build a docker image for `appender` function
```bash
# ./build-function.sh <scaffold-dir> <function-dir>
./build-function.sh ./scaffold ./appender
```
- (if prompted for docker registry url use the logged in user's username: `docker.io/<username>`)

### References
- [Function config with `func.yaml`](https://github.com/knative/func/blob/main/docs/reference/func_yaml.md)
- [Developer's Guide](https://github.com/knative/func/blob/main/docs/function-templates/golang.md)
- [`func` cli reference](https://github.com/knative/func/blob/main/docs/reference/func.md)

### Appendix
- Backup commands
    - Run `ego-`enlightened Knative function using: `docker run --device /dev/sgx_enclave --device /dev/sgx_provision atosh502/appender-ego:latest`
    - Create a public/private RSA key pair with: 
        - for `private.pem`: `openssl genrsa -out private.pem 3072`
        - for `public.pem`: `openssl rsa -in private.pem -outform PEM -pubout -out public.pem`