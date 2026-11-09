#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
version=${VERSION:-dev}
output_dir=dist/mac-vnc-probe
mkdir -p "$output_dir"
assets=()
for target in darwin/amd64 darwin/arm64 windows/amd64 windows/arm64 linux/amd64 linux/arm64 linux/arm/7 linux/mips/softfloat linux/mipsle/softfloat; do
  IFS=/ read -r target_os target_arch variant <<< "$target"
  suffix=""; [[ "$target_os" != windows ]] || suffix=.exe
  artifact="$output_dir/mac-vnc-probe-${target_os}-${target_arch}${variant:+-${variant}}${suffix}"
  assets+=("${artifact##*/}")
  echo "Building $artifact"
  case "$target_arch" in
    arm) CGO_ENABLED=0 GOOS="$target_os" GOARCH="$target_arch" GOARM="$variant" go build -trimpath -ldflags="-s -w -X main.version=$version" -o "$artifact" ./cmd/mac-vnc-probe ;;
    mips|mipsle) CGO_ENABLED=0 GOOS="$target_os" GOARCH="$target_arch" GOMIPS="$variant" go build -trimpath -ldflags="-s -w -X main.version=$version" -o "$artifact" ./cmd/mac-vnc-probe ;;
    *) CGO_ENABLED=0 GOOS="$target_os" GOARCH="$target_arch" go build -trimpath -ldflags="-s -w -X main.version=$version" -o "$artifact" ./cmd/mac-vnc-probe ;;
  esac
done
printf '%s\n' "$version" > "$output_dir/VERSION"
(
  cd "$output_dir"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "${assets[@]}" > SHA256SUMS
  else
    shasum -a 256 "${assets[@]}" > SHA256SUMS
  fi
)
