# MiniTunnel relay container.
#
# Build from the repo ROOT (the build context must include go.mod and cmd/):
#   docker build -f deploy/Dockerfile -t minitunnel-relay:latest .
#
# All configuration is via MINITUNNEL_* environment variables (every flag has an
# env fallback), so the container needs no command-line arguments. See
# deploy/relay.env.example for the WebSocket / L7-gateway ("scenario B") setup.

# ---- build ----
FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# CGO off -> a fully static binary, so the runtime image can be FROM scratch.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/relay ./cmd/relay

# ---- runtime ----
FROM scratch
# Non-root numeric uid:gid; scratch has no /etc/passwd, and a numeric USER needs
# none. Both listen ports are >1024, so an unprivileged user can bind them.
USER 65532:65532
COPY --from=build /out/relay /relay
# 7000 = raw TLS listener (MINITUNNEL_ADDR), used for a direct/L4 entry.
# 8080 = HTTP listener (MINITUNNEL_HTTP_ADDR): /healthz, the dashboard, and the
#        WebSocket tunnel at <prefix>/tunnel. Point the L7 gateway route here.
EXPOSE 7000 8080
ENTRYPOINT ["/relay"]
