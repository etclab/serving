FROM golang:1.24 AS builder

WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /audit-sink ./dev/audit-sink/

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /audit-sink /audit-sink
USER 65532:65532
ENTRYPOINT ["/audit-sink"]
