# 安装、升级和后台服务

安装器使用 POSIX `sh`，目标设备不需要 Go、Bash、Python 或 Node.js。支持 macOS、标准 Linux 和满足运行条件的 OpenWrt/二次开发固件。Windows 继续下载 `.exe` 后直接运行。

## 发布前提与安装入口

维护者推送 `v主版本.次版本.补丁` 标签后，GitHub Actions 执行测试、交叉编译并发布 Release。预发布标签可使用 `v0.1.0-beta.1`，会标记为 prerelease。Release 只上传各平台二进制、VERSION 和二进制 SHA256SUMS。安装脚本和三份服务模板保留在仓库，不作为 Release 附件；仅推送源码不会自动发布二进制。没有 Release 时，可先使用本地构建。

从仓库 main 分支下载最新安装入口，查看后运行：

```sh
curl --fail --location --proto '=https' --proto-redir '=https' \
  https://raw.githubusercontent.com/lemoeo/Shell-WoL/main/scripts/install.sh \
  --output install.sh
sh install.sh
```

需要完全固定安装流程时，从 `https://raw.githubusercontent.com/lemoeo/Shell-WoL/<版本标签>/scripts/install.sh` 下载配套入口，并传 `--version <版本标签>`。入口脚本从 main 下载，并不意味着服务模板也从 main 下载。

文件来源如下：

| 内容 | 默认来源 |
| --- | --- |
| 安装入口 | 仓库 main 的 `scripts/install.sh`，也可固定标签 |
| 最新正式版本信息 | Release 的 `latest/download/VERSION` |
| 二进制及其 SHA256SUMS | Release 的 `download/<版本>/` |
| 服务模板及其 SHA256SUMS | 仓库的 `<同版本标签>/deploy/` |

首次解析 latest 后，二进制与模板全部固定到同一版本；不允许把新 main 的模板静默用于旧二进制。下载模板或其摘要失败就停止，不回退到 main 或 Release 中的旧模板。

旧 Release 已上传的脚本和模板无需删除。**旧标签如果没有 `deploy/SHA256SUMS`，新安装器无法为该版本自动安装服务**；可使用原配套安装方式，或不加 `--service` 只安装程序并手动运行。已有受管理服务的升级会保留服务类型，因此也需要选择包含模板清单的新标签。不要移动旧标签来补文件；应提交文件并发布新版本。

支持有证书校验及 `--https-only` 的 wget；精简 BusyBox wget 不一定具备这些能力，遇到提示应先通过固件支持的方式安装 curl 和 CA 证书，不能跳过 TLS 校验。

交互模式显示系统、固件、架构及推荐根目录，允许修改目录、选择后台服务，最后确认安装。通过 `/dev/tty` 读取选择；自动化环境使用 `--yes`。脚本不自行调用 sudo 提权；macOS 普通用户默认用户安装，Linux 系统安装需管理员执行。

## 路径与选项

| 平台/模式 | 默认安装根目录 |
| --- | --- |
| Linux 系统安装 | `/opt/shell-wol` |
| 小米固件，检测到 `/data/etc/crontabs/root` 且 `/data` 可写 | `/data/shell-wol` |
| 标准 OpenWrt/兼容固件 | `/etc/shell-wol` |
| macOS 用户安装（普通用户默认） | `~/Library/Application Support/Shell-WoL` |
| macOS 系统安装（root 执行） | `/Library/Application Support/Shell-WoL` |
| Linux `--user` | `${XDG_DATA_HOME:-~/.local/share}/shell-wol` |

固件特征只用于推荐位置，并不保证该型号兼容。小米特征优先于通用 OpenWrt；刷入标准 OpenWrt 且没有该特征的设备使用 OpenWrt 路径。安装前仍需确认所选分区可执行、持久可写且空间足够。Linux 内核环境下即使固件来源未知，也可只安装程序并手动运行；无法识别服务机制时 `--service` 会失败。

```text
安装根目录/
├── bin/shell-wol
├── data/                 YAML 数据及凭据密钥；初始化后默认私有权限
├── backup/shell-wol.previous
└── .shell-wol-install    安装器版本、服务及服务文件摘要
```

