# Build auditor-sig with ego-go (needed for CGO deps from pkg/kregistry -> ego/enclave)
# The binary runs as a regular process, not inside an SGX enclave.
ARG egover=1.7.0

FROM ghcr.io/edgelesssys/ego/build-base:v${egover} AS build

ARG egover
WORKDIR /app
COPY . .

RUN apt-get install -y --no-install-recommends \
  build-essential \
  ca-certificates \
  git \
  wget

RUN egodeb=ego_${egover}_amd64_ubuntu-$(grep -oP 'VERSION_ID="\K[^"]+' /etc/os-release).deb \
  && wget https://github.com/edgelesssys/ego/releases/download/v${egover}/${egodeb} \
  && dpkg -i --force-depends ${egodeb}

RUN ego-go build -o /auditor-sig -trimpath ./dev/auditor-sig/

# Use distroless/base (not static) because the ego-go-built binary is dynamically linked (CGO).
FROM gcr.io/distroless/base:nonroot
COPY --from=build /auditor-sig /auditor-sig
USER 65532:65532
ENTRYPOINT ["/auditor-sig"]
