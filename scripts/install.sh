#!/bin/sh
# Shell-WoL installer. Requires POSIX sh; never sources downloaded metadata.
set -eu

REPO=lemoeo/Shell-WoL
DOWNLOAD_BASE=https://github.com/$REPO/releases
REPO_BASE=https://raw.githubusercontent.com/$REPO
SOURCE=github
SOURCE_SET=0
CDN_BASE=${SHELL_WOL_CDN_BASE:-https://cdn.jsdelivr.net}
CUSTOM_DOWNLOAD=0
CUSTOM_REPO=0
VERSION=latest
MENU_CHANGED=0
PROFILE_CHANGED=0
OLD_MENU=0
OLD_PROFILE=0
MENU_PROFILE=
MENU_ENTRY_TMP=
ARCH=
INSTALL_DIR=
USER_MODE=0
WANT_SERVICE=0
BOOT_MODE=auto
PRIOR_BOOT_MODE=native
BOOT_LOCK=/tmp/shell-wol-boot.lock
OWN_BOOT_LOCK=0
FIREWALL_CHANGED=0
BOOT_FILES_CHANGED=0
BOOT_NEW=
ASSUME_YES=0
TMP=
INSTALL_LOCK=
TRANSACTION=0
OLD_BINARY=0
OLD_STATE=0
OLD_SERVICE=0
OLD_SYSTEM_LOCK=0
OLD_LOCK_FILE=0
WAS_ACTIVE=0
WAS_ENABLED=0
SERVICE_CHANGED=0
COMMITTED=0

say() { printf '%s\n' "$*"; }
fail() { say "错误：$*" >&2; exit 1; }
has() { command -v "$1" >/dev/null 2>&1; }
usage() {
    cat <<'HELP'
Shell-WoL 安装器
用法：sh install.sh [选项]
  --version VERSION    固定版本，如 v0.1.0；默认 latest
  --arch ARCH          amd64 / arm64 / armv7 / mips / mipsle
  --install-dir PATH   安装根目录；包含 bin/、data/、backup/
  --user               用户安装，不注册系统服务
  --service            注册后台服务；已有服务升级时自动保留
  --boot-mode MODE     auto / native / firewall；默认自动选择，需后台服务
  --yes                接受已确定的默认选项，不猜测未知架构或覆盖冲突
  --source SOURCE      github / jsdelivr；默认 github，下载失败时停止
  --cdn-base URL       CDN 源站，默认 https://cdn.jsdelivr.net；可改 testingcf.jsdelivr.net
  --download-base URL  HTTPS Release 地址，默认 GitHub Releases
  --repo-base URL      HTTPS 仓库根地址（不含标签），默认 GitHub raw
  --help               显示帮助
安装器不接收密码、不修改 LAN 监听和端口规则；小米镜像固件可注册 firewall include 自启动。
环境变量 SHELL_WOL_CDN_BASE 可设 CDN 默认地址，命令行优先。
HELP
}
valid_version() { [ "$(printf '%s' "$1" | LC_ALL=C tr -cd 'A-Za-z0-9.-')" = "$1" ] && printf '%s\n' "$1" | LC_ALL=C grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9]+([.-][A-Za-z0-9]+)*)?$'; }
validate_dir() {
    [ "$(printf '%s' "$1" | LC_ALL=C tr -cd '\040-\176')" = "$1" ] || fail '安装路径不能含控制字符或非 ASCII 字符'
    # Restrict characters so paths can be represented consistently in all templates.
    printf '%s\n' "$1" | LC_ALL=C grep -Eq '^/[A-Za-z0-9_./ +~-]+$' || fail '安装目录须为绝对路径，仅支持英文、数字、空格及 _ . / + ~ -'
    case "$1" in *//*) fail '安装目录不能包含重复斜线' ;; esac
    case "$1/" in /|//|*/../*|*/./*) fail '安装目录不能是 / 或包含 . / .. 路径段' ;; esac
    case "$1" in /etc|/usr|/usr/bin|/usr/local|/opt|/data|/tmp|/var|/home|/Users|/Library) fail '请指定独立的 shell-wol 子目录' ;; esac
}
confirm() {
    [ "$ASSUME_YES" = 1 ] && return 0
    [ -r /dev/tty ] && [ -w /dev/tty ] || fail '无交互终端；检查参数后使用 --yes'
    printf '%s [y/N] ' "$1" >/dev/tty
    IFS= read -r answer </dev/tty || return 1
    case "$answer" in y|Y|yes|YES) return 0 ;; *) return 1 ;; esac
}
detect_firmware() {
    fsroot=${1:-}
    if [ -f "$fsroot/data/etc/crontabs/root" ] && [ -d "$fsroot/data" ] && [ -w "$fsroot/data" ]; then
        printf '%s\n' xiaomi
    elif [ -f "$fsroot/etc/openwrt_release" ] || { [ -f "$fsroot/etc/rc.common" ] && [ -f "$fsroot/lib/functions/procd.sh" ]; }; then
        printf '%s\n' openwrt
    else
        printf '%s\n' linux
    fi
}
detect_arch() {
    machine=$1
    case "$machine" in
        x86_64|amd64) printf '%s\n' amd64 ;;
        aarch64|arm64) printf '%s\n' arm64 ;;
        armv7*) printf '%s\n' armv7 ;;
        mips*)
            has od || return 1
            endian=$(od -An -tu1 -j5 -N1 /bin/sh 2>/dev/null | tr -d ' \n')
            case "$endian" in 1) printf '%s\n' mipsle ;; 2) printf '%s\n' mips ;; *) return 1 ;; esac
            ;;
        *) return 1 ;;
    esac
}
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
fetch() {
    fetch_url=$1; fetch_out=$2
    fetch_timeout=${3:-180}
    case "$fetch_url" in https://*) ;; *) fail '下载仅允许 HTTPS' ;; esac
    if has curl; then
        curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' --connect-timeout 5 --max-time "$fetch_timeout" --output "$fetch_out" "$fetch_url"
    else
        # Some old BusyBox wget builds cannot verify certificates: reject those.
        wget --https-only -q -t 1 -T "$fetch_timeout" -O "$fetch_out" "$fetch_url"
    fi
}
resource_url() {
    # The CDN mirrors repository trees, not GitHub Release attachments.
    case "$1:$2" in
        latest:github) printf '%s/latest/download/VERSION\n' "$DOWNLOAD_BASE" ;;
        latest:jsdelivr) printf '%s/gh/%s@assets/VERSION\n' "$CDN_BASE" "$REPO" ;;
        binary:github) printf '%s/download/%s/%s\n' "$DOWNLOAD_BASE" "$VERSION" "$3" ;;
        binary:jsdelivr) printf '%s/gh/%s@assets-%s/%s\n' "$CDN_BASE" "$REPO" "$VERSION" "$3" ;;
        script:github) printf '%s/main/scripts/%s\n' "$REPO_BASE" "$3" ;;
        script:jsdelivr) printf '%s/gh/%s@main/scripts/%s\n' "$CDN_BASE" "$REPO" "$3" ;;
        template:github) printf '%s/main/deploy/%s\n' "$REPO_BASE" "$3" ;;
        template:jsdelivr) printf '%s/gh/%s@main/deploy/%s\n' "$CDN_BASE" "$REPO" "$3" ;;
        *) return 1 ;;
    esac
}
fetch_resource() {
    resource_kind=$1; resource_name=$2; resource_out=$3
    case "$resource_kind:$resource_name" in latest:*) resource_timeout=10 ;; template:*|script:*|binary:SHA256SUMS) resource_timeout=20 ;; *) resource_timeout=180 ;; esac
    resource_custom=0
    case "$resource_kind" in
        latest|binary) resource_custom=$CUSTOM_DOWNLOAD ;;
        template|script) resource_custom=$CUSTOM_REPO ;;
    esac
    resource_source=$SOURCE
    if [ "$resource_custom" = 1 ]; then resource_source=github; fi
    resource_url_value=$(resource_url "$resource_kind" "$resource_source" "$resource_name") || return 1
    if fetch "$resource_url_value" "$resource_out.download" "$resource_timeout"; then
        mv -f "$resource_out.download" "$resource_out" || return 1
        return 0
    fi
    rm -f "$resource_out.download"
    return 1
}
verify_asset() {
    asset=$1
    expected=$(awk -v name="$asset" '$2 == name {print $1}' "${2:-$TMP/SHA256SUMS}")
    [ "$(printf '%s\n' "$expected" | wc -l | tr -d ' ')" = 1 ] || fail "校验项重复：$asset"
    printf '%s\n' "$expected" | LC_ALL=C grep -Eq '^[0-9a-f]{64}$' || fail "缺少或无效的校验项：$asset"
    actual=$(checksum "$TMP/$asset") || fail 'SHA256 计算失败'
    [ "$actual" = "$expected" ] || fail "SHA256 校验失败：$asset"
}
xml_escape() { printf '%s' "$1" | sed 's/\&/\&amp;/g; s/</\&lt;/g; s/>/\&gt;/g; s/"/\&quot;/g'; }
state_value() { sed -n "s/^$1=//p" "$ROOT/.shell-wol-install"; }
choose_boot_mode() {
    if [ "$BOOT_MODE" = auto ]; then
        saved_mode=$(state_value boot_mode 2>/dev/null || :)
        if [ -n "$saved_mode" ]; then BOOT_MODE=$saved_mode
        elif [ "$FIRMWARE" = xiaomi ] && [ "$SERVICE" = procd ]; then BOOT_MODE=firewall
        else BOOT_MODE=native; fi
    fi
    if [ "$BOOT_MODE" = firewall ]; then
        [ "$SERVICE" = procd ] && [ "$USER_MODE" = 0 ] || fail 'firewall 自启动仅支持系统级 procd 服务'
        has uci && has ubus || fail 'firewall 自启动需要 uci 和 ubus'
        # fw3 sources script includes using an unquoted path.
        case "$ROOT" in *' '*) fail 'firewall 自启动的安装目录不能包含空格（兼容 fw3 include）' ;; esac
        # The hook itself must be present before late mounts become available.
        case "$FIRMWARE:$ROOT/" in xiaomi:/data/*) ;; xiaomi:*) fail '小米 firewall 自启动须安装到开机即可访问的 /data 子目录' ;; esac
    fi
}
firewall_clean() {
    pending=$(uci -q changes firewall) || fail '无法读取 firewall 待提交配置'
    [ -z "$pending" ] || fail 'firewall 存在未提交修改，请先处理后重试'
}
verify_firewall() {
    uci -q show firewall >/dev/null || fail '无法读取 firewall 配置'
    firewall_clean
    if section=$(uci -q get firewall.ShellWoL); then
        [ "$PRIOR_BOOT_MODE" = firewall ] && [ "$section" = include ] &&
            [ "$(uci -q get firewall.ShellWoL.path)" = "$ROOT/boot.sh" ] &&
            [ "$(uci -q get firewall.ShellWoL.type)" = script ] || fail 'firewall.ShellWoL 已被其他配置占用，拒绝覆盖'
    fi
}
snapshot_firewall() {
    if uci -q get firewall.ShellWoL >/dev/null; then
        uci export firewall >"$TMP/firewall-export" || fail '无法备份 firewall 配置'
        # Preserve this named section, including optional/list values, without
        # importing or restoring any other section. This is UCI data, never shell.
        awk '
            /^package / {print; next}
            /^config / {inside=($1=="config" && $2=="include" && $3=="\047ShellWoL\047")}
            inside {print}
        ' "$TMP/firewall-export" >"$TMP/uci-section"
        grep -q "^config include 'ShellWoL'" "$TMP/uci-section" || fail '无法提取本安装的 firewall include'
    fi
}
commit_firewall() {
    pending=$(uci -q changes firewall) || return 1
    # Never commit another administrator's staged changes, including on rollback.
    if printf '%s\n' "$pending" | grep -Ev '^$|^[+-]?firewall\.ShellWoL([.=]|$)' >/dev/null; then return 1; fi
    uci commit firewall
}
register_firewall() {
    firewall_clean
    FIREWALL_CHANGED=1
    if [ "$BOOT_MODE" = firewall ]; then
        uci set firewall.ShellWoL=include &&
        uci set firewall.ShellWoL.type=script &&
        uci set "firewall.ShellWoL.path=$ROOT/boot.sh" &&
        uci set "firewall.ShellWoL.enabled=$ENABLE_BOOT" || return 1
        if has fw4; then uci set firewall.ShellWoL.fw4_compatible=1 || return 1; fi
    elif uci -q get firewall.ShellWoL >/dev/null; then
        uci delete firewall.ShellWoL || return 1
    fi
    commit_firewall
}
restore_firewall() {
    uci -q revert firewall.ShellWoL || :
    if uci -q get firewall.ShellWoL >/dev/null; then
        [ "$(uci -q get firewall.ShellWoL.path)" = "$ROOT/boot.sh" ] || return 1
    fi
    uci -q delete firewall.ShellWoL || :
    if [ -f "$TMP/uci-section" ]; then
        uci -m import firewall <"$TMP/uci-section" || return 1
    fi
    commit_firewall
}
verify_boot_paths() {
    for path in "$ROOT/service" "$ROOT/service/shell-wol.procd" "$ROOT/boot.sh" "$ROOT/.autostart-disabled"; do
        [ ! -L "$path" ] || fail "启动路径不能是符号链接：$path"
    done
    if [ -e "$ROOT/service" ]; then [ -d "$ROOT/service" ] || fail 'service 不是目录'; fi
    for path in "$ROOT/service/shell-wol.procd" "$ROOT/boot.sh" "$ROOT/.autostart-disabled"; do
        if [ -e "$path" ]; then [ -f "$path" ] || fail "启动文件不是普通文件：$path"; fi
    done
    if [ "$PRIOR_BOOT_MODE" != firewall ]; then
        [ ! -e "$ROOT/boot.sh" ] && [ ! -e "$ROOT/service/shell-wol.procd" ] && [ ! -e "$ROOT/.autostart-disabled" ] || fail '持久启动文件已存在且不属于旧安装'
    else
        [ "$(state_value boot_script)" = "$ROOT/boot.sh" ] && [ "$(state_value uci_section)" = ShellWoL ] || fail '持久启动元数据不属于本安装'
        [ -f "$ROOT/boot.sh" ] && [ "$(checksum "$ROOT/boot.sh")" = "$(state_value boot_sha256)" ] || fail '持久启动脚本缺失或被修改，拒绝覆盖'
        [ -f "$ROOT/service/shell-wol.procd" ] && [ "$(checksum "$ROOT/service/shell-wol.procd")" = "$(state_value service_sha256)" ] || fail '持久服务文件缺失或被修改，拒绝覆盖'
    fi
}
restore_procd_file() {
    [ "$PRIOR_BOOT_MODE" = firewall ] && [ ! -e "$SERVICE_FILE" ] || return 0
    grep -Fqx "INSTALL_ROOT='$ROOT'" "$ROOT/service/shell-wol.procd" || fail '持久服务不属于本安装'
    mkdir -p "$(dirname "$SERVICE_FILE")"
    restored=$(mktemp "$SERVICE_FILE.new.XXXXXX")
    if cp "$ROOT/service/shell-wol.procd" "$restored" && chmod 755 "$restored" && mv -f "$restored" "$SERVICE_FILE"; then :
    else rm -f "$restored"; fail '恢复旧 procd 服务失败'; fi
}
render_boot() {
    sed "s|@ROOT@|$ROOT|g" "$TMP/boot.sh" >"$TMP/rendered-boot"
}
service_active() {
    case "$SERVICE" in
        systemd) systemctl is-active --quiet shell-wol.service ;;
        procd) "$SERVICE_FILE" running >/dev/null 2>&1 ;;
        launchd-user|launchd-system) launchctl print "$LAUNCH_DOMAIN/io.github.lemoeo.shell-wol" >/dev/null 2>&1 ;;
        *) return 1 ;;
    esac
}
service_enabled() {
    if [ "$PRIOR_BOOT_MODE" = firewall ]; then
        [ ! -e "$ROOT/.autostart-disabled" ] && [ "$(uci -q get firewall.ShellWoL.enabled)" = 1 ]; return
    fi
    case "$SERVICE" in
        systemd) systemctl is-enabled --quiet shell-wol.service ;;
        procd) "$SERVICE_FILE" enabled >/dev/null 2>&1 ;;
        launchd-user|launchd-system)
            disabled=$(launchctl print-disabled "$LAUNCH_DOMAIN") || return 1
            ! printf '%s\n' "$disabled" | grep -q '"io.github.lemoeo.shell-wol" => true'
            ;;
        *) return 1 ;;
    esac
}
service_stop() {
    case "$SERVICE" in
        systemd) systemctl stop shell-wol.service ;;
        procd) "$SERVICE_FILE" stop ;;
        launchd-user|launchd-system) launchctl bootout "$LAUNCH_DOMAIN/io.github.lemoeo.shell-wol" ;;
    esac
}
service_disable() {
    case "$SERVICE" in
        systemd) systemctl disable shell-wol.service >/dev/null ;;
        procd) "$SERVICE_FILE" disable ;;
        launchd-user|launchd-system) launchctl disable "$LAUNCH_DOMAIN/io.github.lemoeo.shell-wol" ;;
    esac
}
service_enable() {
    [ "$BOOT_MODE" != firewall ] || return 0
    case "$SERVICE" in
        systemd) systemctl enable shell-wol.service >/dev/null ;;
        procd) "$SERVICE_FILE" enable ;;
        launchd-user|launchd-system) launchctl enable "$LAUNCH_DOMAIN/io.github.lemoeo.shell-wol" ;;
    esac
}
service_reload() { [ "$SERVICE" != systemd ] || systemctl daemon-reload; }
service_start() {
    case "$SERVICE" in
        systemd) systemctl start shell-wol.service ;;
        procd) "$SERVICE_FILE" start ;;
        launchd-user|launchd-system)
            # bootstrap refuses disabled jobs. Restore the flag even on failure;
            # this also applies when restarting the old job during rollback.
            if [ "${ENABLE_BOOT:-1}" = 0 ]; then service_enable || return 1; fi
            bootstrap_result=0
            launchctl bootstrap "$LAUNCH_DOMAIN" "$SERVICE_FILE" || bootstrap_result=$?
            if [ "${ENABLE_BOOT:-1}" = 0 ]; then service_disable || return 1; fi
            [ "$bootstrap_result" = 0 ] || return 1 ;;
    esac
    attempts=0
    while [ "$attempts" -lt 10 ]; do
        if service_active && "$BIN/shell-wol" check --data-dir "$DATA" --running >/dev/null 2>&1; then return 0; fi
        attempts=$((attempts+1)); sleep 1
    done
    return 1
}
rollback() {
    say '安装失败，正在恢复旧程序和服务……' >&2
    if [ "$PROFILE_CHANGED" = 1 ]; then
        if [ "$OLD_PROFILE" = 1 ]; then cp -p "$TMP/old-menu-profile" "$MENU_PROFILE"
        else rm -f "$MENU_PROFILE"; fi
    fi
    if [ "$MENU_CHANGED" = 1 ]; then
        if [ "$OLD_MENU" = 1 ]; then cp -p "$TMP/old-menu" "$ROOT/menu.sh"
        else rm -f "$ROOT/menu.sh"; fi
    fi
    if [ "$SERVICE_CHANGED" = 1 ]; then
        service_stop >/dev/null 2>&1 || :
        service_disable >/dev/null 2>&1 || :
        if [ "$OLD_SERVICE" = 1 ]; then cp "$TMP/old-service" "$SERVICE_FILE" || say '警告：服务文件恢复失败' >&2
        else rm -f "$SERVICE_FILE"; fi
        service_reload >/dev/null 2>&1 || :
    fi
    restore_boot_failed=0
    if [ "$FIREWALL_CHANGED" = 1 ]; then
        if ! restore_firewall; then restore_boot_failed=1; say '警告：firewall 入口恢复失败，自启动保持禁用，请人工检查' >&2; fi
    fi
    if [ "$BOOT_FILES_CHANGED" = 1 ]; then
        for name in boot.sh service/shell-wol.procd .autostart-disabled; do
            if [ -f "$TMP/boot-backup/$name" ]; then mkdir -p "$(dirname "$ROOT/$name")"; cp "$TMP/boot-backup/$name" "$ROOT/$name" || say "警告：恢复失败 $name" >&2
            else rm -f "$ROOT/$name"; fi
        done
        rmdir "$ROOT/service" 2>/dev/null || :
    fi
    if [ "$restore_boot_failed" = 1 ]; then touch "$ROOT/.autostart-disabled"; chmod 600 "$ROOT/.autostart-disabled"; fi
    BOOT_MODE=$PRIOR_BOOT_MODE
    if [ "${NEW_SYSTEM_LOCK:-0}" = 1 ] && [ "$OLD_SYSTEM_LOCK" = 0 ] && [ "$OLD_LOCK_FILE" = 0 ] && [ -f "$DATA/.lock" ]; then
        # Remove only a file introduced by this failed upgrade, under its lock.
        "$TMP/$artifact" lock --data-dir "$DATA" -- sh -c '
            [ ! -L "$1/.lock" ] && [ -f "$1/.lock" ] || exit 1
            rm -f "$1/.setup-token" && rm -f "$1/.lock"
        ' rollback-lock "$DATA" || say '警告：数据锁无法恢复为旧版格式，请先确认进程状态' >&2
    fi
    if [ "$OLD_BINARY" = 1 ]; then
        cp "$TMP/old-binary" "$BIN/.shell-wol-restore" && chmod 755 "$BIN/.shell-wol-restore" && mv -f "$BIN/.shell-wol-restore" "$BIN/shell-wol" || say '警告：旧程序恢复失败' >&2
    else rm -f "$BIN/shell-wol"; fi
    if [ "$OLD_STATE" = 1 ]; then cp "$TMP/old-state" "$ROOT/.shell-wol-install" || say '警告：安装状态恢复失败' >&2
    else
        # Retain ownership of a fresh, partially initialized directory for retry.
        printf 'schema=1\nversion=uninstalled\nservice=none\nservice_sha256=\ninstall_mode=%s\n' "$INSTALL_MODE" >"$ROOT/.shell-wol-install"
        chmod 600 "$ROOT/.shell-wol-install"
    fi
    rm -f "$BIN/.shell-wol-new" "$ROOT/.shell-wol-install.new"
    if [ "$WAS_ENABLED" = 1 ]; then service_enable >/dev/null 2>&1 || say '警告：恢复服务启用状态失败' >&2; fi
    if [ "$WAS_ACTIVE" = 1 ] && ! service_active; then service_start >/dev/null 2>&1 || say '警告：恢复旧服务失败，请检查日志' >&2; fi
    say '已有 YAML 数据未被替换；请检查服务状态后重试。' >&2
}
cleanup() {
    result=$?
    trap - 0 HUP INT TERM
    if [ "$TRANSACTION" = 1 ] && [ "$COMMITTED" = 0 ]; then rollback || :; fi
    if [ -n "$BOOT_NEW" ]; then rm -f "$BOOT_NEW"; fi
    if [ -n "$MENU_ENTRY_TMP" ]; then rm -f "$MENU_ENTRY_TMP"; fi
    if [ "$OWN_BOOT_LOCK" = 1 ]; then rmdir "$BOOT_LOCK" 2>/dev/null || :; fi
    if [ -n "$INSTALL_LOCK" ]; then rmdir "$INSTALL_LOCK" 2>/dev/null || :; fi
    if [ -n "$TMP" ]; then rm -rf "$TMP"; fi
    exit "$result"
}
menu_profile_path() {
    if [ "$USER_MODE" = 0 ] && [ "$OS" != darwin ] && [ -w /etc/profile ]; then printf '/etc/profile\n'
    else
        menu_home=$(CDPATH= cd "$HOME" && pwd -P) || return 1
        case "${SHELL:-}" in
            */zsh) printf '%s/.zshrc\n' "$menu_home" ;;
            */bash) if [ "$OS" = darwin ]; then printf '%s/.bash_profile\n' "$menu_home"; else printf '%s/.bashrc\n' "$menu_home"; fi ;;
            *) printf '%s/.profile\n' "$menu_home" ;;
        esac
    fi
}
check_menu_paths() {
    [ ! -L "$ROOT/menu.sh" ] || fail 'menu.sh 不能是符号链接'
    if [ -e "$ROOT/menu.sh" ]; then
        [ -f "$ROOT/menu.sh" ] && [ "$(checksum "$ROOT/menu.sh")" = "$(state_value menu_sha256)" ] || fail 'menu.sh 无安装记录或已被修改'
    fi
    validate_dir "$MENU_PROFILE"
    parent=$MENU_PROFILE
    while [ "$parent" != / ]; do [ ! -L "$parent" ] || fail '菜单 profile 路径含符号链接'; parent=$(dirname "$parent"); done
    [ -w "$(dirname "$MENU_PROFILE")" ] || fail "无法写入菜单快捷入口：$MENU_PROFILE"
    if [ -e "$MENU_PROFILE" ]; then
        [ -f "$MENU_PROFILE" ] && [ -w "$MENU_PROFILE" ] || fail '菜单 profile 不是可写普通文件'
        if grep -E '^[[:space:]]*alias[[:space:]]+wol=' "$MENU_PROFILE" | grep -Fvx "$MENU_ALIAS" >/dev/null; then fail 'profile 已有其他 wol 别名'; fi
    fi
}
install_menu() {
    check_menu_paths
    if [ -f "$ROOT/menu.sh" ]; then cp -p "$ROOT/menu.sh" "$TMP/old-menu"; OLD_MENU=1; fi
    if [ -f "$MENU_PROFILE" ]; then cp -p "$MENU_PROFILE" "$TMP/old-menu-profile"; OLD_PROFILE=1; fi
    MENU_CHANGED=1
    cp "$TMP/menu.sh" "$ROOT/menu.sh"; chmod 755 "$ROOT/menu.sh"
    MENU_ENTRY_TMP=$(mktemp "$MENU_PROFILE.shell-wol.XXXXXX")
    if [ "$OLD_PROFILE" = 1 ]; then
        cp -p "$MENU_PROFILE" "$MENU_ENTRY_TMP"
        legacy_entry=$(printf "alias swol='sh \"%s/menu.sh\"' # Shell-WoL" "$ROOT")
        awk -v entry="$MENU_ALIAS" -v legacy="$legacy_entry" '$0!=entry && $0!=legacy' "$MENU_PROFILE" >"$MENU_ENTRY_TMP"
    else chmod 644 "$MENU_ENTRY_TMP"; fi
    printf '%s\n' "$MENU_ALIAS" >>"$MENU_ENTRY_TMP"
    if [ "$OLD_PROFILE" = 1 ]; then cmp -s "$MENU_PROFILE" "$TMP/old-menu-profile" || fail 'profile 在注册期间发生变化'
    else [ ! -e "$MENU_PROFILE" ] || fail 'profile 在注册期间被创建'; fi
    PROFILE_CHANGED=1
    mv -f "$MENU_ENTRY_TMP" "$MENU_PROFILE"; MENU_ENTRY_TMP=
}
render_service() {
    case "$SERVICE" in
        systemd)
            sed -e "s|@ROOT@|$ROOT|g" -e "s|@GROUP@|$SERVICE_GROUP|g" "$TMP/shell-wol.service" >"$TMP/rendered-service"
            ;;
        procd) sed "s|@ROOT@|$ROOT|g" "$TMP/shell-wol.openwrt" >"$TMP/rendered-service" ;;
        launchd-user|launchd-system)
            xmlroot=$(xml_escape "$ROOT")
            # Chosen path character set excludes sed's replacement metacharacters.
            sed -e "s|@ROOT@|$xmlroot|g" -e "s|@USERNAME@|$SERVICE_USER|g" "$TMP/shell-wol.plist" >"$TMP/rendered-service"
            ;;
    esac
}
choose_service() {
    SERVICE=none; SERVICE_FILE=; SERVICE_USER=; SERVICE_GROUP=; LAUNCH_DOMAIN=
    case "$OS" in
        darwin)
            if [ "$USER_MODE" = 1 ]; then
                SERVICE=launchd-user; LAUNCH_DOMAIN=gui/$(id -u); SERVICE_USER=$(id -un)
                SERVICE_FILE="$HOME/Library/LaunchAgents/io.github.lemoeo.shell-wol.plist"
            else
                SERVICE=launchd-system; LAUNCH_DOMAIN=system; SERVICE_USER=daemon
                SERVICE_FILE=/Library/LaunchDaemons/io.github.lemoeo.shell-wol.plist
            fi ;;
        linux)
            [ "$USER_MODE" = 0 ] || fail 'Linux 用户安装暂不注册后台服务，请手动运行 serve'
            if [ -f /etc/rc.common ] && [ -f /lib/functions/procd.sh ] && [ "$(cat /proc/1/comm 2>/dev/null)" = procd ]; then SERVICE=procd; SERVICE_FILE=/etc/init.d/shell-wol
            elif has systemctl && [ "$(cat /proc/1/comm 2>/dev/null)" = systemd ]; then SERVICE=systemd; SERVICE_FILE=/etc/systemd/system/shell-wol.service; SERVICE_USER=shell-wol
            else fail '未识别到正在运行的 procd/systemd；可不加 --service 安装并手动启动'; fi ;;
    esac
    if [ "$OS" = darwin ]; then
        has launchctl || fail '缺少 launchctl'
        launchctl print "$LAUNCH_DOMAIN" >/dev/null 2>&1 || fail 'launchd 会话不可用；用户服务需 GUI 登录会话，可先不加 --service 安装'
    fi
}
prepare_service_user() {
    if [ "$SERVICE" = systemd ]; then
        if ! id shell-wol >/dev/null 2>&1; then
            has useradd || fail '缺少 useradd；请先创建 shell-wol 系统用户'
            useradd --system --user-group --no-create-home --home-dir "$DATA" --shell /usr/sbin/nologin shell-wol
        fi
        [ "$(id -u shell-wol)" != 0 ] || fail 'shell-wol 服务账户不能是 root'
        SERVICE_GROUP=$(id -gn shell-wol)
    fi
    if [ -n "$SERVICE_USER" ]; then
        id "$SERVICE_USER" >/dev/null 2>&1 || fail "服务用户不存在：$SERVICE_USER"
        if [ "$USER_MODE" = 0 ]; then chown -R "$SERVICE_USER" "$DATA"; fi
    fi
}
password_ready() {
    # Use the application's YAML parser rather than interpreting YAML in shell.
    "$BIN/shell-wol" check --data-dir "$DATA" --require-password >/dev/null 2>&1
}
parse_options() {
    while [ "$#" -gt 0 ]; do
        case "$1" in
            --help|-h) usage; exit 0 ;;
            --version|--arch|--install-dir|--download-base|--repo-base|--boot-mode|--source|--cdn-base)
                [ "$#" -ge 2 ] || fail "$1 缺少参数"
                case "$1" in --version) VERSION=$2 ;; --arch) ARCH=$2 ;; --install-dir) INSTALL_DIR=$2 ;; --download-base) DOWNLOAD_BASE=$2; CUSTOM_DOWNLOAD=1 ;; --repo-base) REPO_BASE=$2; CUSTOM_REPO=1 ;; --boot-mode) BOOT_MODE=$2 ;; --source) SOURCE=$2; SOURCE_SET=1 ;; --cdn-base) CDN_BASE=$2 ;; esac; shift 2 ;;
            --user) USER_MODE=1; shift ;;
            --service) WANT_SERVICE=1; shift ;;
            --yes|-y) ASSUME_YES=1; shift ;;
            *) fail "未知参数：$1" ;;
        esac
    done
}

