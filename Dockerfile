# The connector as a container, for servers that run everything in Docker:
#   docker run -d --restart unless-stopped -v pawabase-connector:/var/lib/pawabase-connector \
#     -e PAWABASE_SERVER=https://… -e PAWABASE_CONNECTOR_TOKEN=pbce_… -e DATABASE_URL=postgres://… ghcr.io/pawabase/pawabase-connector
# It enrolls on its first start and keeps its key in the volume; DATABASE_URL is read from the environment every time.
FROM golang:1.26-alpine AS build
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN mkdir -p /out/state
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X github.com/Pawabase/pawabase-connector/internal/tunnel.Version=${VERSION}" -o /out/pawabase-connector ./cmd/pawabase-connector

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/pawabase-connector /usr/local/bin/pawabase-connector
# Owned by the image's user, so a new named volume mounted here is writable for the key the connector saves.
COPY --from=build --chown=65532:65532 /out/state /var/lib/pawabase-connector
ENV PAWABASE_CONNECTOR_CONFIG=/var/lib/pawabase-connector/config.json
VOLUME ["/var/lib/pawabase-connector"]
ENTRYPOINT ["/usr/local/bin/pawabase-connector"]
CMD ["run"]
