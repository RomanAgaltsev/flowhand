FROM golang:1.26-alpine AS builder
WORKDIR /src
COPY go.* ./
RUN go mod download
COPY cmd/flowhand-demo ./cmd/flowhand-demo
RUN CGO_ENABLED=0 GOOS=linux go build -o /flowhand-demo ./cmd/flowhand-demo

FROM alpine:3.22
COPY --from=builder /flowhand-demo /flowhand-demo
EXPOSE 8081
USER 65534:65534
ENTRYPOINT ["/flowhand-demo"]
