#!/usr/bin/env bash
# oos build helper. One place that knows the Go version, the binary path and
# where the installed copy lives.
#
#   ./build.sh            build ./oos for this machine
#   ./build.sh install    build and install to /usr/local/bin/oos (GOBIN honoured)
#   ./build.sh test       full test suite, exit code gated (never pipe go test)
#   ./build.sh linux      cross-build dist/oos-linux-amd64 for the fleet
#   ./build.sh clean      remove ./oos, dist/ and the Go build cache for this module
#   ./build.sh all        test, build, linux
set -euo pipefail

ROOT="$(cd "$(dirname "$0")" && pwd)"
cd "$ROOT"

# asdf has no global golang; without this the shim says "No version is set".
export ASDF_GOLANG_VERSION="${ASDF_GOLANG_VERSION:-1.24.6}"
export GOFLAGS="${GOFLAGS:--trimpath}"
GOBIN="${GOBIN:-/usr/local/bin}"
VERSION="$(sed -n 's/^const Version = "\(.*\)"/\1/p' internal/cli/main.go)"

cmd="${1:-build}"
case "$cmd" in
  build)
    go build -ldflags="-s -w" -o oos ./cmd/oos
    echo "built ./oos ($VERSION)"
    ;;
  install)
    go build -ldflags="-s -w" -o oos ./cmd/oos
    install -m 0755 oos "$GOBIN/oos"
    echo "installed $GOBIN/oos ($VERSION)"
    "$GOBIN/oos" -V
    ;;
  test)
    gofmt -l cmd internal | { ! grep . ; } || { echo "gofmt: files above need formatting" >&2; exit 1; }
    go vet ./...
    go test -count=1 ./...
    ;;
  linux)
    mkdir -p dist
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o dist/oos-linux-amd64 ./cmd/oos
    echo "built dist/oos-linux-amd64 ($VERSION)"
    ;;
  clean)
    rm -f oos
    rm -rf dist
    go clean ./...
    echo "cleaned ./oos, dist/ and build outputs"
    ;;
  all)
    "$0" test && "$0" build && "$0" linux
    ;;
  -h|--help|help)
    sed -n '2,11p' "$0"
    ;;
  *)
    echo "build.sh: unknown command $cmd (build|install|test|linux|clean|all)" >&2
    exit 2
    ;;
esac
