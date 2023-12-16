FROM golang:alpine AS builder
WORKDIR /app
ADD . ./
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o k8s-ttl-controller .
RUN apk --update add ca-certificates

FROM scratch
COPY --from=builder /app/k8s-ttl-controller .
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
ENTRYPOINT ["/k8s-ttl-controller"]