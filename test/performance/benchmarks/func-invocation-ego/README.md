#### `func-invocation-ego` micro-benchmark

- Measures the time taken by an ego-enlightened function to reply to a message 

- Steps to run `func-invocation-ego` benchmark 
    - add secrets as configmap: `eval/s/test-secret.sh`
    - (from inside `func-invocation-ego` dir)
        - setup: `./setup.sh`
        - run benchmark with `./run-appender-ego.sh`

- What is being measured here?
    - queue-proxy receives the request and removes the encryption (differs if the queue-proxy is a `leader` or a `member`)
    - queue-proxy sends the request to user-container via attested TLS
    - queue-proxy receives the response from user-container, encrypts it with client's (or sink's) pk and send back the response

- How to create required for running tests?
    - (inside `test/performance/benchmarks/func-invocation-ego/`)
    - Run: `go test -v .`

- How to parse the data?
    - Run: `python3 parse.py run/single-fun-enclave-samba/ samba.data "single-function-samba"`
    - (where `samba.data` is the output file to store the data in; `"single-function-samba" is the label describing the data` and `run/single-fun-enclave-samba/` is the folder storing log files)