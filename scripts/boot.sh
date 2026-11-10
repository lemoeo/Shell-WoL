#!/bin/sh
# Persistent boot helper, installed only in firewall mode.
# fw3 may source this file: keep variables, options and traps in a subshell.
(
ROOT='@ROOT@'
SERVICE_FILE=/etc/init.d/shell-wol
BOOT_LOCK=/tmp/shell-wol-boot.lock
set -eu
state_value() { sed -n "s/^$1=//p" "$ROOT/.shell-wol-install"; }
log() { logger -t shell-wol-boot "$*" 2>/dev/null || :; }
die() { log "$*"; printf '%s\n' "$*" >&2; exit 1; }
# BEGIN SHA256 compatibility (also embedded in the persistent boot helper).
SHA256_TOOL=
sha256_digest() {
    # Read stdin so filenames never affect the digest's output format.
    case "$SHA256_TOOL" in
        sha256sum) digest_output=$(sha256sum <"$1") || return 1 ;;
        busybox) digest_output=$(busybox sha256sum <"$1") || return 1 ;;
        shasum) digest_output=$(shasum -a 256 <"$1") || return 1 ;;
        openssl) digest_output=$(openssl dgst -sha256 <"$1") || return 1 ;;
        *) return 1 ;;
    esac
    digest_value=$(printf '%s\n' "$digest_output" | LC_ALL=C awk -v tool="$SHA256_TOOL" '
        NR == 1 {if (tool == "openssl") print $NF; else print $1; next}
        {exit 1}
    ') || return 1
    digest_value=$(printf '%s' "$digest_value" | LC_ALL=C tr 'A-F' 'a-f')
    [ "${#digest_value}" = 64 ] || return 1
    case "$digest_value" in *[!0-9a-f]*) return 1 ;; esac
    printf '%s\n' "$digest_value"
}
require_sha256() {
    [ -z "$SHA256_TOOL" ] || return 0
    for SHA256_TOOL in sha256sum busybox shasum openssl; do
        if digest_probe=$(sha256_digest /dev/null 2>/dev/null) &&
            [ "$digest_probe" = e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855 ]; then
            return 0
        fi
    done
    SHA256_TOOL=
    printf '%s\n' '没有可用的 SHA256 校验工具；已尝试 sha256sum、busybox sha256sum、shasum -a 256、openssl dgst -sha256，请安装或启用其中一种。' >&2
    return 1
}
checksum() {
    require_sha256 || return 1
    sha256_digest "$1"
}
# END SHA256 compatibility
owned_state() {
    [ -f "$ROOT/.shell-wol-install" ] && [ ! -L "$ROOT/.shell-wol-install" ] &&
    [ "$(state_value schema)" = 2 ] && [ "$(state_value service)" = procd ] &&
    [ "$(state_value boot_mode)" = firewall ] && [ "$(state_value boot_script)" = "$ROOT/boot.sh" ] &&
    [ "$(state_value uci_section)" = ShellWoL ]
}
owned_include() {
    [ "$(uci -q get firewall.ShellWoL 2>/dev/null)" = include ] &&
    [ "$(uci -q get firewall.ShellWoL.type 2>/dev/null)" = script ] &&
    [ "$(uci -q get firewall.ShellWoL.path 2>/dev/null)" = "$ROOT/boot.sh" ]
}
allowed() {
    owned_state && owned_include &&
    [ "$(uci -q get firewall.ShellWoL.enabled 2>/dev/null)" = 1 ] &&
    [ ! -e "$ROOT/.autostart-disabled" ] && [ ! -L "$ROOT/.autostart-disabled" ] &&
    [ ! -e "$ROOT/.install-lock" ] && [ ! -L "$ROOT/.install-lock" ]
}
lock() {
    [ ! -e "$ROOT/.install-lock" ] && [ ! -L "$ROOT/.install-lock" ] || die '安装或卸载正在进行，跳过自启动'
    mkdir "$BOOT_LOCK" 2>/dev/null || die '启动操作正在进行；遗留锁需人工确认或重启清理'
    trap 'rmdir "$BOOT_LOCK" 2>/dev/null || :' 0
    trap 'exit 130' INT
    trap 'exit 143' HUP TERM
    [ ! -e "$ROOT/.install-lock" ] && [ ! -L "$ROOT/.install-lock" ] || die '安装或卸载正在进行，跳过自启动'
}
check_paths() {
    parent=$ROOT
    while [ "$parent" != / ]; do [ ! -L "$parent" ] || die "拒绝符号链接目录：$parent"; parent=$(dirname "$parent"); done
    for path in "$ROOT/service" "$ROOT/service/shell-wol.procd" "$ROOT/bin" "$ROOT/bin/shell-wol" "$ROOT/data" "$SERVICE_FILE"; do
        [ ! -L "$path" ] || die "拒绝符号链接：$path"
    done
}
restore_service() {
    source_file=$ROOT/service/shell-wol.procd
    [ -f "$source_file" ] && [ "$(checksum "$source_file")" = "$(state_value service_sha256)" ] || die '持久服务校验失败'
    grep -Fqx "INSTALL_ROOT='$ROOT'" "$source_file" || die '持久服务不属于本安装'
    if [ ! -e "$SERVICE_FILE" ]; then
        mkdir -p "$(dirname "$SERVICE_FILE")"
        # mktemp + rename prevents partial init scripts if power is lost.
        new_file=$(mktemp "$SERVICE_FILE.new.XXXXXX") || die '无法创建服务文件'
        if cp "$source_file" "$new_file" && chmod 755 "$new_file" && mv -f "$new_file" "$SERVICE_FILE"; then :
        else rm -f "$new_file"; die '恢复服务失败'; fi
    fi
    [ -f "$SERVICE_FILE" ] && [ -x "$SERVICE_FILE" ] &&
        grep -Fqx "INSTALL_ROOT='$ROOT'" "$SERVICE_FILE" || die '现有服务不属于本安装或不可执行'
}
run() {
    allowed || return 0
    check_paths
    lock
    allowed || return 0
    require_sha256 || die '没有可用的 SHA256 校验工具，持久自启动已停止'
    # Restore the shortcut when snapshot firmware rebuilds /etc/profile.
    if [ "$(state_value menu_profile)" = /etc/profile ] && [ -w /etc/profile ] && [ ! -L /etc/profile ] &&
        [ -f "$ROOT/menu.sh" ] && [ ! -L "$ROOT/menu.sh" ] &&
        [ "$(checksum "$ROOT/menu.sh")" = "$(state_value menu_sha256)" ]; then
        entry=$(printf "alias wol='sh \"%s/menu.sh\"' # Shell-WoL" "$ROOT")
        if ! grep -Eq '^[[:space:]]*alias[[:space:]]+wol=' /etc/profile; then printf '%s\n' "$entry" >>/etc/profile; fi
    fi
    deadline=$(($(date +%s) + 60))
    while ! { [ -x "$ROOT/bin/shell-wol" ] && [ -f "$ROOT/data/config.yaml" ] &&
        [ -f "$ROOT/data/devices.yaml" ] && ubus -t 2 list service >/dev/null 2>&1; }; do
        allowed || return 0
        [ "$(date +%s)" -lt "$deadline" ] || die '等待 procd 或持久数据超时（60 秒），可恢复后重新执行 boot.sh'
        sleep 1
    done
    restore_service
    if "$SERVICE_FILE" running >/dev/null 2>&1; then
        # Some vendor rc.common implementations return success unconditionally.
        "$ROOT/bin/shell-wol" check --data-dir "$ROOT/data" --running >/dev/null 2>&1 && return 0
    else code=$?; [ "$code" = 1 ] || die '无法确认 procd 服务状态'; fi
    if "$ROOT/bin/shell-wol" lock --help >/dev/null 2>&1; then
        "$ROOT/bin/shell-wol" lock --data-dir "$ROOT/data" >/dev/null || die '数据目录正被占用或存在旧版目录锁'
    else
        [ ! -e "$ROOT/data/.lock" ] && [ ! -L "$ROOT/data/.lock" ] || die '数据运行锁仍存在；不会删除锁或重复启动'
    fi
    "$ROOT/bin/shell-wol" check --data-dir "$ROOT/data" >/dev/null 2>&1 || die 'YAML 配置检查失败'
    allowed || return 0
    "$SERVICE_FILE" start || die 'procd 启动失败'
    while ! { "$SERVICE_FILE" running >/dev/null 2>&1 && "$ROOT/bin/shell-wol" check --data-dir "$ROOT/data" --running >/dev/null 2>&1; }; do
        [ "$(date +%s)" -lt "$deadline" ] || die '服务就绪检查超时，请检查 procd 日志及监听地址'
        sleep 1
    done
    log 'Shell-WoL 已恢复并启动'
}
set_enabled() {
    [ "$(id -u)" = 0 ] || die '修改自启动需要 root'
    owned_state && owned_include || die '启动配置不属于本安装'
    check_paths
    lock
    pending=$(uci -q changes firewall) || die '无法读取 firewall 待提交配置'
    [ -z "$pending" ] || die 'firewall 存在未提交修改，请先处理'
    [ ! -L "$ROOT/.autostart-disabled" ] || die '禁用标记不能是符号链接'
    # Disable locally first; a failed commit must never accidentally re-enable it.
    if [ "$1" = 0 ]; then touch "$ROOT/.autostart-disabled"; chmod 600 "$ROOT/.autostart-disabled"; fi
    if ! { uci set "firewall.ShellWoL.enabled=$1" &&
        pending=$(uci -q changes firewall) &&
        ! printf '%s\n' "$pending" | grep -Ev '^$|^[+-]?firewall\.ShellWoL\.enabled=' >/dev/null &&
        uci commit firewall; }; then
        uci -q revert firewall.ShellWoL.enabled || :
        die '保存自启动状态失败'
    fi
    if [ "$1" = 1 ]; then rm -f "$ROOT/.autostart-disabled"; fi
}
case "${1:-start}" in
    start|boot|restart|reload|includes) if allowed; then "$ROOT/boot.sh" run </dev/null >/dev/null 2>&1 & fi ;;
    run) run ;;
    enable) set_enabled 1; printf '%s\n' '自启动已启用；未立即启动服务' ;;
    disable) set_enabled 0; printf '%s\n' '自启动已禁用；未停止当前服务' ;;
    status) if owned_state && owned_include && [ "$(uci -q get firewall.ShellWoL.enabled)" = 1 ] && [ ! -e "$ROOT/.autostart-disabled" ]; then echo enabled; else echo disabled; exit 1; fi ;;
    *) die '用法：boot.sh [start|run|enable|disable|status]' ;;
esac
)
