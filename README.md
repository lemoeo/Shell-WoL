# Shell-WoL

使用 Go 实现的单文件 Wake-on-LAN 工具：内置中文 Web 管理界面，也可在终端独立发送唤醒包。配置、设备与有限操作记录均存为 YAML，不依赖数据库、Node.js 或外部 CDN。

## 已实现

- 设备增删改查、搜索、一键与批量唤醒。
- MAC 校验、UDP 魔术包、重复发送、IPv4 网卡选择与广播地址建议。
- 按需 TCP 上线检测；不自动扫描网络。
- 首次密码设置、bcrypt 哈希、12 小时会话、CSRF 校验、登录与操作限流。
- Web 设置、设备 YAML 导出、最多 1000 条操作记录（默认 100 条）。
- YAML 注释及未知扩展字段尽量保留；保存时统一排版。
- macOS、Windows、Linux，以及面向 OpenWrt 和兼容二次开发固件的交叉编译脚本与 procd 部署示例。

## 平台范围

桌面与服务器面向 macOS、Windows 和 Linux。路由器面向 **OpenWrt 及保留所需运行能力的二次开发固件**，具体条件与步骤见 [OpenWrt 部署指南](docs/openwrt.md)。

小米、网件等是设备品牌，不能仅凭品牌或“基于 OpenWrt”判断兼容：不同型号、固件版本可能限制 SSH、自定义程序或服务管理。需要允许运行匹配架构的二进制，且 CPU、内核和可用资源满足要求；现有开机启动脚本还要求 `/etc/rc.common` 和 procd。当前提供构建产物与部署示例，尚未完成路由器实机验证，不保证所有型号或原厂固件均兼容。

## 本地构建与启动

需要 Go 1.23 或更新版本。使用项目现有 checkout，无需创建 Git worktree。

```sh
go mod download
go build -trimpath -o bin/shell-wol ./cmd/shell-wol
./bin/shell-wol serve --data-dir ./data
```

Windows 将程序名换为 `shell-wol.exe`。直接运行 `serve` 会创建 `data/config.yaml` 和 `data/devices.yaml`，默认监听 `127.0.0.1:8080`。在本机浏览器打开该地址，输入终端显示的初始化令牌并设置密码（12–72 字节）。令牌只用于首次初始化，不会通过 Web 接口返回。请勿公开启动终端输出。

### 局域网访问与无本机浏览器的部署

无本机浏览器时，先使用标准输入设置管理员密码，再开放监听：

```sh
# Bash 示例；密码不放入命令行参数或 Shell 历史
read -r -s -p '管理员密码: ' wol_password
printf '\n'
printf '%s' "$wol_password" | ./bin/shell-wol init --data-dir ./data --password-stdin
unset wol_password
./bin/shell-wol serve --data-dir ./data --listen 0.0.0.0:8080
```

OpenWrt 的 BusyBox Shell 可用 `read -r -s wol_password`。PowerShell 可通过交互式安全输入转为标准输入传给程序，避免将密码直接写进命令历史。

没有管理员密码时，程序拒绝非回环地址监听。`--listen` 是本次覆盖，不会写入配置；Web 设置中的监听地址在重启后生效。程序不自动改防火墙，请仅放行可信 LAN，勿开放到 WAN。

局域网 HTTP 不加密。推荐在可信网络内使用；需要远程访问时使用 VPN，或终止 TLS 的可信 HTTPS 反向代理。反向代理需保留原始 Host；将 `public_url` 设置为实际外部地址（如 `https://wol.example.com`），以校验浏览器 Origin 并启用 Secure Cookie。应用不信任 `X-Forwarded-*`，不要把后端端口直接暴露给外部。目标电脑需接通电源、启用 BIOS/UEFI 和操作系统的 WoL，通常需有线连接。跨 VLAN、客户端隔离和外网广播不在首版支持范围内。

## CLI

```sh
# 查看本机网卡及对应广播地址
./bin/shell-wol interfaces

# 指定 MAC 直接发送，无需管理员登录或运行 Web 服务
./bin/shell-wol wake --mac AA:BB:CC:DD:EE:FF --broadcast 192.168.1.255

# 读取 Web 已保存的设备；可与 Web 服务同时使用
./bin/shell-wol wake --data-dir ./data --device desktop

# 多网卡时显式选择来源；网卡名称以 interfaces 输出为准
./bin/shell-wol wake --mac AA:BB:CC:DD:EE:FF --broadcast 192.168.1.255 --interface eth0 --port 9 --repeat 3
```

CLI 从 YAML 读取默认值与设备的原子快照，不持有写锁，不修改历史；结果输出到终端。Web 操作写入 `events.yaml`。重复次数范围 1–10，发送间隔 100ms。Web 的唤醒与检测共用每分钟 60 次的操作额度，登录和初始化共用每分钟 10 次额度；均为单进程全局限制。

**“唤醒包已发送”不等于“开机成功”**：WoL 无标准回执。检测需配置目标 IP 和真实开放的 TCP 端口，例如 SSH 22 或 RDP 3389。检测成功表示端口可达；未响应不代表已关机，也可能是防火墙或服务尚未启动。

