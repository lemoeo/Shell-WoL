# 安装、升级和后台服务

安装器使用 POSIX `sh`，目标设备不需要 Go、Bash、Python 或 Node.js。支持 macOS、标准 Linux 和满足运行条件的 OpenWrt/二次开发固件。Windows 继续下载 `.exe` 后直接运行。

## 发布前提与安装入口

维护者推送 `v主版本.次版本.补丁` 标签后，GitHub Actions 执行测试、交叉编译、发布固定的 CDN 资产标签，再发布 Release 并更新 CDN 最新版本。预发布标签可使用 `v0.1.0-beta.1`，会标记为 prerelease，且不更新 CDN 最新版本。Release 只上传各平台二进制、VERSION 和二进制 SHA256SUMS；CDN 同步相同构建文件。安装脚本和三份服务模板保留在源码仓库，不作为 Release 附件；仅推送源码不会自动发布二进制。没有 Release 时，可先使用本地构建。

参考 [ShellCrash 中文安装说明](https://github.com/juewuy/ShellCrash/blob/dev/README_CN.md)，可以设置入口地址后直接安装，并在成功后加载菜单别名。Linux 和路由器的系统安装请在 root 终端执行；Linux 可先使用 `sudo -i`。

GitHub 源：

```sh
export url='https://raw.githubusercontent.com/lemoeo/Shell-WoL/main' \
  && install_script="$(curl -fsSL "$url/scripts/install.sh")" \
  && sh -c "$install_script" install.sh --source github \
  && . /etc/profile >/dev/null 2>&1
```

jsDelivr CDN 源：

```sh
export url='https://testingcf.jsdelivr.net/gh/lemoeo/Shell-WoL@main' \
  && install_script="$(curl -fsSL "$url/scripts/install.sh")" \
  && sh -c "$install_script" install.sh --source jsdelivr --cdn-base https://testingcf.jsdelivr.net \
  && . /etc/profile >/dev/null 2>&1
```

两条命令都从源码 `main/scripts/install.sh` 下载安装入口；CDN 命令同时指定后续二进制、模板及菜单使用的 CDN。替换域名时须同步修改 `url` 和 `--cdn-base`，例如均改为 `cdn.jsdelivr.net`。

`sh -c` 后的 `install.sh` 是子 Shell 的 `$0`（脚本名称占位），`--source` 等后续参数传给安装器；这里不会读取名为 install.sh 的本地文件。

命令先确认下载成功，再执行脚本；保留 HTTPS 证书校验。安装成功后才加载 `/etc/profile`，使用兼容 POSIX sh 的 `>/dev/null 2>&1` 重定向，随后可直接输入 `wol`。安装器仍会询问安装目录、是否注册后台服务等选项，升级使用相同命令。固定安装源后不会再询问源选择。

macOS 默认 zsh 用户安装使用个人 profile：

```sh
export url='https://raw.githubusercontent.com/lemoeo/Shell-WoL/main' \
  && install_script="$(curl -fsSL "$url/scripts/install.sh")" \
  && sh -c "$install_script" install.sh --source github --user \
  && . "$HOME/.zshrc" >/dev/null 2>&1
```

Linux `--user` 安装、macOS Bash 或其他 Shell，请按安装器提示加载对应的 `~/.bashrc`、`~/.bash_profile` 或 `~/.profile`。不手动加载时，重新登录也可生效。

也可先下载查看脚本，再执行，便于传入其他选项：

```sh
curl -fsSL https://raw.githubusercontent.com/lemoeo/Shell-WoL/main/scripts/install.sh -o install.sh
# 查看 install.sh 后执行；系统安装使用 root，用户安装加 --user
sh install.sh
```

`--version <版本标签>` 仅固定二进制版本。服务模板、菜单、启动脚本及部署校验清单始终从 main 下载，不跟随二进制版本切换。

文件来源如下：

| 内容 | GitHub 源 | jsDelivr 源 |
| --- | --- | --- |
| 安装入口 | 源码 `main/scripts/install.sh` | 源码 `@main/scripts/install.sh` |
| 最新正式版本信息 | Release 的 `latest/download/VERSION` | 资产分支 `@assets/VERSION` |
| 二进制及其 SHA256SUMS | Release 的 `download/<版本>/` | 固定资产标签 `@assets-<版本>/` |
| 服务模板 | 源码 `main/deploy/` | 源码 `@main/deploy/` |
| 终端菜单和启动脚本 | 源码 `main/scripts/` | 源码 `@main/scripts/` |
| 部署文件校验清单 | 源码 `main/deploy/SHA256SUMS` | 源码 `@main/deploy/SHA256SUMS` |

首次解析 latest 后，二进制与其 SHA256SUMS 固定到同一 Release 版本。服务模板、菜单、启动脚本及部署校验清单使用 main 的最新内容；安装器仅下载一份部署清单校验本次需要的文件。仅访问所选源，下载或校验失败直接停止；需要换源时重新执行并指定 `--source`。

菜单校验通过后安装为根目录的 `menu.sh`，通过 profile 别名 `wol` 进入，见 [终端菜单](terminal-menu.md)。脚本或模板修改后，同步更新 `deploy/SHA256SUMS` 并推送 main 即可分发，不需要创建新标签或发布二进制 Release。已安装设备在下次运行安装器时更新本地文件；旧 Release 已上传的历史脚本和模板无需删除。

支持有证书校验及 `--https-only` 的 wget；精简 BusyBox wget 不一定具备这些能力，遇到提示应先通过固件支持的方式安装 curl 和 CA 证书，不能跳过 TLS 校验。

安装器、卸载器和生成的持久启动脚本保留 SHA256 校验，按以下顺序选择可用工具：`sha256sum` → `busybox sha256sum` → `shasum -a 256` → `openssl dgst -sha256`。探测会实际计算空输入并核对已知摘要；独立命令缺失、BusyBox 未编入对应 applet、执行失败或输出无效时，继续尝试下一种。四种方式都不可用时明确报错并停止，不跳过校验。

精简小米固件没有独立的 `sha256sum` 命令时，可以先用 `busybox sha256sum /dev/null` 或 `openssl dgst -sha256 /dev/null` 检查已有能力，无需先安装完整 GNU 工具。选定工具后，文件读取或校验失败仍会中止，已有安装不会因这类失败被替换。

交互模式显示系统、固件、架构及推荐根目录，允许选择安装源、修改目录、选择后台服务，最后确认安装。通过 `/dev/tty` 读取选择；自动化环境使用 `--yes`。脚本不自行调用 sudo 提权；macOS 普通用户默认用户安装，Linux 系统安装需管理员执行。

## 安装源和 CDN

`--source github|jsdelivr` 控制本次安装或升级，不提供 auto 选项：

- `github`（默认，未传 `--source` 时使用）：只使用 GitHub Release 和 raw 源，失败时停止。
- `jsdelivr`：只使用配置的 CDN，失败时停止，不隐式切换其他域名。

`--cdn-base` 是 HTTPS 源站地址，不带 `/gh` 或仓库路径。默认 `https://cdn.jsdelivr.net`，可改为 `https://testingcf.jsdelivr.net`；环境变量 `SHELL_WOL_CDN_BASE` 可以设置 CDN 默认地址，命令行优先。安装源不受环境变量覆盖，使用 `--source` 或交互菜单显式选择；源选择不写入应用 YAML，每次执行可调整。

```sh
# 默认 CDN
sh install.sh --source jsdelivr --service --yes

# ShellCrash 使用的 Cloudflare CDN 域名
sh install.sh --source jsdelivr --cdn-base https://testingcf.jsdelivr.net --service --yes

# 在当前终端设置 CDN 默认地址，后续无需重复传 --cdn-base
export SHELL_WOL_CDN_BASE=https://testingcf.jsdelivr.net
sh install.sh --source jsdelivr --service --yes
```

安装入口本身也需要可访问。上面的 CDN 安装命令同时配置入口与后续文件的源站；若已下载安装脚本，可继续使用 `sh install.sh --source jsdelivr --cdn-base https://testingcf.jsdelivr.net`。jsDelivr 缓存可能延迟更新，需要最新入口时可改用 GitHub 源。

以 `v0.1.2` 为例，二进制使用 `https://cdn.jsdelivr.net/gh/lemoeo/Shell-WoL@assets-v0.1.2/shell-wol-linux-arm64`，模板使用 `https://cdn.jsdelivr.net/gh/lemoeo/Shell-WoL@main/deploy/shell-wol.openwrt`。jsDelivr 读取仓库文件，不能直接镜像 Release 附件；维护者必须先同步资产标签。旧 Release 未同步时，CDN 安装会失败，可重新执行脚本并选择 GitHub 源。

CDN 的分支文件可能有缓存延迟。二进制版本只从 `@assets/VERSION` 解析一次，再按固定资产标签下载；模板和脚本继续读取 `@main`。若部署文件与清单因缓存暂时不同步，SHA256 校验失败时会在停服前中止，可等待缓存更新或改用 GitHub 源。`--version v0.1.2` 仅指定二进制版本，不改变脚本来源。

元数据连接超时为 5 秒，VERSION 请求上限 10 秒、校验清单和模板上限 20 秒、二进制下载上限 180 秒（curl）；wget 使用对应的单次超时并关闭自动多次重试。网络失败、SHA256 不匹配、版本不符或配置错误都会停止，不自动换源。

## 路径与选项

| 平台/模式 | 默认安装根目录 |
| --- | --- |
| Linux 系统安装 | `/opt/shell-wol` |
| 小米固件，检测到 `/data/etc/crontabs/root` 且 `/data` 可写 | `/data/shell-wol` |
| 标准 OpenWrt/兼容固件 | `/etc/shell-wol` |
| macOS 用户安装（普通用户默认） | `~/Library/Application Support/Shell-WoL` |
| macOS 系统安装（root 执行） | `/Library/Application Support/Shell-WoL` |
| Linux `--user` | `${XDG_DATA_HOME:-~/.local/share}/shell-wol` |

固件特征用于推荐位置及自动选择启动方式，并不保证该型号兼容。小米特征优先于通用 OpenWrt；刷入标准 OpenWrt 且没有该特征的设备使用 OpenWrt 路径。安装前仍需确认所选分区可执行、持久可写且空间足够。Linux 内核环境下即使固件来源未知，也可只安装程序并手动运行；无法识别服务机制时 `--service` 会失败。

```text
安装根目录/
├── bin/shell-wol
├── data/                 YAML 数据及凭据密钥；初始化后默认私有权限
├── backup/shell-wol.previous
├── menu.sh              简单交互菜单及服务启停
├── boot.sh              仅 firewall 模式，从 scripts/boot.sh 安装
├── service/shell-wol.procd  仅 firewall 模式，服务文件的持久副本
└── .shell-wol-install    安装器版本、服务、启动模式及文件摘要
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

支持参数：`--version`、`--arch`、`--install-dir`、`--user`、`--service`、`--boot-mode`、`--yes`、`--source`、`--cdn-base`、`--download-base`、`--repo-base` 和 `--help`。

`--boot-mode auto|native|firewall` 需配合 `--service` 或已有后台服务：

- `auto`（默认）：保留已记录的启动模式；没有模式记录时，检测到小米固件且使用 procd 就选择 firewall，其他系统使用 native。因此旧安装器创建的小米 procd 安装可在升级时自动迁移。
- `native`：沿用 systemd、procd 或 launchd 自启动；从 firewall 模式切换时移除本安装的 include 和持久启动文件。
- `firewall`：仅支持系统级 procd，要求 uci、ubus 及可用的 firewall 配置。为兼容 fw3 未引用 include 路径的行为，此模式安装目录不能含空格。小米设备须使用 `/data` 下的独立目录，确保启动脚本在防火墙启动时已经可访问。不会自动回退到可能无法持久化的原生启动方式。

`boot.sh` 来自仓库 main 中的 [`scripts/boot.sh`](../scripts/boot.sh)，仅 firewall 模式下载。启动脚本和 procd 模板均按 main 的 `deploy/SHA256SUMS` 校验，安装时替换脚本里的 `@ROOT@` 路径；开机恢复不依赖网络。仅更新启动脚本无需发布程序 Release。详细启动链路、前提及管理命令见 [路由器持久自启动](openwrt.md#镜像固件的持久自启动)。

- `--download-base` 是 HTTPS Release 根地址，镜像保持 `latest/download/VERSION` 和 `download/<版本>/<资产名>` 结构。
- `--repo-base` 是 HTTPS 仓库文件根地址，不包含分支或标签；镜像保持 `main/deploy/SHA256SUMS`、`main/deploy/<模板名>` 和 `main/scripts/<脚本名>` 结构。
- 两类地址独立配置，不会用 Release 镜像地址推断仓库镜像。仓库镜像提供 main 下的最新脚本和模板，Release 镜像提供指定版本的二进制。
- 显式自定义地址优先于 `--source`，对应文件只访问该镜像，失败时不会隐式回退官方源；另一类文件仍遵循所选源策略。jsDelivr 使用独立的 `@版本` 地址格式，应通过 `--cdn-base` 配置，不能把 CDN 地址直接传给 `--repo-base` 或 `--download-base`。

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

首次安装执行不含密码的 `init`，生成默认 YAML；路由器默认 `log_limit: 0`，减少操作记录写闪存。安装器不接收或保存密码、不改变 LAN 监听和端口规则；firewall 模式仅注册本工具的启动 include。默认只监听 `127.0.0.1:9000`。

服务模式检测如下：

| 环境 | 后台方式与账户 |
| --- | --- |
| Linux PID 1 为 systemd | systemd，创建/使用非 root 的 shell-wol 用户 |
| OpenWrt PID 1 为 procd，且有 rc.common | procd，按路由器默认账户运行 |
| macOS 用户安装 | LaunchAgent，当前用户；需要可用的 GUI 登录会话 |
| macOS 系统安装 | LaunchDaemon，daemon 用户 |

系统安装的程序及元数据由安装账户管理，后台服务仅需写 data；用户安装的程序与服务属于同一账户。服务文件放在操作系统要求的 systemd/init.d/LaunchAgents/LaunchDaemons 目录。模板由安装器填充，不能直接复制含 `@ROOT@` 的模板启动。

首次使用 `--service` 会注册、启用并启动服务，尚未设置管理员密码也能启动。安装器使用 `check --running` 请求实际 Web 接口验证就绪；“待初始化”和“已初始化”均属于可用的服务状态。firewall 模式的升级保留原有启用/禁用及运行/停止状态。

首次通过浏览器设置密码：

1. 本机访问默认地址；局域网访问需先在服务停止时将 `data/config.yaml` 中 `listen` 改为 LAN 地址或 `0.0.0.0:9000`，然后启动服务。默认不会自动开放 LAN 监听。
2. 从当前启动的终端或服务日志取得随机初始化令牌。
3. 打开管理页面，输入令牌，设置 12–72 字节的密码。
4. 使用新密码登录；不需要重启服务。

令牌不会通过 Web 返回，重启前的旧令牌失效；初始化成功后不能再次通过初始化接口更改密码。设备管理、配置、记录和唤醒仍要求登录，初始化接口保留同源检查和请求限流。

查看当前启动的令牌：

```sh
# Linux systemd
sudo journalctl -u shell-wol -n 30 --no-pager

# 新版 OpenWrt/小米固件，无需 logread
wol token
# 或：/data/shell-wol/bin/shell-wol token --data-dir /data/shell-wol/data
# 旧版仍使用 logread -e shell-wol

# macOS 用户安装
cat "$HOME/Library/Application Support/Shell-WoL/data/service.log"

# macOS 系统安装
sudo cat "/Library/Application Support/Shell-WoL/data/service.log"
```

新程序优先使用 `token` 命令：它读取私有 `data/.setup-token`，核对当前服务锁及实例标识；已初始化则不显示历史令牌。日志仍可能保留旧令牌，不要公开令牌或启动日志。文件生命周期及权限见 [运行状态文件](runtime-state.md)。也可先停止服务，再通过 `init --password-stdin` 设置密码；Linux systemd 使用 `sudo -u shell-wol`，macOS 系统安装使用 `sudo -u daemon`，用户安装使用当前用户，路由器通常使用 root。不要用错误账户生成仅该账户可读的 0600 配置文件。

路由器需检查 LAN/WAN 防火墙。桌面模式可直接运行 `serve`，局域网模式可加 `--listen 0.0.0.0:9000`。不要同时运行手动进程和使用同一数据目录的后台服务。

## 升级、冲突与回退

再次运行安装器可升级；已有安装的服务类型会自动保留，不需要再次传 `--service`。

- 下载、SHA256 和版本运行检查在替换程序前完成；服务模板和脚本根据 main 的 `deploy/SHA256SUMS` 验证摘要，和 Release 的二进制校验清单分开处理。
- 升级前只读校验已有 YAML，停止本安装管理的服务，再通过 Go 检查内核锁。文件 `data/.lock` 可以保留；旧版目录锁仍需确认无进程后人工迁移。
- 程序替换后保留旧程序为 `backup/shell-wol.previous`，已有 YAML 不改写、不重置密码。
- 升级保留原来的运行和开机自启状态：已停止的服务不会自动启动，已禁用的自启不会自动开启；需要启动时仍执行实际就绪检查。
- 启动服务后会实际请求配置地址的 `/api/session`，未初始化时也应正常响应；仅有进程或服务状态不足以通过就绪检查。
- 启动失败会尝试恢复旧程序、服务文件、安装元数据和原有启用/运行状态，并明确报告恢复失败。数据不做跨版本迁移；将来若有格式变化，需另行制定迁移与数据备份流程。
- 安装器拒绝覆盖不属于本安装的服务文件，以及摘要发生变化的用户定制服务文件。应备份后人工合并。
- 目标非空且没有安装元数据时拒绝覆盖；老版手动安装需先停止服务、备份数据，选择新目录安装，再在服务停止时迁移 YAML。路径更换需人工迁移服务文件。
- firewall 模式还备份和回滚本安装的 include、持久启动文件及禁用标记；发现其他未提交的 firewall 修改或同名 section 指向其他安装时停止，不会覆盖 ShellCrash 或其他 section。切换到 native 时同样清理和回滚本安装入口。
- 新版数据锁由系统自动释放，无需因 `data/.lock` 文件存在而人工清理。旧版目录锁的迁移及异常中断安装留下的 `.install-lock` 仍需确认进程及事务状态后处理。持久启动操作使用 `/tmp/shell-wol-boot.lock` 与安装、卸载互斥；只清理自己创建的锁，不自动删除其他遗留锁。不会修改厂商 SSH、NVRAM 或端口规则。

firewall 安装使用 schema 2 元数据，旧版安装器/卸载器会拒绝处理，防止遗漏启动入口；请使用仓库最新的安装和卸载脚本。普通 native 安装继续使用 schema 1。两种安装记录均增加 `install_mode=user|system`，指定已有目录时会沿用原安装模式；模式冲突会报错。旧记录没有该字段时继续按原方式传入 `--user`，成功升级后补齐。默认卸载保留该字段，供重新安装使用。

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
| `menu.sh` 和本安装的 `wol` 别名 | 移除 | 移除 |
| 私有初始化令牌及本安装的运行锁文件 | 移除 | 移除 |
| 本安装的 firewall include、`boot.sh`、持久 procd 副本及禁用标记 | 移除 | 移除 |
| `bin/shell-wol`、`backup/shell-wol.previous` | 删除 | 删除 |
| `data/config.yaml`、`devices.yaml`、`events.yaml` | 保留 | 删除 |
| `data/service.log`、`service.err.log` | 保留 | 删除 |
| 最小安装元数据 | 标记为已卸载，保留 | 移除 |
| 未知文件、空的未知子目录 | 保留 | 保留 |
| 系统账户、journal/syslog 中的历史日志 | 保留 | 保留 |

脚本先验证安装元数据及服务命令的实际安装路径，再停止和禁用对应 systemd/procd/launchd 服务，确认服务停止后检查数据锁和本安装的可见运行进程。不会按进程名批量杀进程，也不直接递归删除根目录。仅删除明确列出的文件，最后尝试移除空目录。

firewall 模式在停止服务前，先写入持久禁用标记并移除本安装的 `firewall.ShellWoL`。如果 `/etc/init.d/shell-wol` 已丢失，从校验通过的持久副本恢复它，用于查询并停止原 procd 服务。预览不会恢复文件或提交 UCI；入口已移除但后续停止失败时，程序和数据保留且自启动维持禁用，修复后可以重试。

卸载器不再比较服务文件与安装记录中的摘要。修正 WorkingDirectory 引号、添加注释或调整服务参数后，只要服务命令仍指向本安装的程序和数据目录，就可卸载；不需要修改或重算元数据中的 `service_sha256`。如果服务指向另一个安装目录，仍会拒绝处理。此调整仅适用于卸载，安装器升级时对定制服务文件的覆盖保护保持不变。

卸载使用和安装器相同的 `.install-lock` 防止并发。新版程序通过 `lock -- 命令` 在整个同步数据清理阶段持有系统锁；兼容旧程序时仍使用目录锁。仅在停止服务、完成清理后移除本安装的运行文件。检测到手动运行进程、被占用的内核锁、遗留的旧版目录锁、无法判断服务状态、服务指向其他安装目录或路径为符号链接时，会中止；请先人工确认，不要盲目删除锁。Linux 检查可读取的 `/proc` 可执行路径，macOS 使用 ps；执行期间不要在另一个终端绕过数据锁启动程序。

服务停止、禁用或重新加载失败时，不继续删除程序和数据；已经完成的服务操作可能保留。处理错误后可再次执行：原服务已停止、已卸载或文件已不存在且状态仍可确认时可继续；普通 native procd 安装的脚本缺失，或 firewall 安装连持久副本也不可用时，需先人工处理。脚本不删除系统服务账户，以保持保留数据的归属，并支持重新安装。

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

Release 工作流仅在推送版本标签时运行；准备好版本后再推送标签。上述本地命令只生成文件，不发布。`dist/` 只包含二进制、VERSION、SHA256SUMS；Release 的 SHA256SUMS 仅列出二进制。构建脚本会清理旧版生成的 install.sh 与三份模板副本，不删除其他用户文件。构建侧校验工具兼容 sha256sum 与 macOS shasum；设备端安装、卸载和持久启动另支持 BusyBox 与 OpenSSL。

### CDN 资产发布与旧版本补齐

Release 工作流使用 `scripts/publish-assets.sh` 创建独立资产树，先推送 `assets-v具体版本` 标签，再发布 GitHub Release，最后把专用 `assets` 分支指向最新稳定快照。标签包含九个平台二进制、VERSION、SHA256SUMS 及归属标记，不包含源码、模板或多版本目录。使用私有 Git 索引，不切换或修改源码工作区；历史资产通过固定标签保留。

脚本验证九个文件的名称、摘要及 CDN 大小限制（单文件 20 MB、总量 150 MB），忽略目录中的其他文件。已有资产标签只允许内容完全相同的重复操作，内容不同则拒绝，不移动旧标签。最新分支有归属检查和并发更新保护；预发布和较旧版本的补齐不会替换较新的最新版本。`assets` 是程序管理的专用分支，请勿在其中放置自定义文件。

已有 Release（例如 `v0.1.2`）无需重新编译或重新发布程序：

1. 将本次工作流推送到仓库默认分支。
2. 在 GitHub **Actions → Sync CDN assets → Run workflow** 中填写现有版本，例如 `v0.1.2`。
3. 保持 `update_latest` 启用，脚本仅在该版本是较新的稳定版本时更新最新指针。
4. 工作流下载原 Release 文件并验证摘要，发布对应资产标签。完成后再用 `--source jsdelivr --version v0.1.2` 测试。

此手动工作流不修改已有 Release 附件，也不重新构建二进制。仅运行本地构建或推送安装脚本，不能让缺失的 CDN 二进制自动出现。

补齐要求原 Release 含当前九个平台二进制、VERSION 和匹配的二进制 SHA256SUMS；缺文件、清单格式不符或摘要不匹配会停止。Release 工作流重试时，如果 Release 已发布，会比较 VERSION 和 SHA256SUMS，仅一致时继续恢复最新分支，不覆盖已有附件或草稿。


服务模板修改后必须运行：

```sh
sh scripts/template-checksums.sh
sh scripts/template-checksums.sh --check
```

将模板与 `deploy/SHA256SUMS` 一起提交并推送 main 即可生效，无需创建版本标签。Tests、Release 工作流和构建脚本都会检查该清单；摘要过期会导致检查失败。不要在 Release 工作流中自动覆盖仓库清单来绕过检查。

该清单还包含 `scripts/menu.sh` 和 `scripts/boot.sh` 的摘要；修改任一脚本后运行同一个 `template-checksums.sh` 更新。仅更新 Shell 脚本不需要重发二进制；Go 程序发生变化时再重新编译并发布 Release。

## Mac VNC 凭据

启用 Mac 唤醒后，数据目录还包含加密的 `credentials.yaml` 与本机密钥 `credentials.key`。升级和默认卸载保留两者；`--purge` 将两者列入预览并删除。备份必须保护完整数据目录及对应文件权限，缺少密钥无法恢复密码。配置与只写密码行为见 [Mac VNC 唤醒指南](mac-vnc-wake.md)。

## 安装脚本结构

安装器依次执行参数解析、环境检测、安装规划、下载校验、备份停服、程序安装、服务配置、安装记录保存和结果提示。所有下载校验在停服之前完成；之后失败则尝试恢复旧程序、服务、启动脚本和安装记录。

`install.sh`、`boot.sh`、`menu.sh` 和 `uninstall.sh` 各自独立运行。启动脚本源码单独维护，安装器只负责下载、校验和替换安装路径，不再内嵌脚本正文。
