# based on: https://github.com/edgelesssys/ego/blob/e45d00fce7edfc12b741e41cc474c2ef4c48699f/samples/reproducible_build/Dockerfile
ARG egover=1.7.0

# Use this to build an executable for Ubuntu 24.04
FROM ghcr.io/edgelesssys/ego/build-base:v${egover} AS build

ARG egover
WORKDIR /app
COPY . .

# Install required packages
# These are cached in the build-base image. Don't run `apt-get update` or
# you may get other package versions and the build won't be reproducible.
RUN apt-get install -y --no-install-recommends \
  build-essential \
  ca-certificates \
  git \
  wget

# Download and install further requirements (if any)
#
# Make sure that these stay the same, e.g., don't use "latest", but fixed versions.
#
# Avoid installing packages via apt here. This may change the version of already
# installed dependencies and may influence the final binary. If not using apt isn't
# feasible, consider building a Docker image that gathers all required apt packages
# and serves as a stable base.

# Download and install EGo
# Use --force-depends to ignore SGX dependencies, which aren't required for building
RUN egodeb=ego_${egover}_amd64_ubuntu-$(grep -oP 'VERSION_ID="\K[^"]+' /etc/os-release).deb \
  && wget https://github.com/edgelesssys/ego/releases/download/v${egover}/${egodeb} \
  && dpkg -i --force-depends ${egodeb}

# Build your app
RUN ego-go build -o queue -trimpath ./cmd/queue 

# copy the enclave.json file to root so it can find `queue` binary
COPY ./dev/queue-proxy/enclave.json .

# sign the binary
RUN --mount=type=secret,id=signingkey,dst=private.pem,required=true \
  ego sign enclave.json
RUN ego bundle queue 

# Use the deploy target if you want to deploy your app as a Docker image
FROM ghcr.io/edgelesssys/ego-deploy:v${egover} AS deploy

# taken from here: https://github.com/edgelesssys/ego/blob/e45d00fce7edfc12b741e41cc474c2ef4c48699f/pccs/Dockerfile#L97-L106
# Knative requires a non-root user to run the container
# ARG USER=queue
# ARG UID=777
# RUN useradd -M -U -u ${UID} -r ${USER} -s /bin/false

# COPY --from=build --chown=${UID}:${UID}  "/app/queue" /
COPY --from=build  "/app/queue" /
# for some reason mounting a volume inside `queue-proxy` using code doesn't work
# so copying the file here
COPY --from=build /app/dev/sgx/sgx_default_qcnl.conf /etc/sgx_default_qcnl.conf
# also why can't `queue-proxy` create this directory itself?:  /root/.dcap-qcnl/
# because the pod runs with readonly filesystem
RUN mkdir -p /root/.dcap-qcnl && chmod 777 /root/.dcap-qcnl
RUN export SGX_DCAP_QCNL_CACHE_PATH=/tmp/dcap-qcnl-cache && mkdir -p $SGX_DCAP_QCNL_CACHE_PATH

# USER ${UID}

ENV OE_SIMULATION=0
ENTRYPOINT ["ego", "run", "queue"]