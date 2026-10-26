FROM golang:1.26@sha256:672c944cfd07599b5f1bb179869270cdc94c6254e41db4d431259693cb286b82 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/server ./cmd/server \
    && CGO_ENABLED=0 go build -o /out/cluster-server ./cmd/cluster-server \
    && CGO_ENABLED=0 go build -o /out/client ./cmd/client

FROM debian:bookworm-slim AS runtime
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --uid 10001 --create-home bank \
    && mkdir -p /app/data \
    && chown -R bank:bank /app
WORKDIR /app
COPY --from=build /out/ /usr/local/bin/
COPY --chown=bank:bank deploy/ ./deploy/
ENV LISTEN_ADDR=0.0.0.0:3000 \
    DB_PATH=/app/data/users.db
USER bank
EXPOSE 3000 7000
CMD ["server"]

# Assignment 2 targets use the shared source/build cache, with one role per image.
FROM build AS assignment2-build
RUN CGO_ENABLED=0 go build -o /out/gateway ./cmd/gateway \
    && CGO_ENABLED=0 go build -o /out/shard-server ./cmd/shard-server

FROM debian:bookworm-slim@sha256:7ff6ba31527b9e336162fa061746c366c4b2032f4d2cff2078bea43f78efe3f8 AS assignment2-runtime
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
USER 10001:10001
ENV LISTEN_ADDR=0.0.0.0:3000
EXPOSE 3000

FROM assignment2-runtime AS gateway
COPY --from=assignment2-build /out/gateway /usr/local/bin/gateway
ENTRYPOINT ["gateway"]

FROM assignment2-runtime AS shard
COPY --from=assignment2-build /out/shard-server /usr/local/bin/shard-server
ENTRYPOINT ["shard-server"]

# Preserve the default Assignment 1 image used by standalone-compose.yml.
FROM runtime AS legacy
