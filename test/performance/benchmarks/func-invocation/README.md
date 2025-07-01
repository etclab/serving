#### `func-invocation` micro-benchmark

- Measures the time taken by a function to reply to a message 

- Steps to run `func-invocation` benchmark 
    - add secrets as configmap: `eval/s/test-secret.sh`
    - (from inside `func-invocation` dir)
        - setup: `./setup.sh`
        - run benchmark with `./run-autoscale.sh`

- What is being measured here?
    - queue-proxy receives the request and removes the encryption (differs if the queue-proxy is a `leader` or a `member`)
    - queue-proxy sends the request to user-container via attested TLS
    - queue-proxy receives the response from user-container, encrypts it with client's (or sink's) pk and send back the response