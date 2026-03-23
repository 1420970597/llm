FROM golang:1.24-alpine AS builder
WORKDIR /src
COPY go.mod ./
COPY go.sum ./
COPY apps ./apps
COPY internal ./internal
RUN go build -o /out/api ./apps/api

FROM alpine:3.21
WORKDIR /app
COPY --from=builder /out/api /usr/local/bin/api
COPY sql ./sql
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/api"]
