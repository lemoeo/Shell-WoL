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

## 2. 上传并检查二进制

例如从电脑上传 MIPS 小端版本（替换路由器 IP 和本地文件路径）：

```sh
scp -O shell-wol-linux-mipsle-softfloat root@192.168.1.1:/tmp/shell-wol
```

`-O` 用于没有 SFTP 服务的固件；若本机 SCP 客户端不支持此选项，使用固件支持的其他文件上传方式。在路由器上检查：

```sh
chmod +x /tmp/shell-wol
/tmp/shell-wol version
```

正常显示版本后，再安装到持久化位置：

```sh
cp /tmp/shell-wol /usr/bin/shell-wol
mkdir -p /etc/shell-wol
chmod 700 /etc/shell-wol
```

如果目录不可写或空间不足，应先选择固件支持的可执行存储位置，再同步修改后续命令及启动脚本中的路径。不要将需要保留的 YAML 数据放在重启即清空的 `/tmp`。

## 3. 初始化并手动测试

BusyBox ash 示例，密码通过标准输入传入，不写入命令历史：

```sh
read -r -s -p '管理员密码: ' wol_password
printf '\n'
printf '%s' "$wol_password" | /usr/bin/shell-wol init --data-dir /etc/shell-wol --password-stdin
unset wol_password
```

密码须为 12–72 字节。如果固件裁剪的 Shell 不支持 `read -s`，应使用其支持的隐藏输入方式，勿将真实密码直接写入命令参数。确认初始化成功后，再继续修改配置。

在服务停止时编辑 `/etc/shell-wol/config.yaml`，设置 LAN 访问与关闭操作记录：

```sh
sed -i 's/^listen:.*/listen: 0.0.0.0:8080/' /etc/shell-wol/config.yaml
sed -i 's/^log_limit:.*/log_limit: 0/' /etc/shell-wol/config.yaml
/usr/bin/shell-wol serve --data-dir /etc/shell-wol
```

从同一 LAN 的电脑或手机访问 `http://路由器LAN地址:8080` 并登录。若端口已占用，更换配置中的端口。监听所有 IPv4 网卡时，应确保防火墙仅允许可信 LAN 访问，不能依赖二次开发固件具有与 OpenWrt 默认值相同的规则；无需公网端口映射。

添加设备时，选择目标电脑所在 LAN 的实际 IPv4 网卡，例如 `br-lan`。广播地址按该接口的 IP 和子网掩码计算，不能固定假设为 `192.168.1.255`。目标电脑需启用 WoL 并保持供电。发送成功不代表已开机，可另行配置 TCP 上线检测。

## 4. 配置 procd 后台服务

先按 `Ctrl+C` 停止手动测试进程，避免两个写进程使用同一数据目录。确认以下命令返回成功；否则应按固件的实际服务机制适配，不直接套用脚本：

```sh
test -f /etc/rc.common && test -f /lib/functions/procd.sh
```

将仓库中的 [`deploy/shell-wol.openwrt`](../deploy/shell-wol.openwrt) 上传为 `/etc/init.d/shell-wol`，然后运行：

```sh
chmod +x /etc/init.d/shell-wol
/etc/init.d/shell-wol enable
/etc/init.d/shell-wol start
logread -e shell-wol
```

管理命令为 `/etc/init.d/shell-wol stop` 或 `/etc/init.d/shell-wol restart`。

正常退出会清理数据锁。异常终止留下 `/etc/shell-wol/.lock` 时，先停止服务，并通过 `pidof shell-wol` 等确认没有进程使用该数据目录，再用 `rmdir /etc/shell-wol/.lock` 删除空锁目录后启动。不要在进程运行期间删除锁目录。

## 5. 升级与恢复

1. 将新版本先上传到 `/tmp`，核对架构、版本及随构建产物提供的 SHA256SUMS。
2. 停止服务，确认进程退出，将现有程序和 `/etc/shell-wol` 中的 YAML 文件备份到受保护的持久化位置。`config.yaml` 含密码哈希，不要公开备份。
3. 替换程序并确认有执行权限，保留现有数据目录；不需要重新初始化密码。
4. 启动服务，检查日志、Web 登录、设备列表及发送功能。

升级失败时，先停止服务，再恢复旧程序；若新版本修改了数据格式，应同时恢复对应版本的数据备份。不要覆盖正在运行的程序，也不要在服务运行时直接恢复 YAML。

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
