# syntax=docker/dockerfile:1

# ---------------------------------------------------------------------------
# Build: server binary + CLI clients (Linux and Windows) for /downloads
# ---------------------------------------------------------------------------
FROM golang:1.27.2-alpine AS build
WORKDIR /src
RUN apk add --no-cache ca-certificates tzdata
COPY go.mod go.sum ./
ARG GOPROXY=https://proxy.golang.org,direct
ARG GOSUMDB=sum.golang.org
ENV GOPROXY=${GOPROXY} GOSUMDB=${GOSUMDB} CGO_ENABLED=0 GOTOOLCHAIN=local GOFLAGS=-buildvcs=false
COPY . .
ARG VERSION=1.0.0
RUN set -e; if [ -d vendor ]; then export GOFLAGS="${GOFLAGS} -mod=vendor"; else go mod download; fi; \
    LDFLAGS="-s -w -X main.version=${VERSION}"; \
    go build -trimpath -ldflags="${LDFLAGS}" -o /out/impamind ./cmd/impamind && \
    go build -tags client -trimpath -ldflags="${LDFLAGS}" -o /out/impamind-linux-amd64 ./cmd/impamind && \
    GOOS=windows GOARCH=amd64 go build -tags client -trimpath -ldflags="${LDFLAGS}" -o /out/impamind-windows-amd64.exe ./cmd/impamind

# ---------------------------------------------------------------------------
# Runtime: scratch image, a single static binary, no shell
# ---------------------------------------------------------------------------
FROM scratch
ARG VERSION=1.0.0
LABEL org.opencontainers.image.title="ImpaMind" \
      org.opencontainers.image.description="Persistent, auditable memory server for AI agents (MCP + REST + Web UI)" \
      org.opencontainers.image.source="https://github.com/impa365/impamind" \
      org.opencontainers.image.vendor="Impa365" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.version="${VERSION}"
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=build /out/impamind /impamind
COPY --from=build /out/impamind-linux-amd64 /downloads/impamind-linux-amd64
COPY --from=build /out/impamind-windows-amd64.exe /downloads/impamind-windows-amd64.exe
COPY LICENSE NOTICE /licenses/
ENV IMPAMIND_DATA_DIR=/data IMPAMIND_ADDR=:30303
VOLUME ["/data"]
EXPOSE 30303
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=5 CMD ["/impamind", "healthcheck"]
ENTRYPOINT ["/impamind"]
CMD ["serve"]
