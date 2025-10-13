# syntax=docker/dockerfile:1.7
ARG GO_VERSION=1.25
ARG ALPINE_VERSION=3.20

FROM golang:${GO_VERSION}-alpine AS builder
RUN apk add --no-cache build-base git
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS=linux
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH:-amd64} go build \
  -trimpath \
  -ldflags="-s -w" \
  -o /out/rdma_exporter ./

FROM alpine:${ALPINE_VERSION}
RUN addgroup -S rdma_exporter \
  && adduser -S -G rdma_exporter rdma_exporter \
  && apk add --no-cache ca-certificates
COPY --from=builder /out/rdma_exporter /usr/local/bin/rdma_exporter
USER rdma_exporter
EXPOSE 9879
ENTRYPOINT ["/usr/local/bin/rdma_exporter"]
CMD ["--listen-address=:9879","--metrics-path=/metrics","--health-path=/healthz"]
