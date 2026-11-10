FROM golang:1.26 AS build
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

# Sharded targets share the build cache, including the client built above.
FROM build AS sharded-build
RUN CGO_ENABLED=0 go build -o /out/gateway ./cmd/gateway \
    && CGO_ENABLED=0 go build -o /out/shard-server ./cmd/shard-server \
    && CGO_ENABLED=0 go build -o /out/reshard ./cmd/reshard

FROM debian:bookworm-slim AS sharded-runtime
ARG VERSION=dev
ARG COMMIT=unknown
LABEL org.opencontainers.image.source="https://github.com/azare242/gobank" \
    org.opencontainers.image.revision="${COMMIT}" \
    org.opencontainers.image.version="${VERSION}"
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
USER 10001:10001
ENV LISTEN_ADDR=0.0.0.0:3000 \
    VERSION=${VERSION} \
    COMMIT=${COMMIT}
EXPOSE 3000 9090

FROM sharded-runtime AS gateway
COPY --from=sharded-build /out/gateway /usr/local/bin/gateway
ENTRYPOINT ["gateway"]

FROM sharded-runtime AS shard
COPY --from=sharded-build /out/shard-server /usr/local/bin/shard-server
ENTRYPOINT ["shard-server"]

FROM sharded-runtime AS client
COPY --from=sharded-build /out/client /usr/local/bin/client
ENTRYPOINT ["client"]

FROM sharded-runtime AS reshard
COPY --from=sharded-build /out/reshard /usr/local/bin/reshard
ENTRYPOINT ["reshard"]

# This controller uses only the Go standard library; no module downloads needed.
FROM golang:1.26 AS healing-build
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
