#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
version=${VERSION:-dev}
sh scripts/template-checksums.sh --check
mkdir -p dist
# Remove only the known legacy packaging copies, not unrelated user files.
rm -f dist/install.sh dist/shell-wol.service dist/shell-wol.openwrt dist/shell-wol.plist
assets=()
# OpenWrt and compatible derivatives use GOOS=linux too. Select by CPU/ABI,
# not router brand; see docs/openwrt.md for firmware and runtime requirements.
for target in darwin/amd64 darwin/arm64 windows/amd64 windows/arm64 linux/amd64 linux/arm64 linux/arm/7 linux/mips/softfloat linux/mipsle/softfloat; do
  IFS=/ read -r target_os target_arch variant <<< "$target"
  suffix=""; [[ "$target_os" != windows ]] || suffix=.exe
  artifact="dist/shell-wol-${target_os}-${target_arch}${variant:+-${variant}}${suffix}"
  assets+=("${artifact#dist/}")
  echo "Building $artifact"
  case "$target_arch" in
    arm) CGO_ENABLED=0 GOOS="$target_os" GOARCH="$target_arch" GOARM="$variant" go build -trimpath -ldflags="-s -w -X main.version=$version" -o "$artifact" ./cmd/shell-wol ;;
    mips|mipsle) CGO_ENABLED=0 GOOS="$target_os" GOARCH="$target_arch" GOMIPS="$variant" go build -trimpath -ldflags="-s -w -X main.version=$version" -o "$artifact" ./cmd/shell-wol ;;
    *) CGO_ENABLED=0 GOOS="$target_os" GOARCH="$target_arch" go build -trimpath -ldflags="-s -w -X main.version=$version" -o "$artifact" ./cmd/shell-wol ;;
  esac
done
# Repository scripts/templates are not Release assets. Only hash binaries.
printf '%s\n' "$version" > dist/VERSION
(
  cd dist
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "${assets[@]}" > SHA256SUMS
  else
    shasum -a 256 "${assets[@]}" > SHA256SUMS
  fi
)
