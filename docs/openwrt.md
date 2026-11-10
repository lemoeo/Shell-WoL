# OpenWrt 及兼容二次开发固件部署

本指南面向 OpenWrt，以及保留所需运行能力的二次开发固件。部分小米、网件等设备可能运行此类固件，但同一品牌的不同型号、原厂版本或第三方版本不能视为相同平台。项目未在这些品牌的所有设备上测试，也不提供刷机或解锁 SSH 的流程。

## 部署条件

- 可以通过 SSH 或其他管理终端执行命令、上传文件。
- 允许运行自定义 ELF 程序，且 CPU 架构、内核版本满足所用 Go 工具链的运行要求。
- 可执行分区和持久化数据目录可写，存储空间、内存足够。
- 使用本项目的开机启动示例时，固件必须保留 `/etc/rc.common` 和 procd。手动运行二进制本身不依赖 procd。
- 路由器能访问目标电脑所在 LAN；访客网络、VLAN 或客户端隔离可能阻断广播。

`CGO_ENABLED=0` 不依赖目标设备的 glibc/musl 动态链接库，但不代表能兼容任意 CPU、内核或原厂固件。当前提供通用二进制和 procd 示例，尚未提供 LuCI 插件、opkg/APK 安装包或刷机镜像。

## 1. 确认架构与固件

在路由器终端中执行：

```sh
uname -m
uname -r
ubus call system board
df -h
free
```

根据固件提供的包管理器，补充执行 `opkg print-architecture` 或 `apk --print-arch`。部分二次开发固件可能没有这些命令，需要结合固件文档确认。

| 架构信息 | 构建文件 |
| --- | --- |
| x86_64 / amd64 | `shell-wol-linux-amd64` |
| aarch64 / arm64 | `shell-wol-linux-arm64` |
| ARMv7 | `shell-wol-linux-arm-7` |
| MIPS 小端，例如 mipsel_24kc | `shell-wol-linux-mipsle-softfloat` |
| MIPS 大端，例如 mips_24kc | `shell-wol-linux-mips-softfloat` |

`uname -m` 仅显示 `mips` 时，不足以确定大小端。ARM 也要确认具体版本，不要将所有 32 位 ARM 设备都当作 ARMv7。未列出的架构需要另行评估和构建。构建脚本在开发电脑运行，不要求在路由器上安装 Go 或 Bash。

## 2. 安装器部署（推荐）

详见 [安装与升级指南](install.md)。从仓库下载 `scripts/install.sh`，在路由器上以 root 执行。支持 GitHub 和 jsDelivr 源；二进制和模板始终固定到同一版本：

```sh
sh install.sh --service
# 使用 ShellCrash 中的 CDN 域名
sh install.sh --source jsdelivr --cdn-base https://testingcf.jsdelivr.net --service
```