## YAML 数据

`config.yaml` 示例（`password_hash` 由初始化生成，不要填写明文密码）：

```yaml
listen: 127.0.0.1:8080
public_url: "" # 使用 HTTPS 反向代理时填写外部访问地址
password_hash: ""
broadcast: 255.255.255.255
port: 9
repeat: 3
log_limit: 100 # 路由器可设为 0，避免记录操作时写闪存
```

`devices.yaml` 示例：

```yaml
devices:
  - id: desktop
    name: 书房电脑 # Web 保存时保留此注释
    mac: AA:BB:CC:DD:EE:FF
    broadcast: 192.168.1.255
    port: 9
    repeat: 3
    interface: "" # 空字符串表示由系统选择
    ip: 192.168.1.100
    probe_port: 3389
    note: Windows 主机
```

- 数据保存在内存，修改后先写同目录临时文件、同步，再重命名替换；失败时不更新内存。Windows 的替换不具有与所有 Unix 文件系统相同的断电原子性保证。
- 默认目录权限 0700、新文件权限 0600；Windows 上应同时使用账户与目录 ACL 控制访问。
- 手工编辑、备份或恢复前先停止服务，编辑后重启加载；不支持热重载。导出仅含设备数据，不含密码哈希；恢复时用导出的文件替换 `devices.yaml`。
- ID 必须唯一，1–64 位字母、数字、下划线或连字符；Web 最多添加 1000 台设备。每个 YAML 文件读取上限 2 MiB，不支持多文档、锚点或别名。
- 注释按字段、设备 ID 合并保留；删除设备时相关注释也删除。不会保留精确缩进和所有文本布局。
- 同一数据目录只允许一个写进程。异常终止后，如遗留 `data/.lock`，确认没有进程使用该目录，再手动删除空锁目录。不得在服务运行时删除锁。
- 忘记密码：停止服务后，用 `init --password-stdin` 重新设置。

## 后台运行

### Linux systemd

将程序复制到 `/usr/local/bin/shell-wol`；创建系统用户/组 `shell-wol`，将初始化后的数据放到 `/var/lib/shell-wol` 并归属该用户（目录 0700、文件 0600）。将 `deploy/shell-wol.service` 安装到 `/etc/systemd/system/`，然后：

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now shell-wol
sudo journalctl -u shell-wol -f
```

在配置中预先设置所需监听地址；未设置密码时仅回环监听。异常退出留下锁目录时，先停止服务并确认无进程使用，再手动清理空锁目录并重启服务。

### OpenWrt 及兼容二次开发固件

适用于满足上述平台条件的固件；架构选择、安装、升级和兼容性排查见 [OpenWrt 部署指南](docs/openwrt.md)。

1. 用 `uname -m` 等确认 CPU 架构，选择对应二进制；MIPS 还需确认大小端和浮点 ABI。
2. 程序放 `/usr/bin/shell-wol`，授予执行权限；数据放 `/etc/shell-wol`，先设置密码和 LAN 监听地址。
3. 将 `deploy/shell-wol.openwrt` 复制为 `/etc/init.d/shell-wol` 并 `chmod +x`。
4. `/etc/init.d/shell-wol enable`，再 `/etc/init.d/shell-wol start`；`logread` 查看日志。

进程被强制终止留下锁时，先 stop 服务，确认进程停止，再删除 `/etc/shell-wol/.lock` 后 start。路由器建议 `log_limit: 0`，正常启停会清理锁；不自动创建防火墙规则。二次开发固件如未保留 rc.common/procd，应使用其实际服务管理方式，不应直接安装此启动脚本。macOS 与 Windows 首版可直接终端运行，暂不提供原生服务安装器。

## 构建与验证

```sh
go test -race ./...
go vet ./...
./scripts/build.sh
```

构建默认 `CGO_ENABLED=0`，输出到 `dist/` 并生成 SHA256SUMS：

| 系统 | 架构 |
| --- | --- |
| macOS | amd64、arm64 |
| Windows | amd64、arm64 |
| Linux | amd64、arm64、armv7 |
| OpenWrt 及兼容二次开发固件 | amd64、arm64、armv7、mips/mipsle（softfloat），按实际 CPU 选择 |

OpenWrt 使用 Linux 内核，因此二进制仍命名为 `shell-wol-linux-*`、构建仍使用 `GOOS=linux`，无需另设 `GOOS=openwrt` 或按路由器品牌构建。构建脚本在开发电脑的 Bash 中运行，路由器只需运行匹配的二进制。ARMv5、ARMv6 和其他未列出的架构没有现成构建产物，需另行评估。

交叉编译成功不代表已在所有设备实测。工具链版本影响最低系统/内核要求；部署前请核对所用 Go 版本的系统要求和设备架构。测试覆盖魔术包内容与实际本机 UDP 收包、TCP 检测、YAML 注释/清空字段/锁与失败恢复、会话、认证、CSRF、限流和 API 设备管理。真实广播、关机唤醒及各系统服务部署仍需在对应硬件网络上验证。

暂不实现定时唤醒、自动发现、跨网段代理、自动更新、多用户或远程关机。
