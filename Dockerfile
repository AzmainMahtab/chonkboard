# syntax=docker/dockerfile:1

# ---- stylesheet -------------------------------------------------------------
# Tailwind scans the .templ sources for class names, so it needs the markup, not
# the compiled Go. The standalone CLI means no Node in this image or any other.
FROM alpine:3.21 AS css
ARG TAILWIND_VERSION=v4.1.18
RUN apk add --no-cache curl
WORKDIR /src
RUN curl -sSLf -o /usr/local/bin/tailwindcss \
      "https://github.com/tailwindlabs/tailwindcss/releases/download/${TAILWIND_VERSION}/tailwindcss-linux-x64" \
    && chmod +x /usr/local/bin/tailwindcss
COPY web/css ./web/css
COPY web/layouts ./web/layouts
COPY web/pages ./web/pages
COPY web/components ./web/components
COPY web/static/js ./web/static/js
RUN tailwindcss -i web/css/input.css -o web/static/css/app.css --minify

# ---- binary -----------------------------------------------------------------
FROM golang:1.27-alpine AS builder
WORKDIR /src
RUN go install github.com/a-h/templ/cmd/templ@v0.3.1020
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=css /src/web/static/css/app.css ./web/static/css/app.css
RUN templ generate \
    # CGO off: modernc.org/sqlite is pure Go, so the binary is static and the
    # runtime image needs no libc beyond what alpine already has.
    && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
         -o /out/chonkboard ./cmd/chonkboard

# ---- runtime ----------------------------------------------------------------
FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -g 1000 -S chonk \
    && adduser -u 1000 -S chonk -G chonk
COPY --from=builder /out/chonkboard /usr/local/bin/chonkboard
# Migrations and every asset are embedded in the binary. This directory is the
# only mutable state: database, uploads, backups.
RUN mkdir -p /data/uploads /data/backups && chown -R chonk:chonk /data
USER chonk
WORKDIR /data
VOLUME ["/data"]
EXPOSE 8080
ENV APP_ADDR=:8080 DB_PATH=/data/chonkboard.db UPLOAD_DIR=/data/uploads BACKUP_DIR=/data/backups
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
  CMD wget -qO- http://127.0.0.1:8080/healthz >/dev/null || exit 1
ENTRYPOINT ["chonkboard"]