resolve_install_mode() {
    if [ -f "$INSTALL_DIR/.shell-wol-install" ] && [ ! -L "$INSTALL_DIR/.shell-wol-install" ]; then
        recorded_mode=$(sed -n 's/^install_mode=//p' "$INSTALL_DIR/.shell-wol-install")
        case "$recorded_mode" in
            user) USER_MODE=1 ;;
            system) [ "$USER_MODE" = 0 ] || fail '已有安装为系统模式，不能使用 --user 升级' ;;
            '') ;; # Legacy records rely on the original caller's --user option.
            *) fail '安装记录中的 install_mode 无效' ;;
        esac
    fi
    INSTALL_MODE=system; [ "$USER_MODE" = 0 ] || INSTALL_MODE=user
    [ "$USER_MODE" = 1 ] || [ "$(id -u)" = 0 ] || fail '系统安装需要 root；可使用 --user 或由管理员执行'
}

detect_environment() {
    case "$BOOT_MODE" in auto|native|firewall) ;; *) fail '--boot-mode 必须为 auto、native 或 firewall' ;; esac
    case "$SOURCE" in github|jsdelivr) ;; *) fail '--source 必须为 github 或 jsdelivr' ;; esac
    CDN_BASE=${CDN_BASE%/}
    [ "$(printf '%s' "$CDN_BASE" | LC_ALL=C tr -cd '\040-\176')" = "$CDN_BASE" ] || fail '--cdn-base 不能含控制字符或非 ASCII 字符'
    printf '%s\n' "$CDN_BASE" | LC_ALL=C grep -Eq '^https://[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?(:[0-9]+)?$' || fail '--cdn-base 必须为 HTTPS 源站地址，不含 /gh、路径、查询参数或片段'
    case "$(uname -s)" in Linux) OS=linux; FIRMWARE=$(detect_firmware) ;; Darwin) OS=darwin; FIRMWARE=macos ;; *) fail '仅支持 macOS 和兼容 Linux 固件' ;; esac
    if [ -z "$ARCH" ]; then ARCH=$(detect_arch "$(uname -m)") || fail '无法可靠识别 CPU，请用 --arch 指定'; fi
    case "$ARCH" in amd64|arm64|armv7|mips|mipsle) ;; *) fail "不支持的架构：$ARCH" ;; esac
    if [ "$OS" = darwin ]; then case "$ARCH" in amd64|arm64) ;; *) fail 'macOS 仅支持 amd64/arm64' ;; esac; fi
    if [ "$VERSION" != latest ]; then valid_version "$VERSION" || fail '版本格式须为 v主版本.次版本.补丁[-预发布标识]'; fi
    case "$DOWNLOAD_BASE" in https://*) ;; *) fail '--download-base 必须是 HTTPS 地址' ;; esac
    case "$REPO_BASE" in https://*) ;; *) fail '--repo-base 必须是 HTTPS 地址' ;; esac
    DOWNLOAD_BASE=${DOWNLOAD_BASE%/}; REPO_BASE=${REPO_BASE%/}
    if [ "$OS" = darwin ] && [ "$(id -u)" != 0 ]; then USER_MODE=1; fi
    if [ -z "$INSTALL_DIR" ]; then
        if [ "$USER_MODE" = 1 ]; then
            if [ "$OS" = darwin ]; then INSTALL_DIR="$HOME/Library/Application Support/Shell-WoL"
            else INSTALL_DIR="${XDG_DATA_HOME:-$HOME/.local/share}/shell-wol"; fi
        else
            case "$FIRMWARE" in macos) INSTALL_DIR='/Library/Application Support/Shell-WoL' ;; xiaomi) INSTALL_DIR=/data/shell-wol ;; openwrt) INSTALL_DIR=/etc/shell-wol ;; *) INSTALL_DIR=/opt/shell-wol ;; esac
        fi
    fi
    INSTALL_DIR=${INSTALL_DIR%/}; validate_dir "$INSTALL_DIR"
    resolve_install_mode
    case "$INSTALL_DIR/" in /tmp/*|/var/run/*|/run/*|/sys/*|/proc/*|/dev/*) fail '安装根目录必须使用持久化存储' ;; esac
    has curl || has wget || fail '需要 curl 或 wget'
    require_sha256 || fail '无法执行 SHA256 校验，安装已停止'
    if ! has curl; then wget --help 2>&1 | grep -q -- '--https-only' || fail '需要支持 --https-only 的 wget；精简固件请先安装 curl 和 CA 证书'; fi
}

plan_install() {
    say "系统：$OS；固件类型：$FIRMWARE；CPU：$ARCH"
    say "安装根目录：$INSTALL_DIR"
    if [ "$ASSUME_YES" = 0 ]; then
        [ -r /dev/tty ] && [ -w /dev/tty ] || fail '无交互终端；检查参数后使用 --yes'
        if [ "$SOURCE_SET" = 0 ]; then
            printf '安装源：1 GitHub（默认）  2 jsDelivr\n请选择 [1]：' >/dev/tty
            IFS= read -r source_choice </dev/tty || fail '无法读取安装源选择'
            case "$source_choice" in ''|1) SOURCE=github ;; 2) SOURCE=jsdelivr ;; *) fail '无效的安装源选择' ;; esac
        fi
        printf '回车使用推荐目录，或输入其他绝对路径：' >/dev/tty
        IFS= read -r chosen </dev/tty || fail '无法读取目录选择'
        if [ -n "$chosen" ]; then INSTALL_DIR=${chosen%/}; validate_dir "$INSTALL_DIR"; fi
        if [ "$WANT_SERVICE" = 0 ] && confirm '是否配置后台服务？'; then WANT_SERVICE=1; fi
    fi
    say "安装源：$SOURCE；CDN：$CDN_BASE"
    case "$INSTALL_DIR/" in /tmp/*|/var/run/*|/run/*|/sys/*|/proc/*|/dev/*) fail '安装根目录必须使用持久化存储' ;; esac
    confirm "安装到 $INSTALL_DIR？" || fail '安装已取消'
    # Refuse symlinked directories before making any installation changes.
    parent=$INSTALL_DIR
    while [ "$parent" != / ]; do [ ! -L "$parent" ] || fail "路径含符号链接：$parent"; parent=$(dirname "$parent"); done
    if [ -d "$INSTALL_DIR" ] && [ -n "$(ls -A "$INSTALL_DIR")" ] && [ ! -f "$INSTALL_DIR/.shell-wol-install" ]; then fail '目标目录非空且不是安装器管理的目录；请选择新目录，旧数据请手动迁移'; fi
    ROOT=$INSTALL_DIR; BIN=$ROOT/bin; DATA=$ROOT/data
    resolve_install_mode
    for path in "$ROOT/.shell-wol-install" "$BIN" "$DATA" "$ROOT/backup" "$BIN/shell-wol"; do [ ! -L "$path" ] || fail "安装路径不能是符号链接：$path"; done
    if [ -d "$DATA" ] && [ -n "$(find "$DATA" -type l -print)" ]; then fail '数据目录含符号链接，需先人工检查'; fi
    SERVICE=none; SERVICE_FILE=; SERVICE_USER=; SERVICE_GROUP=; LAUNCH_DOMAIN=
    planned_state=none
    if [ -f "$ROOT/.shell-wol-install" ]; then
        planned_state=$(checksum "$ROOT/.shell-wol-install")
        schema=$(state_value schema)
        case "$schema" in 1|2) ;; *) fail '不支持的安装状态文件' ;; esac
        PRIOR_BOOT_MODE=$(state_value boot_mode)
        PRIOR_BOOT_MODE=${PRIOR_BOOT_MODE:-native}
        case "$schema:$PRIOR_BOOT_MODE" in 1:native|2:firewall) ;; *) fail '无效的启动模式元数据' ;; esac
        prior_service=$(state_value service)
        case "$prior_service" in none) ;; systemd|procd|launchd-user|launchd-system) WANT_SERVICE=1 ;; *) fail '无效的安装状态文件' ;; esac
        if [ "$PRIOR_BOOT_MODE" = firewall ]; then [ "$prior_service" = procd ] || fail '持久启动元数据的服务类型无效'; fi
    else prior_service=none; fi
    if [ -f "$ROOT/.shell-wol-install" ]; then MENU_PROFILE=$(state_value menu_profile); fi
    [ -n "$MENU_PROFILE" ] || MENU_PROFILE=$(menu_profile_path)
    MENU_ALIAS=$(printf "alias wol='sh \"%s/menu.sh\"' # Shell-WoL" "$ROOT")
    check_menu_paths
    if [ "$WANT_SERVICE" = 1 ]; then
        choose_service
        choose_boot_mode
        [ "$prior_service" = none ] || [ "$prior_service" = "$SERVICE" ] || fail '服务类型与旧安装不同，请先人工迁移'
        if [ "$PRIOR_BOOT_MODE" = firewall ]; then [ "$SERVICE" = procd ] || fail '持久启动元数据的服务类型无效'; fi
        if [ -e "$SERVICE_FILE" ]; then
            [ ! -L "$SERVICE_FILE" ] || fail '服务文件不能是符号链接'
            [ "$prior_service" = "$SERVICE" ] || fail "服务文件已存在，拒绝覆盖：$SERVICE_FILE"
            old_hash=$(state_value service_sha256)
            [ "$(checksum "$SERVICE_FILE")" = "$old_hash" ] || fail '服务文件被手动修改，拒绝覆盖；请先备份并人工合并'
        fi
    else
        [ "$BOOT_MODE" = auto ] || fail '--boot-mode 需要 --service 或已有后台服务'
        BOOT_MODE=native
    fi
    if [ "$BOOT_MODE" = firewall ] || [ "$PRIOR_BOOT_MODE" = firewall ]; then
        has uci && has ubus || fail '持久自启动管理需要 uci 和 ubus'
        verify_boot_paths
        verify_firewall
    fi
}

download_assets() {
    TMP=$(mktemp -d "${TMPDIR:-/tmp}/shell-wol-install.XXXXXX") || fail '无法创建临时目录'
    trap cleanup 0
    trap 'exit 130' INT
    trap 'exit 143' HUP TERM
    if [ "$VERSION" = latest ]; then fetch_resource latest VERSION "$TMP/VERSION" || fail '无法获取版本信息；CDN 源需要先同步 assets 分支'; VERSION=$(cat "$TMP/VERSION"); valid_version "$VERSION" || fail '远端版本信息无效'; fi
    say "下载固定版本：$VERSION"
    fetch_resource binary SHA256SUMS "$TMP/SHA256SUMS" || fail "无法下载 $VERSION 校验文件；CDN 源需要 assets-$VERSION 标签"
    case "$ARCH" in armv7) suffix=arm-7 ;; mips|mipsle) suffix=$ARCH-softfloat ;; *) suffix=$ARCH ;; esac
    artifact=shell-wol-$OS-$suffix
    fetch_resource binary "$artifact" "$TMP/$artifact" || fail "二进制下载失败（$VERSION）；不会回退到其他版本"
    verify_asset "$artifact"
    chmod 755 "$TMP/$artifact"
    [ "$("$TMP/$artifact" version)" = "$VERSION" ] || fail '二进制无法运行或版本不匹配'
    NEW_SYSTEM_LOCK=0
    if "$TMP/$artifact" lock --help >/dev/null 2>&1; then NEW_SYSTEM_LOCK=1; fi
    # Deployment files track main independently of the binary Release.
    fetch_resource template SHA256SUMS "$TMP/deploy-SHA256SUMS" || fail '无法下载 main 部署文件校验清单'
    if [ "$SERVICE" != none ]; then
        case "$SERVICE" in systemd) template=shell-wol.service ;; procd) template=shell-wol.openwrt ;; *) template=shell-wol.plist ;; esac
        fetch_resource template "$template" "$TMP/$template" || fail '无法下载 main 服务模板'
        verify_asset "$template" "$TMP/deploy-SHA256SUMS"
    fi
    if [ "$BOOT_MODE" = firewall ]; then
        fetch_resource script boot.sh "$TMP/boot.sh" || fail '无法下载 main 的 boot.sh'
        verify_asset boot.sh "$TMP/deploy-SHA256SUMS"
    fi
    fetch_resource script menu.sh "$TMP/menu.sh" || fail '无法下载 main 终端菜单'
    verify_asset menu.sh "$TMP/deploy-SHA256SUMS"
    existing=$ROOT
    while [ ! -d "$existing" ]; do existing=$(dirname "$existing"); done
    [ -w "$existing" ] || fail "目录不可写：$existing"
    needed=$(du -k "$TMP/$artifact" | awk '{print $1}'); needed=$((needed * 3 + 2048))
    available=$(df -Pk "$existing" | awk 'END {print $4}')
    case "$available" in ''|*[!0-9]*) fail '无法确定剩余空间' ;; esac
    [ "$available" -ge "$needed" ] || fail "空间不足：需要至少 ${needed} KiB（含升级备份余量）"
}

backup_and_stop() {
    mkdir -p "$ROOT"
    mkdir "$ROOT/.install-lock" 2>/dev/null || fail '另一个安装进程正在使用该目录'
    INSTALL_LOCK=$ROOT/.install-lock
    current_state=none
    if [ -f "$ROOT/.shell-wol-install" ]; then current_state=$(checksum "$ROOT/.shell-wol-install"); fi
    [ "$planned_state" = "$current_state" ] || fail '安装状态在下载期间变化，请重新运行'
    if [ "$BOOT_MODE" = firewall ] || [ "$PRIOR_BOOT_MODE" = firewall ]; then
        verify_boot_paths
        verify_firewall
        mkdir "$BOOT_LOCK" 2>/dev/null || fail '持久启动操作正在进行；遗留启动锁需人工确认或重启清理'
        OWN_BOOT_LOCK=1
        snapshot_firewall
        mkdir -p "$TMP/boot-backup/service"
        for name in boot.sh service/shell-wol.procd .autostart-disabled; do
            if [ -f "$ROOT/$name" ]; then cp "$ROOT/$name" "$TMP/boot-backup/$name"; fi
        done
    fi
    if [ "$SERVICE" != none ] && [ -e "$SERVICE_FILE" ]; then
        [ "$prior_service" = "$SERVICE" ] && [ ! -L "$SERVICE_FILE" ] || fail '下载期间出现冲突服务文件，拒绝覆盖'
        [ "$(checksum "$SERVICE_FILE")" = "$(state_value service_sha256)" ] || fail '下载期间服务文件发生修改，拒绝覆盖'
    fi
    mkdir -p "$BIN" "$DATA" "$ROOT/backup"
    chmod 755 "$ROOT" "$BIN"; chmod 700 "$DATA" "$ROOT/backup"
    if [ -f "$ROOT/.shell-wol-install" ]; then cp "$ROOT/.shell-wol-install" "$TMP/old-state"; OLD_STATE=1; fi
    if [ -f "$BIN/shell-wol" ]; then
        cp "$BIN/shell-wol" "$TMP/old-binary"; OLD_BINARY=1
        if "$TMP/old-binary" lock --help >/dev/null 2>&1; then OLD_SYSTEM_LOCK=1; fi
    fi
    # Read-only application validation precedes any service restoration or stop.
    if [ -f "$DATA/config.yaml" ]; then "$TMP/$artifact" check --data-dir "$DATA" >/dev/null || fail '现有 YAML 配置检查失败'; fi
    TRANSACTION=1
    restore_procd_file
    if [ "$SERVICE" != none ] && [ -f "$SERVICE_FILE" ]; then
        cp "$SERVICE_FILE" "$TMP/old-service"; OLD_SERVICE=1
        if service_active; then WAS_ACTIVE=1
        else status=$?; if [ "$SERVICE" = procd ]; then [ "$status" = 1 ] || fail '无法确认旧 procd 服务状态'; fi; fi
        if service_enabled; then WAS_ENABLED=1; fi
    fi
    ENABLE_BOOT=1
    if [ "$prior_service" != none ] && [ "$WAS_ENABLED" = 0 ]; then ENABLE_BOOT=0; fi
    if [ "$BOOT_MODE" = firewall ] || [ "$PRIOR_BOOT_MODE" = firewall ]; then
        BOOT_FILES_CHANGED=1
        touch "$ROOT/.autostart-disabled"; chmod 600 "$ROOT/.autostart-disabled"
    fi
    if [ "$WAS_ACTIVE" = 1 ]; then service_stop || fail '停止旧服务失败'; fi
    if [ -d "$DATA/.lock" ]; then
        attempts=0
        while [ "$WAS_ACTIVE" = 1 ] && [ -d "$DATA/.lock" ] && [ "$attempts" -lt 10 ]; do sleep 1; attempts=$((attempts+1)); done
        [ ! -d "$DATA/.lock" ] || fail '数据目录存在运行锁；请确认进程状态并人工处理，安装器不会删除锁'
    fi
    if [ -f "$DATA/.lock" ] && [ ! -L "$DATA/.lock" ]; then OLD_LOCK_FILE=1; fi
    if [ "$NEW_SYSTEM_LOCK" = 1 ]; then
        attempts=0
        while ! "$TMP/$artifact" lock --data-dir "$DATA" >"$TMP/lock-status" 2>&1; do
            if [ "$WAS_ACTIVE" = 1 ] && [ "$attempts" -lt 10 ]; then
                attempts=$((attempts+1)); sleep 1
            else cat "$TMP/lock-status" >&2; fail '数据锁不可用；旧版遗留目录需确认无进程后清理一次'; fi
        done
    elif [ -e "$DATA/.lock" ] || [ -L "$DATA/.lock" ]; then
        fail '目标旧版本不支持当前文件锁格式；请先停服并使用兼容版本'
    fi
}

install_program() {
    if [ "$OLD_BINARY" = 1 ]; then cp "$TMP/old-binary" "$ROOT/backup/shell-wol.previous"; chmod 700 "$ROOT/backup/shell-wol.previous"; fi
    cp "$TMP/$artifact" "$BIN/.shell-wol-new"; chmod 755 "$BIN/.shell-wol-new"; mv -f "$BIN/.shell-wol-new" "$BIN/shell-wol"
    if [ ! -f "$DATA/config.yaml" ] && [ ! -f "$DATA/devices.yaml" ]; then
        if [ "$NEW_SYSTEM_LOCK" = 1 ] && { [ "$FIRMWARE" = xiaomi ] || [ "$FIRMWARE" = openwrt ]; }; then
            "$BIN/shell-wol" init --data-dir "$DATA" --log-limit 0 >/dev/null || fail '初始化数据失败'
        else
            "$BIN/shell-wol" init --data-dir "$DATA" >/dev/null || fail '初始化数据失败'
            if [ "$FIRMWARE" = xiaomi ] || [ "$FIRMWARE" = openwrt ]; then sed 's/^log_limit:.*/log_limit: 0/' "$DATA/config.yaml" >"$TMP/config"; cat "$TMP/config" >"$DATA/config.yaml"; fi
        fi
    fi
    "$BIN/shell-wol" check --data-dir "$DATA" >/dev/null || fail '安装后的配置检查失败'
}