```sh
# Linux 系统安装（需管理员权限）
sudo sh install.sh --version v0.1.0 --service --yes

# Linux/macOS 用户安装
sh install.sh --user --yes

# 自定义根目录，程序和数据仍在同一根目录内
sh install.sh --user --install-dir "$HOME/Apps/shell-wol" --yes

# 路由器上确认大小端后显式选择架构（以 root 执行）
sh install.sh --arch mipsle --service --yes
```

支持参数：`--version`、`--arch`、`--install-dir`、`--user`、`--service`、`--yes`、`--download-base`、`--repo-base` 和 `--help`。

- `--download-base` 是 HTTPS Release 根地址，镜像保持 `latest/download/VERSION` 和 `download/<版本>/<资产名>` 结构。
- `--repo-base` 是 HTTPS 仓库文件根地址，不包含分支或标签；镜像保持 `<版本标签>/deploy/SHA256SUMS` 和 `<版本标签>/deploy/<模板名>` 结构。
- 两类地址独立配置，不会用 Release 镜像地址推断仓库镜像。模板仍固定到二进制版本标签；同一版本的内容不应随意替换。

例如：

```sh
sh install.sh --version v0.1.0 \
  --download-base https://release.example/releases \
  --repo-base https://repo.example/raw/lemoeo/Shell-WoL
```

以上镜像域名仅为结构示例，使用时替换为实际服务。

路径支持 ASCII 英文、数字、空格和 `_ . / + ~ -`，须为绝对路径；不支持非 ASCII 路径、符号链接路径、控制字符、`.`/`..` 路径段或临时根目录。目录不应有其他用户的写权限。`--yes` 不会跳过校验、不确定架构、目录冲突或未知服务机制；Linux 用户模式暂不自动注册后台服务。

架构支持 amd64、arm64、armv7、mips、mipsle。自动检测使用 uname，MIPS 通过本机 ELF 字节序区分大小端；无法识别时要求显式指定。ARMv7 仍需 CPU/浮点能力和内核兼容，最终以下载后的运行检查为准。不自动选择 ARMv5/ARMv6/386 产物。

## 首次密码与服务启动

首次安装执行不含密码的 `init`，生成默认 YAML；路由器默认 `log_limit: 0`，减少操作记录写闪存。安装器不接收或保存密码、不改变 LAN 监听和防火墙。默认只监听 `127.0.0.1:8080`。

服务模式检测如下：

| 环境 | 后台方式与账户 |
| --- | --- |
| Linux PID 1 为 systemd | systemd，创建/使用非 root 的 shell-wol 用户 |
| OpenWrt PID 1 为 procd，且有 rc.common | procd，按路由器默认账户运行 |
| macOS 用户安装 | LaunchAgent，当前用户；需要可用的 GUI 登录会话 |
| macOS 系统安装 | LaunchDaemon，daemon 用户 |

系统安装的程序及元数据由安装账户管理，后台服务仅需写 data；用户安装的程序与服务属于同一账户。服务文件放在操作系统要求的 systemd/init.d/LaunchAgents/LaunchDaemons 目录。模板由安装器填充，不能直接复制含 `@ROOT@` 的模板启动。

`--service` 会注册、启用并启动服务，尚未设置管理员密码也能启动。安装器使用 `check --running` 请求实际 Web 接口验证就绪；“待初始化”和“已初始化”均属于可用的服务状态。

首次通过浏览器设置密码：

1. 本机访问默认地址；局域网访问需先在服务停止时将 `data/config.yaml` 中 `listen` 改为 LAN 地址或 `0.0.0.0:8080`，然后启动服务。默认不会自动开放 LAN 监听。
2. 从当前启动的终端或服务日志取得随机初始化令牌。
3. 打开管理页面，输入令牌，设置 12–72 字节的密码。
4. 使用新密码登录；不需要重启服务。

令牌不会通过 Web 返回，重启前的旧令牌失效；初始化成功后不能再次通过初始化接口更改密码。设备管理、配置、记录和唤醒仍要求登录，初始化接口保留同源检查和请求限流。

查看当前启动的令牌：