未传 `--source` 时使用 GitHub，下载失败直接停止；`--source jsdelivr` 使用所选 CDN，不自动换源。`--cdn-base` 或 `SHELL_WOL_CDN_BASE` 可修改 CDN 源站，地址不带 `/gh`。也可直接从该 CDN 的 `/gh/lemoeo/Shell-WoL@main/scripts/install.sh` 获取入口。CDN 二进制必须由发布或同步工作流先补齐资产标签，见 [安装源和 CDN](install.md#安装源和-cdn)。

可以显式指定 `--version v0.1.0`、`--arch mipsle`、`--install-dir /持久化目录/shell-wol`。没有 curl 或支持证书校验的完整 wget 时，先用固件支持的方式安装下载工具及 CA 证书；不要关闭校验。

SHA256 校验不要求独立安装 GNU 工具：安装、卸载和持久自启依次探测 `sha256sum`、`busybox sha256sum`、`shasum -a 256`、`openssl dgst -sha256`，通过实际计算验证可用性。如果 BusyBox 包含 sha256sum applet，即使没有同名独立命令也可以使用；全部不可用时会报错并停止。详细说明见 [校验工具兼容](install.md#发布前提与安装入口)。

默认根目录：标准 OpenWrt 为 `/etc/shell-wol`；检测到小米特征 `/data/etc/crontabs/root` 且 `/data` 可写时，为 `/data/shell-wol`。根目录下 `bin/shell-wol` 是程序，`data/` 是 YAML，`backup/` 是升级备份。请确认所选分区持久可写；程序和数据不能放在重启即清空的 `/tmp`。

默认关闭路由器操作记录（`log_limit: 0`）。首次安装不设置密码；`--service` 会直接注册并启动服务，可在浏览器完成初始化。默认监听本机，局域网访问需显式修改监听地址。

### 镜像固件的持久自启动

参考 ShellCrash，以 `/data/etc/crontabs/root` 存在且 `/data` 可写识别小米镜像固件；不会因为版本字符串包含 `SNAPSHOT` 就采用特殊启动方式。普通 OpenWrt Snapshot 若具有持久 overlay，继续使用原生 procd 即可。

小米镜像固件首次注册服务时，默认自动选择 firewall 模式：

```sh
sh install.sh --service --yes
# 显式选择；安装器会检查 procd、uci 和 ubus
sh install.sh --service --boot-mode firewall --install-dir /data/shell-wol --yes
# 确认 /etc 可以持久保存的固件可使用原生方式
sh install.sh --service --boot-mode native --yes
```

安装器在 `/data/shell-wol` 保留 `boot.sh` 和 `service/shell-wol.procd`，注册如下入口（路径跟随安装目录）：

```text
firewall.ShellWoL=include
firewall.ShellWoL.type=script
firewall.ShellWoL.path=/data/shell-wol/boot.sh
firewall.ShellWoL.enabled=1
```

检测到 fw4 时额外设置 `fw4_compatible=1`。不修改其他 include、SSH、NVRAM 或端口规则，也不为注册入口重启防火墙。存在其他未提交的 firewall 配置时先中止，避免顺带提交。

启动链路为：开机防火墙执行 include → `boot.sh` 启动后台恢复任务并立即返回 → 等待 procd 和配置就绪 → 校验持久副本、按需恢复 `/etc/init.d/shell-wol` → 通过 procd 启动。等待与 Web 就绪检查共享约 60 秒预算（正在执行的检查可能额外占用几秒），不会等待互联网；初始化后的服务仍由 procd 守护。已有运行服务直接跳过，重复触发通过锁去重，不会停止或重启现有服务；遇到数据锁、服务归属冲突或校验失败时停止处理，使用 `logread -e shell-wol-boot` 查看原因。此模式不依赖 `/etc/rc.d` 自启链接；防火墙操作是否重跑 include 取决于固件及 fw3/fw4 行为。

**前提：firewall UCI 配置确实能被该固件持久保存，而且开机时执行 script include。** `uci commit` 本身无法保证任意重置文件系统都持久化。小米 firewall 模式限定安装到开机即可访问的 `/data` 子目录，不支持把入口放到尚未挂载的 USB 分区；其他兼容固件显式选择该模式时也需自行确认路径在防火墙启动前可访问。为兼容 fw3 对脚本路径的处理，此模式安装目录不能含空格。

自启动开关使用持久脚本管理：

```sh
sh /data/shell-wol/boot.sh status
sh /data/shell-wol/boot.sh disable  # 关闭下次自启动，不停止当前服务
/etc/init.d/shell-wol stop

sh /data/shell-wol/boot.sh enable   # 恢复自启动，不立即启动
sh /data/shell-wol/boot.sh          # 后台恢复并按需启动
sh /data/shell-wol/boot.sh run      # 前台恢复，便于查看错误
```

`disable` 同时记录持久禁用标记并提交 include 的启用状态；升级保留该状态。不要仅依赖 `/etc/init.d/shell-wol disable`，它只操作原生启动链接。如果需要保持服务停止，先禁用持久自启，再停止服务。

安装器不会自动重启路由器。自行安排重启验证后，检查：

```sh
uci show firewall.ShellWoL
/etc/init.d/shell-wol running
logread -e shell-wol-boot
logread -e shell-wol
```

只有目标型号和固件完成重启验证后，才能确认该机制在该设备上可用。

## 3. 初始化和 LAN 访问

以标准 OpenWrt 的默认路径为例，小米固件请替换为 `/data/shell-wol`。先停止服务并编辑监听地址，再重启：

```sh
/etc/init.d/shell-wol stop
sed -i 's/^listen:.*/listen: 0.0.0.0:8080/' /etc/shell-wol/data/config.yaml
/etc/init.d/shell-wol start
swol token  # 新版无需 logread
```

新程序使用 `swol token` 或 `shell-wol token --data-dir PATH` 读取当前服务令牌；菜单第 5 项也可查看，不依赖 logread。旧程序从最近启动日志取令牌。在浏览器设置管理员密码后再登录。密码须为 12–72 字节；令牌不通过网页返回，重启后旧令牌失效。也可选择停止服务后通过 `init --password-stdin` 设置密码，但不是启动的前置条件。

从同一 LAN 的电脑或手机访问 `http://路由器LAN地址:8080`。若端口已占用，更换配置中的端口。确保防火墙仅允许可信 LAN 访问，不能假设二次开发固件具有 OpenWrt 的默认规则；无需公网端口映射。

发送网卡选择目标电脑所在 LAN 的实际 IPv4 网卡，例如 `br-lan`。广播地址按 IP 和子网掩码计算，不能固定假设为 `192.168.1.255`。目标电脑需启用 WoL 并保持供电，发送成功不等于已开机。

## 4. 手动安装与后台服务

不能在线下载时，可从电脑上传匹配的二进制到持久化根目录的 `bin/shell-wol`，授予执行权限，在 `data/` 初始化，再用 `serve --data-dir` 手动运行。不要直接复制 `deploy/shell-wol.openwrt` 启动：它是包含 `@ROOT@` 的模板，需要填入实际根目录。

自动服务要求 `/etc/rc.common`、procd 库且 PID 1 为 procd。没有该能力的厂商固件应按实际服务机制适配；firewall include 仅用于恢复已有 procd 能力，不能替代 procd。

服务管理使用 `/etc/init.d/shell-wol stop` 或 `/etc/init.d/shell-wol restart`。新程序的 `data/.lock` 是普通文件，实际锁在进程退出或设备重启后自动释放，不要因文件仍在就删除它。旧程序的空 `.lock/` 目录需停服、确认无进程后迁移一次，见 [运行状态文件](runtime-state.md)。

## 5. 升级与旧目录迁移

安装器管理的目录可再次运行脚本升级，保留 YAML、密码及服务类型。下载校验或服务就绪检查失败时会停止或尝试回退；详见 [升级与恢复](install.md)。

以前手动部署的 `/usr/bin/shell-wol` 和 `/etc/shell-wol/*.yaml` 没有安装元数据，不会被自动覆盖。先停止旧服务、备份程序与数据，使用新的空根目录安装；在服务停止时把 YAML 迁移到新根目录的 `data/`。如果沿用旧服务名称，先备份旧服务文件并人工迁移，安装器拒绝覆盖不属于它的服务。原数据确认可用前不要删除旧目录。

## 卸载

从仓库下载 `scripts/uninstall.sh`，先预览，再以 root 执行。以标准 OpenWrt 为例：

```sh
sh uninstall.sh --install-dir /etc/shell-wol --dry-run
sh uninstall.sh --install-dir /etc/shell-wol
```

小米兼容固件的默认根目录是 `/data/shell-wol`，外置存储使用实际安装路径。默认停止并禁用 procd 服务、移除程序，保留数据。只有显式指定 `--purge` 才删除已知 YAML 和文件日志；未知文件、系统日志及账户保留。卸载不比较服务文件摘要；服务路径不属于本安装、运行锁遗留或仍有手动运行进程时中止，不自动杀进程或删锁。完整范围见 [卸载与重新安装](install.md#卸载与重新安装)。

firewall 模式使用最新版卸载器：先移除本安装的 include，阻止恢复任务重新拉起服务，再停止 procd 并删除持久启动脚本和副本。即使 `/etc/init.d/shell-wol` 已重置，也会用验证通过的持久副本完成服务清理。`--dry-run` 只展示这些操作。

## 兼容性验证与排查

当前已完成 Linux 开发环境中的功能测试及所列目标的交叉编译。OpenWrt/二次开发固件的启动、真实 LAN 广播、关机唤醒和服务重启仍需在对应型号与固件上验证，不能仅凭构建成功声明实机兼容。

| 现象 | 检查方向 |
| --- | --- |
| `Exec format error` | CPU 架构、大小端、文件完整性 |
| `Illegal instruction` | CPU 指令集与构建参数是否匹配 |
| 无法执行程序 | 文件权限、分区挂载限制、厂商固件限制 |
| 启动崩溃、系统调用报错或内存不足 | 内核、Go 工具链系统要求、设备资源 |
| CLI 可运行，但开机服务失败 | rc.common/procd、脚本路径、数据锁、启动日志 |
| Web 无法访问或目标未被唤醒 | 监听地址、端口、防火墙、LAN 接口、广播地址、目标 WoL 设置 |

排查时记录型号、固件版本、架构、内核及实际错误信息，不应按品牌直接更换构建目标。
