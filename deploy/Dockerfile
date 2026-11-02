# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.26@sha256:670bb5aa5349ae03b8516764718486f4f16399134bdc9acd0c46a23f043ab05c AS build
ENV GOTOOLCHAIN=local GOFLAGS=-mod=readonly CGO_ENABLED=0
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY cmd ./cmd
COPY internal ./internal
RUN apt-get update && apt-get install -y --no-install-recommends libcap2-bin && rm -rf /var/lib/apt/lists/*
ARG TARGETOS TARGETARCH
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w" -o /out/palier ./cmd/palier \
 && setcap cap_chown,cap_dac_override+ep /out/palier \
 && mkdir -p /out/data

FROM docker/compose-bin:v5.6.1@sha256:2aeaeedd4f2d8e2dcb399b2b9723bfaeb6714140cb0657897f5865960f1624ae AS compose

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=compose /docker-compose /usr/local/bin/docker-compose
COPY --from=build --chown=65532:65532 /out/data /var/lib/palier
COPY --from=build /out/palier /palier
USER 65532:65532
ENV PATH=/usr/local/bin HOME=/var/lib/palier
EXPOSE 8080
ENTRYPOINT ["/palier"]
