# syntax=docker/dockerfile:1.7
FROM golang:1.25-bookworm AS build

RUN apt-get update && apt-get install -y --no-install-recommends gcc libc6-dev && rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=1 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/marketradar .

FROM debian:bookworm-slim AS runtime
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl tzdata && rm -rf /var/lib/apt/lists/* \
    && groupadd --system --gid 10001 marketradar \
    && useradd --system --uid 10001 --gid marketradar --home-dir /app --shell /usr/sbin/nologin marketradar
WORKDIR /app
COPY --from=build --chown=marketradar:marketradar /out/marketradar /app/marketradar
RUN mkdir -p /app/data && chown marketradar:marketradar /app/data
USER marketradar
ENTRYPOINT ["/app/marketradar"]
CMD ["bot"]
