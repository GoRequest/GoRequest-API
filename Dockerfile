FROM golang:1.26.5-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/gorequest ./cmd/server

FROM alpine:3.23

RUN apk add --no-cache ca-certificates \
    && addgroup -S -g 10001 gorequest \
    && adduser -S -D -H -u 10001 -G gorequest gorequest \
    && mkdir -p /app /data \
    && chown gorequest:gorequest /data \
    && chmod 700 /data

WORKDIR /app
COPY --from=build /out/gorequest /usr/local/bin/gorequest
ENV AUTH_DB_PATH=/data/gorequest.db
USER 10001:10001
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
    CMD wget -q -O /dev/null http://127.0.0.1:8080/health || exit 1
ENTRYPOINT ["/usr/local/bin/gorequest"]
