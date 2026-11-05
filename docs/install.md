# 安装、升级和后台服务

安装器使用 POSIX `sh`，目标设备不需要 Go、Bash、Python 或 Node.js。支持 macOS、标准 Linux 和满足运行条件的 OpenWrt/二次开发固件。Windows 继续下载 `.exe` 后直接运行。

## 发布前提与安装入口

维护者推送 `v主版本.次版本.补丁` 标签后，GitHub Actions 执行测试、交叉编译并发布 Release。预发布标签可使用 `v0.1.0-beta.1`，会标记为 prerelease。安装器、二进制、服务模板、VERSION 和 SHA256SUMS 一起发布；仅推送源码不会自动发布安装包。项目尚无 Release 时，线上安装入口暂不可用，可先使用本地构建。

从最新正式 Release 下载安装器，查看后运行：

```sh
curl --fail --location --proto '=https' --proto-redir '=https' \
  https://github.com/lemoeo/Shell-WoL/releases/latest/download/install.sh \
  --output install.sh
sh install.sh
```

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
├── data/                 YAML 数据；初始化后默认私有权限
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

支持参数：`--version`、`--arch`、`--install-dir`、`--user`、`--service`、`--yes`、`--download-base` 和 `--help`。`--download-base` 仅接受 HTTPS Release 根地址，镜像需保持 `latest/download/VERSION` 和 `download/<版本>/<资产名>` 结构；同一固定版本内资产不得随意替换。首次解析 latest 后所有资产均按固定版本下载。

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

`--service` 在没有管理员密码时只注册并禁用服务，不立即启动。按照安装器输出，以**实际数据账户**运行密码初始化。例如 Linux systemd 安装：

```sh
read -r -s -p '管理员密码: ' wol_password
printf '\n'
printf '%s' "$wol_password" | sudo -u shell-wol \
  /opt/shell-wol/bin/shell-wol init --data-dir /opt/shell-wol/data --password-stdin
unset wol_password
sudo systemctl enable --now shell-wol
```

macOS 系统安装用 `sudo -u daemon` 初始化；用户安装直接执行程序；路由器通常为 root。不要用错误账户生成仅该账户可读的 0600 配置文件。没有 sudo 的精简 Linux 应用系统支持的账户切换工具执行。

初始化后可重复运行同样的安装命令，让安装器检查配置并启动服务，或使用原生服务命令：

```sh
# OpenWrt
/etc/init.d/shell-wol enable
/etc/init.d/shell-wol start

# macOS 用户安装（初始化后）
launchctl enable "gui/$(id -u)/io.github.lemoeo.shell-wol"
launchctl bootstrap "gui/$(id -u)" "$HOME/Library/LaunchAgents/io.github.lemoeo.shell-wol.plist"

# macOS 系统安装（初始化后）
sudo launchctl enable system/io.github.lemoeo.shell-wol
sudo launchctl bootstrap system /Library/LaunchDaemons/io.github.lemoeo.shell-wol.plist
```

路由器首次启动服务前，还应在服务停止时编辑 `data/config.yaml`，设置合适的 LAN 监听地址，并检查 LAN/WAN 防火墙。桌面模式可在本机用终端 `serve`，然后通过浏览器完成首次设置；接着先停止手动进程，再重新运行安装器或启动后台服务。

## 升级、冲突与回退

再次运行安装器可升级；已有安装的服务类型会自动保留，不需要再次传 `--service`。

- 下载、SHA256 和版本运行检查在替换程序前完成；服务模板也验证摘要。
- 升级前只读校验已有 YAML，停止本安装管理的服务，再检查运行锁。安装器不会删除 `data/.lock`。
- 程序替换后保留旧程序为 `backup/shell-wol.previous`，已有 YAML 不改写、不重置密码。
- 已设置密码时，启动服务后会实际请求配置地址的 `/api/session`，仅有进程或服务状态不足以通过就绪检查。
- 启动失败会尝试恢复旧程序、服务文件、安装元数据和原有启用/运行状态，并明确报告恢复失败。数据不做跨版本迁移；将来若有格式变化，需另行制定迁移与数据备份流程。
- 安装器拒绝覆盖不属于本安装的服务文件，以及摘要发生变化的用户定制服务文件。应备份后人工合并。
- 目标非空且没有安装元数据时拒绝覆盖；老版手动安装需先停止服务、备份数据，选择新目录安装，再在服务停止时迁移 YAML。路径更换需人工迁移服务文件。
- 异常退出遗留数据锁 `data/.lock` 或安装锁 `.install-lock` 时，需分别确认没有服务或安装进程后人工处理。不会修改厂商 SSH、防火墙或 NVRAM。

程序版本正确不等于该固件上的广播、开机启动都已验证；macOS launchd、真实 systemd/procd 与路由器仍需对应系统实测。当前安装器测试在 Linux 运行，下载、服务管理的故障场景使用隔离夹具验证。

## 维护者发布与验证

```sh
go test -race ./...
go vet ./...
python3 tests/installer/test_install.py
VERSION=v0.1.0 ./scripts/build.sh
# Linux 开发机可选：真实二进制安装与 HTTP 就绪集成检查
go build -ldflags='-X main.version=v0.0.0-test' -o bin/shell-wol-install-test ./cmd/shell-wol
python3 tests/installer/smoke_native.py bin/shell-wol-install-test
```

Release 工作流仅在推送版本标签时运行；准备好版本后再推送标签。上述本地命令只生成文件，不发布。`dist/` 包含二进制、install.sh、服务模板、VERSION、SHA256SUMS；构建脚本兼容 sha256sum 与 macOS shasum。
