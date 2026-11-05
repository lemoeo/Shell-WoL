#!/bin/sh
# Shell-WoL installer. Requires POSIX sh; never sources downloaded metadata.
set -eu

REPO=lemoeo/Shell-WoL
DOWNLOAD_BASE=https://github.com/$REPO/releases
VERSION=latest
ARCH=
INSTALL_DIR=
USER_MODE=0
WANT_SERVICE=0
ASSUME_YES=0
TMP=
INSTALL_LOCK=
TRANSACTION=0
OLD_BINARY=0
OLD_STATE=0
OLD_SERVICE=0
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
  --yes                接受已确定的默认选项，不猜测未知架构或覆盖冲突
  --download-base URL  HTTPS Release 地址，默认 GitHub Releases
  --help               显示帮助
安装器不接收密码，不修改 LAN 监听、防火墙或厂商启动钩子。
HELP
}
valid_version() { [ "$(printf '%s' "$1" | LC_ALL=C tr -cd 'A-Za-z0-9.-')" = "$1" ] && printf '%s\n' "$1" | LC_ALL=C grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9]+([.-][A-Za-z0-9]+)*)?$'; }
validate_dir() {
    [ "$(printf '%s' "$1" | LC_ALL=C tr -cd '\040-\176')" = "$1" ] || fail '安装路径不能含控制字符或非 ASCII 字符'
    # Restrict characters so paths can be represented consistently in all templates.
    printf '%s\n' "$1" | LC_ALL=C grep -Eq '^/[A-Za-z0-9_./ +~-]+$' || fail '安装目录须为绝对路径，仅支持英文、数字、空格及 _ . / + ~ -'
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
checksum() {
    if has sha256sum; then sha256sum "$1" | awk '{print $1}'
    elif has shasum; then shasum -a 256 "$1" | awk '{print $1}'
    else return 1; fi
}
fetch() {
    fetch_url=$1; fetch_out=$2
    case "$fetch_url" in https://*) ;; *) fail '下载仅允许 HTTPS' ;; esac
    if has curl; then
        curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' --connect-timeout 15 --max-time 180 --output "$fetch_out" "$fetch_url"
    else
        # Some old BusyBox wget builds cannot verify certificates: reject those.
        wget --https-only -q -T 180 -O "$fetch_out" "$fetch_url"
    fi
}
verify_asset() {
    asset=$1
    expected=$(awk -v name="$asset" '$2 == name {print $1}' "$TMP/SHA256SUMS")
    [ "$(printf '%s\n' "$expected" | wc -l | tr -d ' ')" = 1 ] || fail "校验项重复：$asset"
    printf '%s\n' "$expected" | LC_ALL=C grep -Eq '^[0-9a-f]{64}$' || fail "缺少或无效的校验项：$asset"
    actual=$(checksum "$TMP/$asset") || fail 'SHA256 计算失败'
    [ "$actual" = "$expected" ] || fail "SHA256 校验失败：$asset"
}
xml_escape() { printf '%s' "$1" | sed 's/\&/\&amp;/g; s/</\&lt;/g; s/>/\&gt;/g; s/"/\&quot;/g'; }
state_value() { sed -n "s/^$1=//p" "$ROOT/.shell-wol-install"; }
service_active() {
    case "$SERVICE" in
        systemd) systemctl is-active --quiet shell-wol.service ;;
        procd) "$SERVICE_FILE" running >/dev/null 2>&1 ;;
        launchd-user|launchd-system) launchctl print "$LAUNCH_DOMAIN/io.github.lemoeo.shell-wol" >/dev/null 2>&1 ;;
        *) return 1 ;;
    esac
}
service_enabled() {
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
        launchd-user|launchd-system) launchctl bootstrap "$LAUNCH_DOMAIN" "$SERVICE_FILE" ;;
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
    if [ "$SERVICE_CHANGED" = 1 ]; then
        service_stop >/dev/null 2>&1 || :
        service_disable >/dev/null 2>&1 || :
        if [ "$OLD_SERVICE" = 1 ]; then cp "$TMP/old-service" "$SERVICE_FILE" || say '警告：服务文件恢复失败' >&2
        else rm -f "$SERVICE_FILE"; fi
        service_reload >/dev/null 2>&1 || :
    fi
    if [ "$OLD_BINARY" = 1 ]; then
        cp "$TMP/old-binary" "$BIN/.shell-wol-restore" && chmod 755 "$BIN/.shell-wol-restore" && mv -f "$BIN/.shell-wol-restore" "$BIN/shell-wol" || say '警告：旧程序恢复失败' >&2
    else rm -f "$BIN/shell-wol"; fi
    if [ "$OLD_STATE" = 1 ]; then cp "$TMP/old-state" "$ROOT/.shell-wol-install" || say '警告：安装状态恢复失败' >&2
    else
        # Retain ownership of a fresh, partially initialized directory for retry.
        printf 'schema=1\nversion=uninstalled\nservice=none\nservice_sha256=\n' >"$ROOT/.shell-wol-install"
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
    if [ -n "$INSTALL_LOCK" ]; then rmdir "$INSTALL_LOCK" 2>/dev/null || :; fi
    if [ -n "$TMP" ]; then rm -rf "$TMP"; fi
    exit "$result"
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
            if [ -f /etc/rc.common ] && [ "$(cat /proc/1/comm 2>/dev/null)" = procd ]; then SERVICE=procd; SERVICE_FILE=/etc/init.d/shell-wol
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
main() {
    while [ "$#" -gt 0 ]; do
        case "$1" in
            --help|-h) usage; return ;;
            --version|--arch|--install-dir|--download-base)
                [ "$#" -ge 2 ] || fail "$1 缺少参数"
                case "$1" in --version) VERSION=$2 ;; --arch) ARCH=$2 ;; --install-dir) INSTALL_DIR=$2 ;; --download-base) DOWNLOAD_BASE=$2 ;; esac; shift 2 ;;
            --user) USER_MODE=1; shift ;;
            --service) WANT_SERVICE=1; shift ;;
            --yes|-y) ASSUME_YES=1; shift ;;
            *) fail "未知参数：$1" ;;
        esac
    done
    case "$(uname -s)" in Linux) OS=linux; FIRMWARE=$(detect_firmware) ;; Darwin) OS=darwin; FIRMWARE=macos ;; *) fail '仅支持 macOS 和兼容 Linux 固件' ;; esac
    if [ -z "$ARCH" ]; then ARCH=$(detect_arch "$(uname -m)") || fail '无法可靠识别 CPU，请用 --arch 指定'; fi
    case "$ARCH" in amd64|arm64|armv7|mips|mipsle) ;; *) fail "不支持的架构：$ARCH" ;; esac
    if [ "$OS" = darwin ]; then case "$ARCH" in amd64|arm64) ;; *) fail 'macOS 仅支持 amd64/arm64' ;; esac; fi
    if [ "$VERSION" != latest ]; then valid_version "$VERSION" || fail '版本格式须为 v主版本.次版本.补丁[-预发布标识]'; fi
    case "$DOWNLOAD_BASE" in https://*) ;; *) fail '--download-base 必须是 HTTPS 地址' ;; esac
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
    [ "$USER_MODE" = 1 ] || [ "$(id -u)" = 0 ] || fail '系统安装需要 root；可使用 --user 或由管理员执行'
    case "$INSTALL_DIR/" in /tmp/*|/var/run/*|/run/*|/sys/*|/proc/*|/dev/*) fail '安装根目录必须使用持久化存储' ;; esac
    has curl || has wget || fail '需要 curl 或 wget'
    has sha256sum || has shasum || fail '需要 sha256sum 或 shasum'
    if ! has curl; then wget --help 2>&1 | grep -q -- '--https-only' || fail '需要支持 --https-only 的 wget；精简固件请先安装 curl 和 CA 证书'; fi
    say "系统：$OS；固件类型：$FIRMWARE；CPU：$ARCH"
    say "安装根目录：$INSTALL_DIR"
    if [ "$ASSUME_YES" = 0 ]; then
        [ -r /dev/tty ] && [ -w /dev/tty ] || fail '无交互终端；检查参数后使用 --yes'
        printf '回车使用推荐目录，或输入其他绝对路径：' >/dev/tty
        IFS= read -r chosen </dev/tty || fail '无法读取目录选择'
        if [ -n "$chosen" ]; then INSTALL_DIR=${chosen%/}; validate_dir "$INSTALL_DIR"; fi
        if [ "$WANT_SERVICE" = 0 ] && confirm '是否配置后台服务？'; then WANT_SERVICE=1; fi
    fi
    case "$INSTALL_DIR/" in /tmp/*|/var/run/*|/run/*|/sys/*|/proc/*|/dev/*) fail '安装根目录必须使用持久化存储' ;; esac
    confirm "安装到 $INSTALL_DIR？" || fail '安装已取消'
    # Refuse symlinked directories before making any installation changes.
    parent=$INSTALL_DIR
    while [ "$parent" != / ]; do [ ! -L "$parent" ] || fail "路径含符号链接：$parent"; parent=$(dirname "$parent"); done
    if [ -d "$INSTALL_DIR" ] && [ -n "$(ls -A "$INSTALL_DIR")" ] && [ ! -f "$INSTALL_DIR/.shell-wol-install" ]; then fail '目标目录非空且不是安装器管理的目录；请选择新目录，旧数据请手动迁移'; fi
    ROOT=$INSTALL_DIR; BIN=$ROOT/bin; DATA=$ROOT/data
    for path in "$ROOT/.shell-wol-install" "$BIN" "$DATA" "$ROOT/backup" "$BIN/shell-wol"; do [ ! -L "$path" ] || fail "安装路径不能是符号链接：$path"; done
    if [ -d "$DATA" ] && [ -n "$(find "$DATA" -type l -print)" ]; then fail '数据目录含符号链接，需先人工检查'; fi
    SERVICE=none; SERVICE_FILE=; SERVICE_USER=; SERVICE_GROUP=; LAUNCH_DOMAIN=
    planned_state=none
    if [ -f "$ROOT/.shell-wol-install" ]; then
        planned_state=$(checksum "$ROOT/.shell-wol-install")
        [ "$(state_value schema)" = 1 ] || fail '不支持的安装状态文件'
        prior_service=$(state_value service)
        case "$prior_service" in none) ;; systemd|procd|launchd-user|launchd-system) WANT_SERVICE=1 ;; *) fail '无效的安装状态文件' ;; esac
    else prior_service=none; fi
    if [ "$WANT_SERVICE" = 1 ]; then
        choose_service
        [ "$prior_service" = none ] || [ "$prior_service" = "$SERVICE" ] || fail '服务类型与旧安装不同，请先人工迁移'
        if [ -e "$SERVICE_FILE" ]; then
            [ ! -L "$SERVICE_FILE" ] || fail '服务文件不能是符号链接'
            [ "$prior_service" = "$SERVICE" ] || fail "服务文件已存在，拒绝覆盖：$SERVICE_FILE"
            old_hash=$(state_value service_sha256)
            [ "$(checksum "$SERVICE_FILE")" = "$old_hash" ] || fail '服务文件被手动修改，拒绝覆盖；请先备份并人工合并'
        fi
    fi
    TMP=$(mktemp -d "${TMPDIR:-/tmp}/shell-wol-install.XXXXXX") || fail '无法创建临时目录'
    trap cleanup 0
    trap 'exit 130' INT
    trap 'exit 143' HUP TERM
    if [ "$VERSION" = latest ]; then fetch "$DOWNLOAD_BASE/latest/download/VERSION" "$TMP/VERSION" || fail '无法获取版本信息'; VERSION=$(cat "$TMP/VERSION"); valid_version "$VERSION" || fail '远端版本信息无效'; fi
    say "下载固定版本：$VERSION"
    release_url=$DOWNLOAD_BASE/download/$VERSION
    fetch "$release_url/SHA256SUMS" "$TMP/SHA256SUMS" || fail '无法下载校验文件'
    case "$ARCH" in armv7) suffix=arm-7 ;; mips|mipsle) suffix=$ARCH-softfloat ;; *) suffix=$ARCH ;; esac
    artifact=shell-wol-$OS-$suffix
    fetch "$release_url/$artifact" "$TMP/$artifact" || fail '二进制下载失败'
    verify_asset "$artifact"
    chmod 755 "$TMP/$artifact"
    [ "$("$TMP/$artifact" version)" = "$VERSION" ] || fail '二进制无法运行或版本不匹配'
    if [ "$SERVICE" != none ]; then
        case "$SERVICE" in systemd) template=shell-wol.service ;; procd) template=shell-wol.openwrt ;; *) template=shell-wol.plist ;; esac
        fetch "$release_url/$template" "$TMP/$template" || fail '服务模板下载失败'; verify_asset "$template"
    fi
    existing=$ROOT
    while [ ! -d "$existing" ]; do existing=$(dirname "$existing"); done
    [ -w "$existing" ] || fail "目录不可写：$existing"
    needed=$(du -k "$TMP/$artifact" | awk '{print $1}'); needed=$((needed * 3 + 2048))
    available=$(df -Pk "$existing" | awk 'END {print $4}')
    case "$available" in ''|*[!0-9]*) fail '无法确定剩余空间' ;; esac
    [ "$available" -ge "$needed" ] || fail "空间不足：需要至少 ${needed} KiB（含升级备份余量）"
    mkdir -p "$ROOT"
    mkdir "$ROOT/.install-lock" 2>/dev/null || fail '另一个安装进程正在使用该目录'
    INSTALL_LOCK=$ROOT/.install-lock
    current_state=none
    if [ -f "$ROOT/.shell-wol-install" ]; then current_state=$(checksum "$ROOT/.shell-wol-install"); fi
    [ "$planned_state" = "$current_state" ] || fail '安装状态在下载期间变化，请重新运行'
    if [ "$SERVICE" != none ] && [ -e "$SERVICE_FILE" ]; then
        [ "$prior_service" = "$SERVICE" ] && [ ! -L "$SERVICE_FILE" ] || fail '下载期间出现冲突服务文件，拒绝覆盖'
        [ "$(checksum "$SERVICE_FILE")" = "$(state_value service_sha256)" ] || fail '下载期间服务文件发生修改，拒绝覆盖'
    fi
    mkdir -p "$BIN" "$DATA" "$ROOT/backup"
    chmod 755 "$ROOT" "$BIN"; chmod 700 "$DATA" "$ROOT/backup"
    if [ -f "$ROOT/.shell-wol-install" ]; then cp "$ROOT/.shell-wol-install" "$TMP/old-state"; OLD_STATE=1; fi
    if [ -f "$BIN/shell-wol" ]; then cp "$BIN/shell-wol" "$TMP/old-binary"; OLD_BINARY=1; fi
    if [ "$SERVICE" != none ] && [ -f "$SERVICE_FILE" ]; then
        cp "$SERVICE_FILE" "$TMP/old-service"; OLD_SERVICE=1
        if service_active; then WAS_ACTIVE=1; fi
        if service_enabled; then WAS_ENABLED=1; fi
    fi
    # Read-only application check diagnoses invalid YAML before stopping services.
    if [ -f "$DATA/config.yaml" ]; then "$TMP/$artifact" check --data-dir "$DATA" >/dev/null || fail '现有 YAML 配置检查失败'; fi
    TRANSACTION=1
    if [ "$WAS_ACTIVE" = 1 ]; then service_stop || fail '停止旧服务失败'; fi
    if [ -d "$DATA/.lock" ]; then
        attempts=0
        while [ "$WAS_ACTIVE" = 1 ] && [ -d "$DATA/.lock" ] && [ "$attempts" -lt 10 ]; do sleep 1; attempts=$((attempts+1)); done
        [ ! -d "$DATA/.lock" ] || fail '数据目录存在运行锁；请确认进程状态并人工处理，安装器不会删除锁'
    fi
    if [ "$OLD_BINARY" = 1 ]; then cp "$TMP/old-binary" "$ROOT/backup/shell-wol.previous"; chmod 700 "$ROOT/backup/shell-wol.previous"; fi
    cp "$TMP/$artifact" "$BIN/.shell-wol-new"; chmod 755 "$BIN/.shell-wol-new"; mv -f "$BIN/.shell-wol-new" "$BIN/shell-wol"
    if [ ! -f "$DATA/config.yaml" ] && [ ! -f "$DATA/devices.yaml" ]; then
        "$BIN/shell-wol" init --data-dir "$DATA" >/dev/null || fail '初始化数据失败'
        if [ "$FIRMWARE" = xiaomi ] || [ "$FIRMWARE" = openwrt ]; then sed 's/^log_limit:.*/log_limit: 0/' "$DATA/config.yaml" >"$TMP/config"; cat "$TMP/config" >"$DATA/config.yaml"; fi
    fi
    "$BIN/shell-wol" check --data-dir "$DATA" >/dev/null || fail '安装后的配置检查失败'
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
        service_enable || fail '服务启用失败'
        service_start || fail '服务启动检查失败'
        say '后台服务已启动；Web 接口就绪检查通过。'
    fi
    {
        printf 'schema=1\nversion=%s\nservice=%s\nservice_sha256=%s\n' "$VERSION" "$SERVICE" "$service_hash"
    } >"$ROOT/.shell-wol-install.new"
    chmod 600 "$ROOT/.shell-wol-install.new"; mv -f "$ROOT/.shell-wol-install.new" "$ROOT/.shell-wol-install"
    COMMITTED=1
    say "安装完成：$BIN/shell-wol"
    say "数据目录：$DATA"
    if [ -n "$SERVICE_USER" ] && [ "$USER_MODE" = 0 ]; then
        say "手动运行（先停止后台服务）：sudo -u \"$SERVICE_USER\" \"$BIN/shell-wol\" serve --data-dir \"$DATA\""
    else say "手动运行：\"$BIN/shell-wol\" serve --data-dir \"$DATA\""; fi
    if ! password_ready; then
        say '尚未设置管理员密码：可在浏览器使用本次启动的初始化令牌完成设置。'
        case "$SERVICE" in
            systemd) say '查看令牌：sudo journalctl -u shell-wol -n 30 --no-pager' ;;
            procd) say '查看令牌：logread -e shell-wol' ;;
            launchd-user|launchd-system) say "查看令牌：$DATA/service.log（系统安装使用 sudo 读取）" ;;
            none) say '手动启动 serve 后，在启动终端查看令牌。' ;;
        esac
        say '也可先停止服务，再以实际数据账户通过标准输入初始化：'
        if [ -n "$SERVICE_USER" ] && [ "$USER_MODE" = 0 ]; then
            say "sudo -u \"$SERVICE_USER\" \"$BIN/shell-wol\" init --data-dir \"$DATA\" --password-stdin"
        else say "\"$BIN/shell-wol\" init --data-dir \"$DATA\" --password-stdin"; fi
    fi
    say '默认仅监听 127.0.0.1:8080。可显式配置 LAN 监听后通过浏览器初始化；安装器不会修改防火墙。'
    if [ "$SERVICE" != none ]; then say "服务类型：$SERVICE；服务文件：$SERVICE_FILE"; fi
}

if [ "${SHELL_WOL_INSTALL_LIBRARY:-0}" != 1 ]; then main "$@"; fi
