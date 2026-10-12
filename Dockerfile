FROM golang:1.27.2-alpine AS build
WORKDIR /src
RUN apk add --no-cache ca-certificates
COPY go.mod go.sum ./
COPY . .
ARG GOPROXY=https://proxy.golang.org,direct
ARG GOSUMDB=sum.golang.org
ENV GOPROXY=${GOPROXY} GOSUMDB=${GOSUMDB} CGO_ENABLED=0 GOTOOLCHAIN=local GOFLAGS=-buildvcs=false
RUN set -e; if [ -d vendor ]; then export GOFLAGS="${GOFLAGS} -mod=vendor"; else go mod download; fi; \
    go build -trimpath -ldflags="-s -w" -o /out/impamind ./cmd/impamind && \
    go build -tags client -trimpath -ldflags="-s -w" -o /out/impamind-linux-amd64 ./cmd/impamind && \
    GOOS=windows GOARCH=amd64 go build -tags client -trimpath -ldflags="-s -w" -o /out/impamind-windows-amd64.exe ./cmd/impamind

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/impamind /impamind
COPY --from=build /out/impamind-linux-amd64 /downloads/impamind-linux-amd64
COPY --from=build /out/impamind-windows-amd64.exe /downloads/impamind-windows-amd64.exe
ENV IMPAMIND_DATA_DIR=/data IMPAMIND_ADDR=:30303
EXPOSE 30303
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=5 CMD ["/impamind", "healthcheck"]
ENTRYPOINT ["/impamind"]
CMD ["serve"]
