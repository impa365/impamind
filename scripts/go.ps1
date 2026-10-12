# Runs the Go toolchain inside a container so the host does not need Go installed.
# Usage: ./scripts/go.ps1 test ./...    |    ./scripts/go.ps1 -Race test ./...
param(
    [switch]$Race,
    [Parameter(ValueFromRemainingArguments = $true)][string[]]$GoArgs
)
if ($env:IMPAMIND_GO_ARGS) { $GoArgs = $env:IMPAMIND_GO_ARGS -split '\s+' }

$ErrorActionPreference = 'Continue'
$root = Split-Path -Parent $PSScriptRoot
$image = if ($Race) { 'golang:1.27.2-bookworm' } else { 'golang:1.27.2-alpine' }
$cgo = if ($Race) { '1' } else { '0' }

docker run --rm `
    -v "${root}:/src" `
    -v impamind-gomod:/go/pkg/mod `
    -v impamind-gocache:/root/.cache/go-build `
    -w /src `
    -e CGO_ENABLED=$cgo `
    -e GOTOOLCHAIN=local `
    -e GOFLAGS=-buildvcs=false `
    -e GOPROXY=http://impamind-modproxy:8099 `
    --link impamind-modproxy `
    -e GOSUMDB=off `
    $image go @GoArgs
exit $LASTEXITCODE
