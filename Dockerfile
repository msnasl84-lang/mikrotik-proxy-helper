# syntax=docker/dockerfile:1.7
FROM --platform=$BUILDPLATFORM golang:1.23-alpine AS build
ARG TARGETOS TARGETARCH TARGETVARIANT
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH GOARM=${TARGETVARIANT#v} \
    go build -trimpath -ldflags="-s -w" -o /out/helper ./cmd/helper

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata && addgroup -S helper && adduser -S -G helper helper
COPY --from=build /out/helper /usr/local/bin/helper
USER helper
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/helper"]