install_service() {
    service_hash=
    if [ "$SERVICE" != none ]; then
        prepare_service_user
        render_service
        mkdir -p "$(dirname "$SERVICE_FILE")"
        SERVICE_CHANGED=1
        cp "$TMP/rendered-service" "$SERVICE_FILE"
        if [ "$SERVICE" = procd ]; then chmod 755 "$SERVICE_FILE"; else chmod 644 "$SERVICE_FILE"; fi
        service_reload || fail '服务配置加载失败'
        service_hash=$(checksum "$SERVICE_FILE")
        if [ "$BOOT_MODE" = firewall ]; then
            mkdir -p "$ROOT/service"; chmod 755 "$ROOT/service"
            cp "$TMP/rendered-service" "$ROOT/service/shell-wol.procd"; chmod 755 "$ROOT/service/shell-wol.procd"
            render_boot
            BOOT_NEW=$(mktemp "$ROOT/.boot.sh.XXXXXX")
            cp "$TMP/rendered-boot" "$BOOT_NEW"; chmod 755 "$BOOT_NEW"; mv -f "$BOOT_NEW" "$ROOT/boot.sh"; BOOT_NEW=
            # The include is the only boot entry in this mode.
            "$SERVICE_FILE" disable || fail '清理原生 procd 自启动链接失败'
        fi
        if [ "$BOOT_MODE" = firewall ] || [ "$PRIOR_BOOT_MODE" = firewall ]; then register_firewall || fail '保存 firewall 自启动入口失败'; fi
        if [ "$ENABLE_BOOT" = 1 ]; then service_enable || fail '服务启用失败'
        elif [ "$BOOT_MODE" = native ]; then service_disable || fail '恢复服务禁用状态失败'; fi
        if [ "$prior_service" = none ] || [ "$WAS_ACTIVE" = 1 ]; then
            service_start || fail '服务启动检查失败'
            say '后台服务已启动；Web 接口就绪检查通过。'
        else say '保留旧服务的停止状态。'; fi
    fi
}

