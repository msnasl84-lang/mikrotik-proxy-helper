# syntax=docker/dockerfile:1.7
FROM --platform=$BUILDPLATFORM golang:1.23-alpine AS build
ARG TARGETOS TARGETARCH TARGETVARIANT
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH GOARM=${TARGETVARIANT#v} \
    go build -trimpath -ldflags="-s -w" -o /out/helper ./cmd/helper

FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS xray-build
ARG TARGETOS TARGETARCH TARGETVARIANT
ARG XRAY_VERSION=v26.7.11
ENV GOBIN=/out
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH GOARM=${TARGETVARIANT#v} \
    go install -trimpath -ldflags="-s -w" github.com/xtls/xray-core/main@${XRAY_VERSION}

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata && addgroup -S helper && adduser -S -G helper helper
COPY --from=build /out/helper /usr/local/bin/helper
COPY --from=xray-build /out/xray /usr/local/bin/xray
USER helper
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/helper"]
