#### `func-deployment` micro-benchmark

- Measures the time taken to deploy a function
    - A function is deployed as pod with two containers: queue-proxy & user-container
    - How do I ensure these two containers are cached locally?
        - maybe run one dummy test that pulls the containers
    - For pods measure the:
        - Start time at: `PodScheduled` (metric name: `kube_pod_status_scheduled_time`)
        - End time at: `Ready` (metric name: `kube_pod_status_ready_time`)
    - For containers within a pod measure the:
        - Start time at: `PodScheduled` (metric name: `kube_pod_status_scheduled_time`)
	    - End time at: `ContainersReady` (metric name: `kube_pod_status_container_ready_time`)

- Create an image with fixed tag and avoid pulling new images everytime
    - `KO_DOCKER_REPO=docker.io/atosh502 ko build ./test/test_images/autoscale -t func-deployment-benchmark --push --sbom none`
    - image: `docker.io/atosh502/autoscale-c163c422b72a456bad9aedab6b2d1f13:func-deployment-benchmark`

- How to run this benchmark?
    - Ensure the `kube-prometheus-stack` is deployed (`eval/s/kube-prometheus.sh`) as we depend on `kube-state-metrics` and `prometheus`
    - Run benchmark measuring the deployment time of `sample-function.yaml` using: `REPEAT=50 ./run-func-deploy.sh`
    - Results and logs will be inside `./run/{timestamp}` folder
    - Transform results with: `python3 transform.py ./run/{timestamp}/run-func-deploy.data` out.data
    - Create a file with min,max,mean,std of time taken to deploy a pod