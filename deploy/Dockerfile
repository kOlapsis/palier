# syntax=docker/dockerfile:1
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/palier ./cmd/palier \
 && mkdir -p /out/data

FROM docker/compose-bin:v2.40.3 AS compose

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=compose /docker-compose /usr/local/bin/docker-compose
COPY --from=build --chown=65532:65532 /out/data /var/lib/palier
COPY --from=build /out/palier /palier
USER 65532:65532
ENV PATH=/usr/local/bin HOME=/var/lib/palier
EXPOSE 8080
ENTRYPOINT ["/palier"]
