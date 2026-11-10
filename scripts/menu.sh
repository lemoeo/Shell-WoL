#!/bin/sh
# Shell-WoL terminal menu. Structure inspired by ShellClash's clash.sh.
set -u

error() { printf '错误：%s\n' "$*" >&2; return 1; }
field() { sed -n "s/^$1=//p" "$ROOT/.shell-wol-install"; }
setup() {
    ROOT=$(CDPATH= cd "$(dirname "$0")" && pwd -P)
    BIN=$ROOT/bin/shell-wol; DATA=$ROOT/data
    [ -r "$ROOT/.shell-wol-install" ] || { error '请以原安装账户运行菜单（系统安装使用 root/sudo）'; return 1; }
    SERVICE=$(field service)
    case "$SERVICE" in
        systemd) UNIT=/etc/systemd/system/shell-wol.service ;;
        procd) UNIT=/etc/init.d/shell-wol ;;
        launchd-user) DOMAIN=gui/$(id -u); UNIT=$HOME/Library/LaunchAgents/io.github.lemoeo.shell-wol.plist ;;
        launchd-system) DOMAIN=system; UNIT=/Library/LaunchDaemons/io.github.lemoeo.shell-wol.plist ;;
        none) UNIT= ;;
        *) error '安装记录中的服务类型无效'; return 1 ;;
    esac
}
find_pid() {
    if [ -d /proc/self ]; then
        if command -v pidof >/dev/null 2>&1; then candidates=$(pidof shell-wol 2>/dev/null)
        else candidates=$(printf '%s\n' /proc/[0-9]* | sed 's|/proc/||'); fi
        unreadable=0
        for process in $candidates; do
            executable=$(readlink "/proc/$process/exe" 2>/dev/null) || { [ ! -d "/proc/$process" ] || unreadable=1; continue; }
            case "$executable" in "$BIN"|"$BIN (deleted)") ;; *) continue ;; esac
            if tr '\000' '\n' <"/proc/$process/cmdline" 2>/dev/null | awk -v data="$DATA" '
                NR==2 && $0=="serve" {serve=1}
                prev=="--data-dir" && $0==data {found=1}
                $0=="--data-dir="data {found=1}
                {prev=$0}
                END {exit !(serve && found)}'; then printf '%s\n' "$process"; return 0; fi
        done
        [ "$unreadable" = 0 ] || return 2
    else
        listing=$(ps -axww -o pid= -o command= 2>/dev/null) || return 2
        process=$(printf '%s\n' "$listing" | awk -v cmd="$BIN serve --data-dir $DATA" '
            {pid=$1;sub(/^[[:space:]]*[0-9]+[[:space:]]+/,"")}
            $0==cmd {print pid;exit}')
        [ -z "$process" ] || { printf '%s\n' "$process"; return 0; }
    fi
    return 1
}
status() {
    PID=$(find_pid); result=$?
    RSS=--; WEB=未检查; STATE=已停止
    if [ "$result" = 0 ]; then
        STATE=运行中
        if [ -r "/proc/$PID/status" ]; then RSS=$(awk '/^VmRSS:/{printf "%.1f MiB",$2/1024}' "/proc/$PID/status")
        else RSS=$(ps -p "$PID" -o rss= | awk 'NF{printf "%.1f MiB",$1/1024}'); fi
        if "$BIN" check --data-dir "$DATA" --running >/dev/null 2>&1; then WEB=正常
        else WEB=未就绪; result=2; fi
    elif [ "$result" = 2 ]; then STATE=无法读取进程状态; fi
    printf 'Shell-WoL %s\n运行状态：%s\nPID：%s    内存（RSS）：%s\nWeb：%s    服务：%s\n安装目录：%s\n' \
        "$(field version)" "$STATE" "${PID:---}" "${RSS:---}" "$WEB" "$SERVICE" "$ROOT"
    return "$result"
}
owned_service() {
    [ "$SERVICE" != none ] || { error '尚未配置后台服务，请重新运行安装器并加 --service'; return 1; }
    case "$SERVICE" in systemd|procd|launchd-system)
        [ "$(id -u)" = 0 ] || { error '系统服务管理需要 root 权限'; return 1; } ;; esac
    [ -f "$UNIT" ] && [ ! -L "$UNIT" ] || { error '服务文件缺失；小米镜像固件请先运行安装目录中的 boot.sh run 恢复'; return 1; }
    case "$SERVICE" in
        systemd) grep -Fqx "ExecStart=\"$BIN\" serve --data-dir \"$DATA\"" "$UNIT" ;;
        procd) grep -Fqx "INSTALL_ROOT='$ROOT'" "$UNIT" ;;
        launchd-*) grep -Fq "<string>$BIN</string>" "$UNIT" && grep -Fq "<string>$DATA</string>" "$UNIT" ;;
    esac || { error '服务文件指向其他安装目录'; return 1; }
}
service_command() {
    case "$SERVICE" in
        systemd) systemctl "$1" shell-wol.service ;;
        procd) "$UNIT" "$1" ;;
        launchd-*)
            if [ "$1" = stop ]; then launchctl bootout "$DOMAIN/io.github.lemoeo.shell-wol"
            elif launchctl print "$DOMAIN/io.github.lemoeo.shell-wol" >/dev/null 2>&1; then
                launchctl kickstart "$DOMAIN/io.github.lemoeo.shell-wol"
            else launchctl bootstrap "$DOMAIN" "$UNIT"; fi ;;
    esac
}
start_service() {
    if PID=$(find_pid); then
        "$BIN" check --data-dir "$DATA" --running >/dev/null 2>&1 && { printf '服务已在运行。\n'; return 0; }
    else
        code=$?; [ "$code" = 1 ] || { error '无法确认进程状态'; return 1; }
        if "$BIN" lock --help >/dev/null 2>&1; then
            "$BIN" lock --data-dir "$DATA" || return 1
        else
            [ ! -e "$DATA/.lock" ] && [ ! -L "$DATA/.lock" ] || { error '旧版数据运行锁存在，请先确认进程或升级程序'; return 1; }
        fi
        service_command start || return 1
    fi
    attempts=0
    while [ "$attempts" -lt 15 ]; do
        if PID=$(find_pid) && "$BIN" check --data-dir "$DATA" --running >/dev/null 2>&1; then
            printf '服务已启动，Web 检查通过。\n'; return 0
        fi
        attempts=$((attempts+1)); sleep 1
    done
    error '服务启动后未就绪，请检查服务日志'
}
stop_service() {
    service_command stop || return 1
    attempts=0
    while [ "$attempts" -lt 10 ]; do
        if PID=$(find_pid); then :
        else code=$?; [ "$code" != 1 ] || { printf '服务已停止。\n'; return 0; }
            error '无法确认进程状态'; return 1
        fi
        attempts=$((attempts+1)); sleep 1
    done
    error '程序仍在运行；手动启动的进程请在其终端按 Ctrl+C 停止'
}
control() {
    owned_service || return 1
    mkdir "$ROOT/.install-lock" 2>/dev/null || { error '安装、卸载或服务管理正在进行'; return 1; }
    trap 'rmdir "$ROOT/.install-lock" 2>/dev/null || :' 0
    trap 'exit 130' INT
    trap 'exit 143' HUP TERM
    if [ "$(field boot_mode)" = firewall ]; then
        if ! mkdir /tmp/shell-wol-boot.lock 2>/dev/null; then error '启动恢复正在进行'; return 1; fi
        trap 'rmdir /tmp/shell-wol-boot.lock "$ROOT/.install-lock" 2>/dev/null || :' 0
    fi
    case "$1" in start) start_service ;; stop) stop_service ;; restart) stop_service && start_service ;; esac
}
main() {
    setup || return 1
    case "${1:-menu}" in
        status) status; return $? ;;
        token)
            "$BIN" token --help >/dev/null 2>&1 || { error '当前程序不支持 token 命令，请升级 Go 程序'; return 1; }
            "$BIN" token --data-dir "$DATA"; return $? ;;
        start|stop|restart) control "$1"; return $? ;;
        menu) ;;
        *) error '用法：swol [status|start|restart|stop|token]'; return 2 ;;
    esac
    [ -t 0 ] && [ -t 1 ] || { error '交互菜单需要终端，可使用 swol status'; return 2; }
    while :; do
        printf '\n----------------------------------------\n'
        status || :
        printf '\n1. 启动服务\n2. 重启服务\n3. 停止服务\n4. 刷新状态\n5. 查看初始化令牌\n0. 退出\n请选择：'
        IFS= read -r choice || return 0
        case "$choice" in
            0|'') return 0 ;;
            1|2|3)
                case "$choice" in 1) action=start ;; 2) action=restart ;; 3) action=stop ;; esac
                # A child releases its operation lock before returning to the menu.
                sh "$ROOT/menu.sh" "$action" || : ;;
            4) continue ;;
            5) sh "$ROOT/menu.sh" token || : ;;
            *) printf '无效选择。\n' ;;
        esac
        printf '按回车返回……'; IFS= read -r pause || return 0
    done
}
if [ "${SHELL_WOL_MENU_LIBRARY:-0}" != 1 ]; then main "$@"; fi
