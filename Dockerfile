FROM golang:1.26 AS builder

WORKDIR /src

# Inject corporate CA cert so go mod download can reach external HTTPS endpoints
COPY .ca-bundle.pem /usr/local/share/ca-certificates/corp-ca.crt
RUN update-ca-certificates

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    GOPROXY=direct GONOSUMDB='*' go mod download

COPY . .
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -o /rdma_exporter .

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /rdma_exporter /rdma_exporter

USER nonroot:nonroot

ENTRYPOINT ["/rdma_exporter"]
