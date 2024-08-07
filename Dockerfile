FROM golang:1.22.5 AS builder
WORKDIR /build
COPY go.* /build/
RUN go mod download

COPY cmd/storage-check /build
WORKDIR /build
ENV CGO_ENABLED=0
RUN go build -v
RUN go test -v
RUN groupadd -g 999 user && \
    useradd -r -u 999 -g user user
FROM scratch
COPY --from=builder /etc/passwd /etc/passwd
USER user
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /build/kuberhealthy-storage-check /app/storage-check
ENTRYPOINT ["/app/storage-check"]
