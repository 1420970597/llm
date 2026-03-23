FROM golang:1.24-alpine AS builder
WORKDIR /src
COPY go.mod ./
COPY apps ./apps
COPY internal ./internal
RUN go build -o /out/worker ./apps/worker

FROM alpine:3.21
WORKDIR /app
COPY --from=builder /out/worker /usr/local/bin/worker
EXPOSE 8081
ENTRYPOINT ["/usr/local/bin/worker"]