save_installation() {
    install_menu
    {
        schema=1; [ "$BOOT_MODE" != firewall ] || schema=2
        printf 'schema=%s\nversion=%s\nservice=%s\nservice_sha256=%s\nboot_mode=%s\n' "$schema" "$VERSION" "$SERVICE" "$service_hash" "$BOOT_MODE"
        printf 'install_mode=%s\n' "$INSTALL_MODE"
        printf 'menu_profile=%s\nmenu_sha256=%s\n' "$MENU_PROFILE" "$(checksum "$ROOT/menu.sh")"
        if [ "$BOOT_MODE" = firewall ]; then printf 'boot_script=%s/boot.sh\nuci_section=ShellWoL\nboot_sha256=%s\n' "$ROOT" "$(checksum "$ROOT/boot.sh")"; fi
    } >"$ROOT/.shell-wol-install.new"
    chmod 600 "$ROOT/.shell-wol-install.new"; mv -f "$ROOT/.shell-wol-install.new" "$ROOT/.shell-wol-install"
    if [ "$BOOT_FILES_CHANGED" = 1 ]; then
        if [ "$BOOT_MODE" = native ]; then
            rm -f "$ROOT/boot.sh" "$ROOT/service/shell-wol.procd" "$ROOT/.autostart-disabled"
            rmdir "$ROOT/service" 2>/dev/null || :
        elif [ "$ENABLE_BOOT" = 1 ]; then rm -f "$ROOT/.autostart-disabled"; fi
    fi
    COMMITTED=1
}