```sh
# Linux systemd
sudo journalctl -u shell-wol -n 30 --no-pager

# OpenWrt
logread -e shell-wol

# macOS 用户安装
cat "$HOME/Library/Application Support/Shell-WoL/data/service.log"

# macOS 系统安装
sudo cat "/Library/Application Support/Shell-WoL/data/service.log"
```

日志可能保留上次启动的旧令牌，应使用最近一次启动输出的令牌，不要公开日志中的令牌。也可先停止服务，再通过 `init --password-stdin` 设置密码；Linux systemd 使用 `sudo -u shell-wol`，macOS 系统安装使用 `sudo -u daemon`，用户安装使用当前用户，路由器通常使用 root。不要用错误账户生成仅该账户可读的 0600 配置文件。

路由器需检查 LAN/WAN 防火墙。桌面模式可直接运行 `serve`，局域网模式可加 `--listen 0.0.0.0:8080`。不要同时运行手动进程和使用同一数据目录的后台服务。

## 升级、冲突与回退

再次运行安装器可升级；已有安装的服务类型会自动保留，不需要再次传 `--service`。

- 下载、SHA256 和版本运行检查在替换程序前完成；服务模板根据同版本仓库中的 `deploy/SHA256SUMS` 验证摘要，和 Release 的二进制校验清单分开处理。
- 升级前只读校验已有 YAML，停止本安装管理的服务，再检查运行锁。安装器不会删除 `data/.lock`。
- 程序替换后保留旧程序为 `backup/shell-wol.previous`，已有 YAML 不改写、不重置密码。
- 启动服务后会实际请求配置地址的 `/api/session`，未初始化时也应正常响应；仅有进程或服务状态不足以通过就绪检查。
- 启动失败会尝试恢复旧程序、服务文件、安装元数据和原有启用/运行状态，并明确报告恢复失败。数据不做跨版本迁移；将来若有格式变化，需另行制定迁移与数据备份流程。
- 安装器拒绝覆盖不属于本安装的服务文件，以及摘要发生变化的用户定制服务文件。应备份后人工合并。
- 目标非空且没有安装元数据时拒绝覆盖；老版手动安装需先停止服务、备份数据，选择新目录安装，再在服务停止时迁移 YAML。路径更换需人工迁移服务文件。
- 异常退出遗留数据锁 `data/.lock` 或安装锁 `.install-lock` 时，需分别确认没有服务或安装进程后人工处理。不会修改厂商 SSH、防火墙或 NVRAM。

程序版本正确不等于该固件上的广播、开机启动都已验证；macOS launchd、真实 systemd/procd 与路由器仍需对应系统实测。当前安装器测试在 Linux 运行，下载、服务管理的故障场景使用隔离夹具验证。

## 卸载与重新安装

卸载器为独立 POSIX Shell 脚本，从仓库下载，不作为 Release 附件；下载后卸载过程不联网，也不依赖同目录下存在 install.sh。

```sh
curl --fail --location --proto '=https' --proto-redir '=https' \
  https://raw.githubusercontent.com/lemoeo/Shell-WoL/main/scripts/uninstall.sh \
  --output uninstall.sh
```

默认推荐路径与安装器一致。可以先预览，再执行：

```sh
# Linux 系统安装
sh uninstall.sh --install-dir /opt/shell-wol --dry-run
sudo sh uninstall.sh --install-dir /opt/shell-wol

# macOS 或 Linux 用户安装
sh uninstall.sh --user --dry-run
sh uninstall.sh --user

# 小米兼容固件，以 root 执行
sh uninstall.sh --install-dir /data/shell-wol

# 标准 OpenWrt，以 root 执行
sh uninstall.sh --install-dir /etc/shell-wol

# 显式清除已知配置、设备、记录及文件日志
sudo sh uninstall.sh --install-dir /opt/shell-wol --purge
```

选项为 `--install-dir`、`--user`、`--purge`、`--dry-run`、`--yes`、`--help`。`--yes` 仅跳过最终确认，不忽略归属、路径、服务状态和进程检查。`--dry-run` 不创建锁、不停止服务、不修改文件。macOS 用户服务应由原登录用户执行，不要通过 sudo 切换到另一个用户的 LaunchAgent 域。

卸载行为：

