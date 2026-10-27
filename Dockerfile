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
FROM build AS sharded-build
RUN CGO_ENABLED=0 go build -o /out/gateway ./cmd/gateway \
    && CGO_ENABLED=0 go build -o /out/shard-server ./cmd/shard-server \
    && CGO_ENABLED=0 go build -o /out/reshard ./cmd/reshard

FROM debian:bookworm-slim@sha256:7ff6ba31527b9e336162fa061746c366c4b2032f4d2cff2078bea43f78efe3f8 AS sharded-runtime
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
USER 10001:10001
ENV LISTEN_ADDR=0.0.0.0:3000
EXPOSE 3000

FROM sharded-runtime AS gateway
COPY --from=sharded-build /out/gateway /usr/local/bin/gateway
ENTRYPOINT ["gateway"]

FROM sharded-runtime AS shard
COPY --from=sharded-build /out/shard-server /usr/local/bin/shard-server
ENTRYPOINT ["shard-server"]

FROM sharded-runtime AS reshard
COPY --from=sharded-build /out/reshard /usr/local/bin/reshard
ENTRYPOINT ["reshard"]

# This controller uses only the Go standard library; no module downloads needed.
FROM golang:1.26@sha256:672c944cfd07599b5f1bb179869270cdc94c6254e41db4d431259693cb286b82 AS healing-build
WORKDIR /src
COPY go.mod ./
COPY internal/healing/ ./internal/healing/
COPY cmd/healing-controller/ ./cmd/healing-controller/
RUN CGO_ENABLED=0 go build -o /out/healing-controller ./cmd/healing-controller

# Docker socket access is required only by this custom orchestration service.
FROM sharded-runtime AS healing-controller
USER 0:0
COPY --from=healing-build /out/healing-controller /usr/local/bin/healing-controller
ENTRYPOINT ["healing-controller"]

# Preserve the default Assignment 1 image used by standalone-compose.yml.
FROM runtime AS legacy
