#!/bin/sh
# Standalone, offline uninstaller. Never sources installation metadata.
set -eu
INSTALL_DIR=
USER_MODE=0
PURGE=0
DRY_RUN=0
ASSUME_YES=0
INSTALL_LOCK=
OWN_DATA_LOCK=0
DATA_CREATED=0
STATE_TMP=
BOOT_MODE=native
BOOT_LOCK=/tmp/shell-wol-boot.lock
OWN_BOOT_LOCK=0

say() { printf '%s\n' "$*"; }
fail() { say "错误：$*" >&2; exit 1; }
has() { command -v "$1" >/dev/null 2>&1; }
usage() {
    cat <<'HELP'
Shell-WoL 卸载器
用法：sh uninstall.sh [选项]
  --install-dir PATH   安装根目录；默认按平台推荐
  --user               当前用户级安装
  --purge              同时删除已知 YAML 和本工具日志；未知文件保留
  --dry-run            只预览，不停止服务、不修改文件
  --yes                跳过最终确认，仍执行归属、进程和路径检查
  --help               查看帮助
默认保留 data 和最小安装元数据，可重新安装。不会删除系统账户。
不比较服务文件摘要；仍检查服务命令是否属于本安装目录。
HELP
}
validate_dir() {
    [ "$(printf '%s' "$1" | LC_ALL=C tr -cd '\040-\176')" = "$1" ] || fail '目录不能含控制字符或非 ASCII 字符'
    printf '%s\n' "$1" | LC_ALL=C grep -Eq '^/[A-Za-z0-9_./ +~-]+$' || fail '需要合法的绝对安装路径'
    case "$1" in *//*) fail '目录不能包含重复斜线' ;; esac
    case "$1/" in /|//|*/../*|*/./*) fail '不能使用 / 或包含 . / .. 路径段的目录' ;; esac
    case "$1" in /etc|/usr|/usr/bin|/usr/local|/opt|/data|/tmp|/var|/home|/Users|/Library) fail '必须指定独立安装子目录' ;; esac
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
state_value() { sed -n "s/^$1=//p" "$STATE"; }
require_service_permissions() {
    case "$SERVICE" in systemd|procd|launchd-system) [ "$(id -u)" = 0 ] || fail '系统服务卸载需要 root 权限' ;; esac
}
resolve_service() {
    SERVICE_FILE=; LAUNCH_DOMAIN=
    case "$SERVICE" in
        none) ;;
        systemd) [ "$OS" = Linux ] || fail '服务类型与系统不匹配'; has systemctl || fail '缺少 systemctl'; SERVICE_FILE=/etc/systemd/system/shell-wol.service ;;
        procd) [ "$OS" = Linux ] || fail '服务类型与系统不匹配'; SERVICE_FILE=/etc/init.d/shell-wol ;;
        launchd-user) [ "$OS" = Darwin ] || fail '服务类型与系统不匹配'; has launchctl || fail '缺少 launchctl'; LAUNCH_DOMAIN=gui/$(id -u); SERVICE_FILE="$HOME/Library/LaunchAgents/io.github.lemoeo.shell-wol.plist" ;;
        launchd-system) [ "$OS" = Darwin ] || fail '服务类型与系统不匹配'; has launchctl || fail '缺少 launchctl'; LAUNCH_DOMAIN=system; SERVICE_FILE=/Library/LaunchDaemons/io.github.lemoeo.shell-wol.plist ;;
        *) fail '安装元数据中的服务类型无效' ;;
    esac
}
verify_service() {
    [ "$SERVICE" != none ] || return 0
    [ ! -L "$SERVICE_FILE" ] || fail '拒绝符号链接服务文件'
    if [ -e "$SERVICE_FILE" ]; then
        [ -f "$SERVICE_FILE" ] || fail '服务文件不是普通文件'
        # A copied metadata file must not authorize removal of another installation.
        case "$SERVICE" in
            systemd) grep -Fqx "ExecStart=\"$ROOT/bin/shell-wol\" serve --data-dir \"$DATA\"" "$SERVICE_FILE" || fail '服务命令不属于本安装目录' ;;
            procd) grep -Fqx "INSTALL_ROOT='$ROOT'" "$SERVICE_FILE" || fail '服务不属于本安装目录' ;;
            launchd-user|launchd-system) grep -Fq "<string>$ROOT/bin/shell-wol</string>" "$SERVICE_FILE" && grep -Fq "<string>$DATA</string>" "$SERVICE_FILE" || fail '服务不属于本安装目录' ;;
        esac
    fi
}
verify_firewall() {
    [ "$BOOT_MODE" = firewall ] || return 0
    has uci || fail '清理持久启动入口需要 uci'
    [ "$(state_value boot_script)" = "$ROOT/boot.sh" ] && [ "$(state_value uci_section)" = ShellWoL ] || fail '持久启动元数据不属于本安装'
    uci -q show firewall >/dev/null || fail '无法读取 firewall 配置'
    pending=$(uci -q changes firewall) || fail '无法读取 firewall 待提交配置'
    [ -z "$pending" ] || fail 'firewall 存在未提交修改，请先处理后重试'
    if section=$(uci -q get firewall.ShellWoL); then
        [ "$section" = include ] && [ "$(uci -q get firewall.ShellWoL.path)" = "$ROOT/boot.sh" ] &&
        [ "$(uci -q get firewall.ShellWoL.type)" = script ] || fail 'firewall.ShellWoL 不属于本安装，停止卸载'
    fi
    if [ -f "$ROOT/boot.sh" ]; then
        grep -Fqx "ROOT='$ROOT'" "$ROOT/boot.sh" || fail '启动脚本不属于本安装'
    fi
    if [ -f "$ROOT/service/shell-wol.procd" ]; then
        grep -Fqx "INSTALL_ROOT='$ROOT'" "$ROOT/service/shell-wol.procd" || fail '持久服务不属于本安装'
    fi
}
detach_firewall() {
    [ "$BOOT_MODE" = firewall ] || return 0
    verify_firewall
    # Keep the persistent marker on failure; do not re-enable a partial removal.
    touch "$ROOT/.autostart-disabled"; chmod 600 "$ROOT/.autostart-disabled"
    if uci -q get firewall.ShellWoL >/dev/null; then
        if ! { uci delete firewall.ShellWoL &&
            pending=$(uci -q changes firewall) &&
            ! printf '%s\n' "$pending" | grep -Ev '^$|^[+-]?firewall\.ShellWoL([.=]|$)' >/dev/null &&
            uci commit firewall; }; then
            uci -q revert firewall.ShellWoL || :
            fail '移除 firewall 自启动入口失败；程序与数据保留，自启动标记已禁用'
        fi
    fi
    # Snapshot firmware may already have discarded /etc/init.d. Restore the
    # verified persistent copy so rc.common still queries the correct service name.
    if [ ! -e "$SERVICE_FILE" ]; then
        source_file=$ROOT/service/shell-wol.procd
        [ -f "$source_file" ] && [ "$(checksum "$source_file")" = "$(state_value service_sha256)" ] || fail '服务文件缺失，且持久服务无法通过校验；程序与数据保留'
        mkdir -p "$(dirname "$SERVICE_FILE")"
        restored=$(mktemp "$SERVICE_FILE.new.XXXXXX")
        if cp "$source_file" "$restored" && chmod 755 "$restored" && mv -f "$restored" "$SERVICE_FILE"; then :
        else rm -f "$restored"; fail '恢复服务文件以停止服务失败'; fi
    fi
}
# Return 0 active, 1 inactive, 2 status unknown. Never treat a manager error as stopped.
service_status() {
    case "$SERVICE" in
        none) return 1 ;;
        systemd)
            if unit_state=$(systemctl is-active shell-wol.service 2>/dev/null); then
                case "$unit_state" in active|activating|reloading|deactivating) return 0 ;; *) return 2 ;; esac
            else
                case "$unit_state" in inactive|failed|unknown) return 1 ;; active|activating|reloading|deactivating) return 0 ;; *) return 2 ;; esac
            fi ;;
        procd)
            # OpenWrt's init helper returns 1 for a stopped/not-running instance.
            [ -f "$SERVICE_FILE" ] || return 2
            if "$SERVICE_FILE" running >/dev/null 2>&1; then return 0; else code=$?; [ "$code" = 1 ] && return 1; return 2; fi ;;
        launchd-user|launchd-system)
            launchctl print "$LAUNCH_DOMAIN" >/dev/null 2>&1 || return 2
            if launchctl print "$LAUNCH_DOMAIN/io.github.lemoeo.shell-wol" >/dev/null 2>&1; then return 0; else code=$?; [ "$code" = 113 ] && return 1; return 2; fi ;;
    esac
}
service_stop() {
    case "$SERVICE" in systemd) systemctl stop shell-wol.service ;; procd) "$SERVICE_FILE" stop ;; launchd-user|launchd-system) launchctl bootout "$LAUNCH_DOMAIN/io.github.lemoeo.shell-wol" ;; esac
}
service_disable() {
    case "$SERVICE" in systemd) systemctl disable shell-wol.service ;; procd) "$SERVICE_FILE" disable ;; launchd-user|launchd-system) launchctl disable "$LAUNCH_DOMAIN/io.github.lemoeo.shell-wol" ;; esac
}
service_reload() { [ "$SERVICE" != systemd ] || systemctl daemon-reload; }
remove_owned_enable_links() {
    [ "$SERVICE" = systemd ] || return 0
    has readlink || fail '清理 systemd 启动链接需要 readlink'
    unit_dir=$(dirname "$SERVICE_FILE")
    for link in "$unit_dir"/*.wants/shell-wol.service "$unit_dir"/*.requires/shell-wol.service; do
        [ -L "$link" ] || continue
        target=$(readlink "$link") || fail '无法读取服务启动链接'
        case "$target" in "$SERVICE_FILE"|../shell-wol.service) rm -f "$link" ;; esac
    done
}
# Return 0 stopped, 1 matching executable running, 2 cannot inspect processes.
processes_stopped() {
    if [ -d /proc/self ] && has readlink; then
        readlink "/proc/$$/exe" >/dev/null 2>&1 || return 2
        for entry in /proc/[0-9]*/exe; do
            target=$(readlink "$entry" 2>/dev/null) || continue
            case "$target" in "$ROOT/bin/shell-wol"|"$ROOT/bin/shell-wol (deleted)"|"$ROOT/backup/shell-wol.previous"|"$ROOT/backup/shell-wol.previous (deleted)") return 1 ;; esac
        done
    else
        # BSD/macOS ps. Match complete executable paths, not the generic process name.
        listing=$(ps -axww -o pid= -o command= 2>/dev/null) || return 2
        if printf '%s\n' "$listing" | awk -v first="$ROOT/bin/shell-wol" -v second="$ROOT/backup/shell-wol.previous" -v own="$$" '
            function matches(s,p, n,b,a) {
                n=index(s,p); if (!n) return 0
                b=n==1 ? " " : substr(s,n-1,1); a=substr(s,n+length(p),1)
                return (b ~ /[[:space:]"\047]/ && (a=="" || a ~ /[[:space:]"\047]/))
            }
            $1 != own {sub(/^[[:space:]]*[0-9]+[[:space:]]+/, ""); if (matches($0,first)||matches($0,second)) found=1}
            END {exit !found}'; then return 1; fi
    fi
    return 0
}
check_file() {
    [ ! -L "$1" ] || fail "拒绝符号链接：$1"
    if [ -e "$1" ]; then [ -f "$1" ] || fail "预期普通文件：$1"; fi
}
check_paths() {
    parent=$ROOT
    while [ "$parent" != / ]; do [ ! -L "$parent" ] || fail "路径含符号链接：$parent"; parent=$(dirname "$parent"); done
    for path in "$ROOT/bin" "$DATA" "$ROOT/backup"; do
        [ ! -L "$path" ] || fail "拒绝符号链接目录：$path"
        if [ -e "$path" ]; then [ -d "$path" ] || fail "预期目录：$path"; fi
    done
    check_file "$STATE"
    check_file "$ROOT/bin/shell-wol"
    check_file "$ROOT/backup/shell-wol.previous"
    if [ "$BOOT_MODE" = firewall ]; then
        [ ! -L "$ROOT/service" ] || fail '拒绝符号链接 service 目录'
        if [ -e "$ROOT/service" ]; then [ -d "$ROOT/service" ] || fail 'service 不是目录'; fi
        check_file "$ROOT/boot.sh"
        check_file "$ROOT/service/shell-wol.procd"
        check_file "$ROOT/.autostart-disabled"
    fi
    if [ "$PURGE" = 1 ]; then
        for name in config.yaml devices.yaml events.yaml credentials.yaml credentials.key service.log service.err.log; do check_file "$DATA/$name"; done
    fi
}
confirm() {
    [ "$ASSUME_YES" = 1 ] && return 0
    [ -r /dev/tty ] && [ -w /dev/tty ] || fail '无交互终端；请先 --dry-run 核对，再显式使用 --yes'
    if [ "$PURGE" = 1 ]; then prompt='确认卸载并清除列出的配置、设备及记录？'; else prompt='确认卸载程序和服务并保留数据？'; fi
    printf '%s [y/N] ' "$prompt" >/dev/tty
    IFS= read -r answer </dev/tty || return 1
    case "$answer" in y|Y|yes|YES) return 0 ;; *) return 1 ;; esac
}
cleanup() {
    code=$?
    trap - 0 HUP INT TERM
    if [ -n "$STATE_TMP" ]; then rm -f "$STATE_TMP"; fi
    if [ "$OWN_BOOT_LOCK" = 1 ]; then rmdir "$BOOT_LOCK" 2>/dev/null || :; fi
    if [ "$OWN_DATA_LOCK" = 1 ]; then rmdir "$DATA/.lock" 2>/dev/null || :; fi
    if [ "$DATA_CREATED" = 1 ]; then rmdir "$DATA" 2>/dev/null || :; fi
    if [ -n "$INSTALL_LOCK" ]; then rmdir "$INSTALL_LOCK" 2>/dev/null || :; fi
    if [ -n "${ROOT:-}" ]; then rmdir "$ROOT" 2>/dev/null || :; fi
    exit "$code"
}
main() {
    while [ "$#" -gt 0 ]; do
        case "$1" in
            --help|-h) usage; return ;;
            --install-dir) [ "$#" -ge 2 ] || fail '--install-dir 缺少参数'; INSTALL_DIR=$2; shift 2 ;;
            --user) USER_MODE=1; shift ;;
            --purge) PURGE=1; shift ;;
            --dry-run) DRY_RUN=1; shift ;;
            --yes|-y) ASSUME_YES=1; shift ;;
            *) fail "未知参数：$1" ;;
        esac
    done
    OS=$(uname -s)
    case "$OS" in Linux|Darwin) ;; *) fail '仅支持 macOS、Linux 和兼容路由器固件' ;; esac
    if [ "$OS" = Darwin ] && [ "$(id -u)" != 0 ]; then USER_MODE=1; fi
    if [ -z "$INSTALL_DIR" ]; then
        if [ "$USER_MODE" = 1 ]; then
            if [ "$OS" = Darwin ]; then INSTALL_DIR="$HOME/Library/Application Support/Shell-WoL"; else INSTALL_DIR="${XDG_DATA_HOME:-$HOME/.local/share}/shell-wol"; fi
        elif [ "$OS" = Darwin ]; then INSTALL_DIR='/Library/Application Support/Shell-WoL'
        elif [ -f /data/etc/crontabs/root ] && [ -w /data ]; then INSTALL_DIR=/data/shell-wol
        elif [ -f /etc/openwrt_release ] || { [ -f /etc/rc.common ] && [ -f /lib/functions/procd.sh ]; }; then INSTALL_DIR=/etc/shell-wol
        else INSTALL_DIR=/opt/shell-wol; fi
    fi
    ROOT=${INSTALL_DIR%/}; validate_dir "$ROOT"
    DATA=$ROOT/data; STATE=$ROOT/.shell-wol-install
    if [ ! -e "$ROOT" ] && [ ! -L "$ROOT" ]; then say "目录不存在，无需卸载：$ROOT"; return; fi
    check_paths
    [ -f "$STATE" ] || fail '缺少安装器元数据，拒绝处理未知目录；手动安装需人工迁移'
    schema=$(state_value schema)
    BOOT_MODE=$(state_value boot_mode); BOOT_MODE=${BOOT_MODE:-native}
    case "$schema:$BOOT_MODE" in 1:native|2:firewall) ;; *) fail '不支持的安装元数据版本或启动模式' ;; esac
    for field in version last_version; do
        value=$(state_value "$field")
        if [ "$field" = last_version ] && [ -z "$value" ]; then continue; fi
        if [ "$value" != uninstalled ]; then
            [ "$(printf '%s' "$value" | LC_ALL=C tr -cd 'A-Za-z0-9.-')" = "$value" ] && printf '%s\n' "$value" | LC_ALL=C grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9]+([.-][A-Za-z0-9]+)*)?$' || fail '安装元数据中的版本无效'
        fi
    done
    SERVICE=$(state_value service)
    case "$SERVICE" in none|systemd|procd|launchd-user|launchd-system) ;; *) fail '安装元数据中的服务类型无效' ;; esac
    if [ "$BOOT_MODE" = firewall ]; then [ "$SERVICE" = procd ] || fail '持久启动元数据的服务类型无效'; fi
    check_paths
    resolve_service
    if [ "$DRY_RUN" = 0 ]; then
        [ "$USER_MODE" = 1 ] || [ "$(id -u)" = 0 ] || fail '系统安装卸载需要 root；用户安装请用 --user'
        require_service_permissions
        [ -w "$ROOT" ] || fail '安装目录不可写'
    fi
    require_sha256 || fail '无法执行 SHA256 校验，卸载已停止'
    verify_service
    verify_firewall
    planned_state=$(checksum "$STATE") || fail '读取安装元数据失败'
    say "安装目录：$ROOT"
    say "服务类型：$SERVICE${SERVICE_FILE:+；服务文件：$SERVICE_FILE}"
    if [ "$BOOT_MODE" = firewall ]; then say "移除自启动：firewall.ShellWoL、$ROOT/boot.sh、$ROOT/service/shell-wol.procd"; fi
    say "删除程序：$ROOT/bin/shell-wol"
    say "删除旧程序：$ROOT/backup/shell-wol.previous"
    if [ "$PURGE" = 1 ]; then
        say '清除以下数据文件（存在时）：'
        for name in config.yaml devices.yaml events.yaml credentials.yaml credentials.key service.log service.err.log; do say "  $DATA/$name"; done
    else say "保留数据与最小安装元数据：$DATA"; fi
    say '未知文件和系统账户始终保留。'
    [ "$DRY_RUN" = 0 ] || { say '预览完成，未执行任何更改。'; return; }
    confirm || fail '卸载已取消'
    mkdir "$ROOT/.install-lock" 2>/dev/null || fail '另一个安装/卸载进程正在使用该目录；遗留锁需人工确认'
    INSTALL_LOCK=$ROOT/.install-lock
    trap cleanup 0
    trap 'exit 130' INT
    trap 'exit 143' HUP TERM
    check_paths
    [ "$(checksum "$STATE")" = "$planned_state" ] || fail '安装状态已变化，请重新预览'
    verify_service
    if [ "$BOOT_MODE" = firewall ]; then
        mkdir "$BOOT_LOCK" 2>/dev/null || fail '持久启动操作正在进行；遗留启动锁需人工确认或重启清理'
        OWN_BOOT_LOCK=1
        detach_firewall
        verify_service
    fi
    if service_status; then active=1; else status=$?; [ "$status" = 1 ] || fail '无法确认服务状态，停止卸载'; active=0; fi
    if [ "$active" = 1 ]; then
        [ -f "$SERVICE_FILE" ] || fail '服务仍在运行但服务文件缺失，请先人工处理'
        service_stop || fail '停止服务失败，程序和数据未删除'
    fi
    attempts=0
    while :; do
        if service_status; then status=0; else status=$?; fi
        [ "$status" != 2 ] || fail '无法确认服务已停止，程序和数据未删除'
        [ "$status" = 1 ] && break
        [ "$attempts" -lt 10 ] || fail '服务仍在运行，程序和数据未删除'
        attempts=$((attempts+1)); sleep 1
    done
    attempts=0
    while [ -e "$DATA/.lock" ] && [ "$active" = 1 ] && [ "$attempts" -lt 10 ]; do attempts=$((attempts+1)); sleep 1; done
    [ ! -e "$DATA/.lock" ] && [ ! -L "$DATA/.lock" ] || fail '数据运行锁仍存在；需人工确认进程状态，卸载器不会删除它'
    if [ ! -d "$DATA" ]; then mkdir "$DATA"; DATA_CREATED=1; fi
    mkdir "$DATA/.lock" 2>/dev/null || fail '数据目录被进程占用'
    OWN_DATA_LOCK=1
    if processes_stopped; then :; else code=$?; [ "$code" != 1 ] || fail '检测到本安装的手动运行进程，先停止该进程；不会自动杀进程'; fail '无法检查进程状态，停止卸载'; fi
    check_paths
    verify_service
    if [ "$SERVICE" != none ]; then
        # A missing inactive unit is tolerated when retrying an interrupted removal.
        if [ -f "$SERVICE_FILE" ]; then
            service_disable || fail '禁用后台服务失败，程序和数据未删除'
            rm -f "$SERVICE_FILE"
        else
            remove_owned_enable_links
        fi
        service_reload || fail '服务文件已移除，但重新加载失败；程序和数据保留，可重试'
    fi
    # Record the detached service before deleting programs, making retries safe.
    previous=$(state_value last_version)
    if [ -z "$previous" ]; then previous=$(state_value version); fi
    STATE_TMP=$(mktemp "$ROOT/.uninstall-state.XXXXXX")
    printf 'schema=1\nversion=uninstalled\nservice=none\nservice_sha256=\nstatus=uninstalled\nlast_version=%s\n' "$previous" >"$STATE_TMP"
    chmod 600 "$STATE_TMP"; mv -f "$STATE_TMP" "$STATE"; STATE_TMP=
    if [ "$BOOT_MODE" = firewall ]; then
        rm -f "$ROOT/boot.sh" "$ROOT/service/shell-wol.procd" "$ROOT/.autostart-disabled"
        rmdir "$ROOT/service" 2>/dev/null || :
    fi
    rm -f "$ROOT/bin/shell-wol" "$ROOT/backup/shell-wol.previous"
    if [ "$PURGE" = 1 ]; then
        for name in config.yaml devices.yaml events.yaml credentials.yaml credentials.key service.log service.err.log; do rm -f "$DATA/$name"; done
        rm -f "$STATE"
    fi
    rmdir "$DATA/.lock"; OWN_DATA_LOCK=0
    if [ "$PURGE" = 1 ] || [ "$DATA_CREATED" = 1 ]; then rmdir "$DATA" 2>/dev/null || :; DATA_CREATED=0; fi
    rmdir "$ROOT/bin" "$ROOT/backup" 2>/dev/null || :
    say '程序及受管理后台服务已卸载。'
    if [ "$PURGE" = 0 ]; then say "数据保留于 $DATA；重新安装时指定 --install-dir \"$ROOT\"。"; fi
    remaining=$(find "$ROOT" ! -path "$ROOT" ! -path "$ROOT/.install-lock" -print) || fail '无法列出剩余文件，请检查目录'
    if [ -n "$remaining" ]; then say '保留的文件：'; printf '%s\n' "$remaining"; fi
}
if [ "${SHELL_WOL_UNINSTALL_LIBRARY:-0}" != 1 ]; then main "$@"; fi