| 内容 | 默认 | `--purge` |
| --- | --- | --- |
| 受管理的服务文件及开机启动 | 移除 | 移除 |
| `bin/shell-wol`、`backup/shell-wol.previous` | 删除 | 删除 |
| `data/config.yaml`、`devices.yaml`、`events.yaml` | 保留 | 删除 |
| `data/service.log`、`service.err.log` | 保留 | 删除 |
| 最小安装元数据 | 标记为已卸载，保留 | 移除 |
| 未知文件、空的未知子目录 | 保留 | 保留 |
| 系统账户、journal/syslog 中的历史日志 | 保留 | 保留 |

脚本先验证安装元数据及服务命令的实际安装路径，再停止和禁用对应 systemd/procd/launchd 服务，确认服务停止后检查数据锁和本安装的可见运行进程。不会按进程名批量杀进程，也不直接递归删除根目录。仅删除明确列出的文件，最后尝试移除空目录。

卸载器不再比较服务文件与安装记录中的摘要。修正 WorkingDirectory 引号、添加注释或调整服务参数后，只要服务命令仍指向本安装的程序和数据目录，就可卸载；不需要修改或重算元数据中的 `service_sha256`。如果服务指向另一个安装目录，仍会拒绝处理。此调整仅适用于卸载，安装器升级时对定制服务文件的覆盖保护保持不变。

卸载使用和安装器相同的 `.install-lock` 防止并发，并在确认没有已有运行锁后临时创建自己的数据锁，防止常规服务同时重启。只清理自己创建的锁。检测到手动运行进程、遗留运行锁、无法判断服务状态、服务指向其他安装目录或路径为符号链接时，会中止；请先人工确认，不要盲目删除锁。Linux 检查可读取的 `/proc` 可执行路径，macOS 使用 ps；执行期间不要在另一个终端绕过数据锁启动程序。

服务停止、禁用或重新加载失败时，不继续删除程序和数据；已经完成的服务操作可能保留。处理错误后可再次执行：原服务已停止、已卸载或文件已不存在且状态仍可确认时可继续；procd 脚本缺失导致无法查询状态时需先人工处理。脚本不删除系统服务账户，以保持保留数据的归属，并支持重新安装。

默认卸载后直接用安装器指定原根目录重新安装，YAML 会保留；需要恢复后台服务时选择 `--service`。清除后若目录已不存在，再次卸载会正常提示无需处理。如果 `--purge` 后仍有未知文件，则目录保留且元数据已移除；再次操作会拒绝把它当作受管理安装，重新安装应选择空目录或先人工迁移未知文件。没有安装元数据的手动安装也需人工卸载。

## 维护者发布与验证

```sh
go test -race ./...
go vet ./...
python3 -m unittest discover -s tests/installer -p 'test_*.py' -v
VERSION=v0.1.0 ./scripts/build.sh
# Linux 开发机可选：真实二进制安装与 HTTP 就绪集成检查
go build -ldflags='-X main.version=v0.0.0-test' -o bin/shell-wol-install-test ./cmd/shell-wol
python3 tests/installer/smoke_native.py bin/shell-wol-install-test
```

Release 工作流仅在推送版本标签时运行；准备好版本后再推送标签。上述本地命令只生成文件，不发布。`dist/` 只包含二进制、VERSION、SHA256SUMS；Release 的 SHA256SUMS 仅列出二进制。构建脚本会清理旧版生成的 install.sh 与三份模板副本，不删除其他用户文件。校验工具兼容 sha256sum 与 macOS shasum。


服务模板修改后必须运行：

```sh
sh scripts/template-checksums.sh
sh scripts/template-checksums.sh --check
```

将模板与 `deploy/SHA256SUMS` 一起提交，再创建版本标签。Tests、Release 工作流和构建脚本都会检查该清单；摘要过期会导致检查失败。不要在 Release 工作流中自动覆盖仓库清单来绕过检查。

## Mac VNC 凭据

启用 Mac 唤醒后，数据目录还包含加密的 `credentials.yaml` 与本机密钥 `credentials.key`。升级和默认卸载保留两者；`--purge` 将两者列入预览并删除。备份必须保护完整数据目录及对应文件权限，缺少密钥无法恢复密码。配置与只写密码行为见 [Mac VNC 唤醒指南](mac-vnc-wake.md)。
