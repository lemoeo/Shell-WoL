#!/bin/sh
# Maintainer helper: update or check the committed service-template checksums.
set -eu
case "${1:-}" in ''|--check) ;; *) printf '用法：sh %s [--check]\n' "$0" >&2; exit 2 ;; esac
[ "$#" -le 1 ] || exit 2
cd "$(dirname "$0")/../deploy"
tmp=$(mktemp "${TMPDIR:-/tmp}/shell-wol-templates.XXXXXX")
trap 'rm -f "$tmp"' 0
trap 'exit 130' INT
trap 'exit 143' HUP TERM
if command -v sha256sum >/dev/null 2>&1; then
    sha256sum shell-wol.service shell-wol.openwrt shell-wol.plist >"$tmp"
elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 shell-wol.service shell-wol.openwrt shell-wol.plist >"$tmp"
else
    echo '需要 sha256sum 或 shasum' >&2
    exit 1
fi
if [ "${1:-}" = --check ]; then
    if ! cmp -s SHA256SUMS "$tmp"; then
        echo '服务模板摘要不一致，请在仓库根目录运行 sh scripts/template-checksums.sh 并一起提交 deploy/SHA256SUMS。' >&2
        exit 1
    fi
else
    cat "$tmp" > SHA256SUMS
fi
