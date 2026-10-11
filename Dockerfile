# syntax=docker/dockerfile:1

# ---- Frontend ----
FROM --platform=$BUILDPLATFORM node:24-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

# ---- Backend ----
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/mailroutine ./cmd/mailroutine

# ---- Runtime ----
FROM alpine:3.24
RUN apk add --no-cache su-exec ca-certificates \
 && addgroup -S -g 10001 mailroutine \
 && adduser -S -u 10001 -G mailroutine -h /data mailroutine \
 && mkdir -p /app /data && chown mailroutine:mailroutine /data
COPY --from=build /out/mailroutine /app/mailroutine
COPY docker-entrypoint.sh /app/docker-entrypoint.sh
RUN chmod +x /app/docker-entrypoint.sh
ENV DATABASE_PATH=/data/mailroutine.db HTTP_PORT=8080
VOLUME ["/data"]
EXPOSE 8080
WORKDIR /app
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["/app/mailroutine", "healthcheck"]
ENTRYPOINT ["/app/docker-entrypoint.sh"]
CMD ["/app/mailroutine", "serve"]
