# syntax=docker/dockerfile:1
ARG REGISTRY=docker.io/library
FROM ${REGISTRY}/golang:1.26-alpine AS build
ARG HTTP_PROXY
ARG HTTPS_PROXY
ENV HTTP_PROXY=${HTTP_PROXY} HTTPS_PROXY=${HTTPS_PROXY} \
    NO_PROXY=localhost,127.0.0.1,.svc.cluster.local,.svc \
    GOPROXY=https://goproxy.cn,direct CGO_ENABLED=0
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -trimpath -ldflags "-s -w" -o /out/sandbox-ext ./cmd/sandbox-ext

FROM ${REGISTRY}/alpine:3.24
RUN sed -i 's|dl-cdn.alpinelinux.org|mirrors.aliyun.com|g' /etc/apk/repositories \
    && apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=build /out/sandbox-ext /app/sandbox-ext
EXPOSE 8080
ENTRYPOINT ["/app/sandbox-ext"]