report_result() {
    say "安装完成：$BIN/shell-wol"
    say "数据目录：$DATA"
    say "终端菜单：wol；重新登录或执行 . \"$MENU_PROFILE\" 后生效。"
    say "完整入口：sh \"$ROOT/menu.sh\"（系统安装以 root 运行）。"
    if [ -n "$SERVICE_USER" ] && [ "$USER_MODE" = 0 ]; then
        say "手动运行（先停止后台服务）：sudo -u \"$SERVICE_USER\" \"$BIN/shell-wol\" serve --data-dir \"$DATA\""
    else say "手动运行：\"$BIN/shell-wol\" serve --data-dir \"$DATA\""; fi
    if ! password_ready; then
        say '尚未设置管理员密码：可在浏览器使用本次启动的初始化令牌完成设置。'
        case "$SERVICE" in
            systemd) say '查看令牌：sudo journalctl -u shell-wol -n 30 --no-pager' ;;
            procd)
                if [ "$NEW_SYSTEM_LOCK" = 1 ]; then say "查看令牌：wol token 或 \"$BIN/shell-wol\" token --data-dir \"$DATA\""
                else say '查看令牌：logread -e shell-wol（新版本可通过 wol token 查看）'; fi ;;
            launchd-user|launchd-system) say "查看令牌：$DATA/service.log（系统安装使用 sudo 读取）" ;;
            none) say '手动启动 serve 后，在启动终端查看令牌。' ;;
        esac
        say '也可先停止服务，再以实际数据账户通过标准输入初始化：'
        if [ -n "$SERVICE_USER" ] && [ "$USER_MODE" = 0 ]; then
            say "sudo -u \"$SERVICE_USER\" \"$BIN/shell-wol\" init --data-dir \"$DATA\" --password-stdin"
        else say "\"$BIN/shell-wol\" init --data-dir \"$DATA\" --password-stdin"; fi
    fi
    say '默认仅监听 127.0.0.1:9000。可显式配置 LAN 监听后通过浏览器初始化；安装器不会修改端口放行规则。'
    if [ "$SERVICE" != none ]; then say "服务类型：$SERVICE；服务文件：$SERVICE_FILE"; fi
    if [ "$BOOT_MODE" = firewall ]; then
        say "自启动：firewall.ShellWoL → $ROOT/boot.sh → procd（enabled=$ENABLE_BOOT）"
        say '请在目标固件上验证重启后 UCI include 仍存在；安装器不会自动重启路由器。'
    fi
    if [ "$CUSTOM_REPO" = 0 ] && [ "$SOURCE" = jsdelivr ]; then
        say "卸载脚本：$CDN_BASE/gh/$REPO@main/scripts/uninstall.sh"
    else say "卸载脚本：$REPO_BASE/main/scripts/uninstall.sh"; fi
    say "预览卸载：sh uninstall.sh --install-dir \"$ROOT\" --dry-run（用户安装加 --user）"
}

main() {
    parse_options "$@"
    detect_environment
    plan_install
    download_assets
    backup_and_stop
    install_program
    install_service
    save_installation
    report_result
}

if [ "${SHELL_WOL_INSTALL_LIBRARY:-0}" != 1 ]; then main "$@"; fi
