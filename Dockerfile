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
