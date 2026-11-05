#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
version=${VERSION:-dev}
mkdir -p dist
for target in darwin/amd64 darwin/arm64 windows/amd64 windows/arm64 linux/amd64 linux/arm64 linux/arm/7 linux/mips/softfloat linux/mipsle/softfloat; do
  IFS=/ read -r target_os target_arch variant <<< "$target"
  suffix=""; [[ "$target_os" != windows ]] || suffix=.exe
  artifact="dist/shell-wol-${target_os}-${target_arch}${variant:+-${variant}}${suffix}"
  echo "Building $artifact"
  case "$target_arch" in
    arm) CGO_ENABLED=0 GOOS="$target_os" GOARCH="$target_arch" GOARM="$variant" go build -trimpath -ldflags="-s -w -X main.version=$version" -o "$artifact" ./cmd/shell-wol ;;
    mips|mipsle) CGO_ENABLED=0 GOOS="$target_os" GOARCH="$target_arch" GOMIPS="$variant" go build -trimpath -ldflags="-s -w -X main.version=$version" -o "$artifact" ./cmd/shell-wol ;;
    *) CGO_ENABLED=0 GOOS="$target_os" GOARCH="$target_arch" go build -trimpath -ldflags="-s -w -X main.version=$version" -o "$artifact" ./cmd/shell-wol ;;
  esac
done
(cd dist && sha256sum shell-wol-* > SHA256SUMS)
