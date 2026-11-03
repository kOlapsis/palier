# syntax=docker/dockerfile:1
FROM docker/compose-bin:v2.40.3@sha256:e39da8206cc48c6e2c99ce371935eef9d7eff7db600a993fd357de8b75621edf AS compose

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
COPY --from=compose /docker-compose /out/docker-compose
RUN setcap cap_dac_override+ep /out/docker-compose

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/docker-compose /usr/local/bin/docker-compose
COPY --from=build --chown=65532:65532 /out/data /var/lib/palier
COPY --from=build /out/palier /usr/local/bin/palier
USER 65532:65532
ENV PATH=/usr/local/bin HOME=/var/lib/palier
EXPOSE 8080
ENTRYPOINT ["palier"]
CMD ["serve"]
