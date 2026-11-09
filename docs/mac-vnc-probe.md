# Mac 屏幕共享分阶段验证工具

`mac-vnc-probe` 是独立的纯 Go 命令行程序，用来找出 Mac 在屏幕共享连接的哪个阶段亮屏。它不接入 Shell-WoL 的 Web 服务，不读取或修改现有设备数据，也不需要安装 TigerVNC、RealVNC 或 Mac 端辅助软件。

## 支持范围

| `--stage` | 到达此处后停止 | 客户端发送的数据 |
| --- | --- | --- |
| `tcp`（默认） | TCP 连接成功 | 不发送 RFB 应用数据，不读取密码 |
| `auth` | 收到成功的 `SecurityResult` | RFB 版本、DH(30) 类型选择、DH 公钥和加密的用户名密码；不发送 `ClientInit` |
| `init` | 完整读取 `ServerInit` | 在认证之后发送共享模式 `ClientInit`；不请求画面，不发送键鼠或剪贴板事件 |

认证阶段支持 RFB 3.7/3.8 的 Apple DH 安全类型 30。服务器若通告 `3.889` 等高于 3.8 的 3.x 版本，客户端选择 3.8。若没有提供 DH(30)，工具会列出认证类型编号并停止，不自动切换为无认证或传统 VNC 密码认证。DH 参数限制为 128–512 字节（1024–4096 位），用户名和密码各为 1–63 个 UTF-8 字节，不接受 NUL 或换行，不截断超长输入。

这是用于实机验证的实验工具。模拟服务器测试验证了报文格式、各阶段停止位置和资源边界；尚需在目标 M 系列 Mac、macOS 27 上确认兼容性和亮屏效果。协议认证成功不证明物理显示器已经点亮，也不证明系统刚刚从睡眠恢复；请同时观察屏幕并对照 Mac 的日志。

## 准备 Mac 和测试环境

1. 开启 macOS 的屏幕共享，确认测试账户具备相应访问权限。先用现有 TigerVNC/RealVNC 验证该账户可连接。
2. 固定 Mac 的局域网 IP（例如路由器 DHCP 地址保留），并确认屏幕共享端口，通常为 5900。
3. 关闭其他 VNC 客户端及其自动重连，暂停 WoL、SSH 和自动端口检测。每轮从 Mac 菜单选择睡眠，等待约 30～60 秒，再只运行本轮命令。
4. 观察物理显示器是否亮起、是否显示锁屏。各轮使用同样的等待条件；找出能亮屏的最短阶段后重复验证几次。Mac 的后台活动仍可能影响结果。

## Windows PowerShell

将 `dist/mac-vnc-probe/mac-vnc-probe-windows-amd64.exe` 复制到 Windows 电脑上的独立目录；Windows ARM64 使用对应 `windows-arm64.exe`。在该目录打开 PowerShell，替换 IP 和用户名：

```powershell
# A：只连接 TCP，不需要用户名密码
.\mac-vnc-probe-windows-amd64.exe --host 192.168.1.100 --stage tcp

# B：认证成功后立即断开；出现提示时输入 Mac 密码，输入不显示
.\mac-vnc-probe-windows-amd64.exe --host 192.168.1.100 --user yourname --stage auth

# C：完成共享会话初始化后立即断开
.\mac-vnc-probe-windows-amd64.exe --host 192.168.1.100 --user yourname --stage init

# 只在同一个阶段改变保持时间，比较时每轮都重新让 Mac 睡眠
.\mac-vnc-probe-windows-amd64.exe --host 192.168.1.100 --user yourname --stage auth --hold 2s
```

默认端口为 5900，可用 `--port` 指定。`--hold` 默认 `0s`，到达目标阶段即主动断开；保持期间检测到服务器提前关闭连接会报错。`--timeout` 默认 `30s`，覆盖网络连接、协议处理和保持时间，不计入输入密码的时间，最多可设置 `2m`。`hold` 必须小于 `timeout`。

密码在建立连接之前收集，所以可以从输出的“开始 TCP 连接”时间确定本轮网络测试起点。工具一次只连接一回，不自动重试网络或密码，不发送魔术包。不要在连接期间同时用其他软件探测 Mac。

## macOS、Linux 和路由器

在运行工具的设备上使用匹配架构的二进制，例如：

```sh
chmod +x ./mac-vnc-probe-linux-amd64
./mac-vnc-probe-linux-amd64 --host 192.168.1.100 --user yourname --stage auth
```

macOS Apple 芯片使用 `darwin-arm64`，Intel 使用 `darwin-amd64`；Linux ARM64 使用 `linux-arm64`，OpenWrt 及兼容固件按 CPU 选择 Linux 版本。它测试的是目标 Mac，工具自身不必运行在 Mac 上。

默认要求在真实终端中隐藏输入密码。自动化时可以明确使用 `--password-stdin`，从安全提供的标准输入读取一行；支持 LF、CRLF 和 EOF 结束。不接受 `--password` 参数或密码环境变量。避免把密码写入命令、脚本、终端历史或仓库文件。

## 日志与结果

输出含本地日期、毫秒和 UTC 偏移，例如：

```text
2026-11-09 14:20:01.105 +0800 TCP 已连接
2026-11-09 14:20:01.110 +0800 服务器提供的认证类型：30
2026-11-09 14:20:01.112 +0800 已选择 DH(30) 认证
2026-11-09 14:20:01.148 +0800 认证数据已发送，等待认证结果
2026-11-09 14:20:01.193 +0800 认证成功
2026-11-09 14:20:01.194 +0800 已到达 auth 阶段
```

工具不输出用户名、密码、密钥、服务器桌面名称或服务器返回的任意错误文本，不保存凭据或屏幕画面。程序返回 0 表示目标协议阶段和指定保持时间完成，物理亮屏结果由人观察。

| 退出码 | 含义 |
| --- | --- |
| 0 | 指定阶段完成，或显示帮助/版本 |
| 1 | 连接、协议、超时或保持连接失败 |
| 2 | 参数或密码输入无效 |
| 3 | 服务器拒绝认证或报告认证尝试过多 |
| 130 | 取消操作 |

认证失败后先检查用户名、密码和屏幕共享权限，避免反复尝试触发 Mac 账户限制。需要日志时，在测试结束后从 Mac 查看对应时间段：

```sh
pmset -g log | grep -Ei 'Sleep|Wake|DarkWake' | tail -n 80
```

连接 SSH 本身也可能改变睡眠状态，请在该轮观察完成后再查看日志。Mac 和测试电脑的时钟应一致，或根据输出中的时区偏移对照。

## 凭据与网络边界

Apple DH 协议在认证中使用 MD5 派生 AES-128 密钥，并按协议加密凭据。这不等于带证书身份验证的 TLS，也不提供对目标服务器身份的可靠校验；仅在可信局域网或可信的安全通道内测试，并确认目标 IP 正确。

## 构建与测试

项目工具链要求与主程序相同。终端隐藏输入使用 Go 官方 `golang.org/x/term`，保持 `CGO_ENABLED=0`，目标设备无需安装 Go。

```sh
go test -race ./internal/vncprobe ./cmd/mac-vnc-probe
go build -o bin/mac-vnc-probe ./cmd/mac-vnc-probe
python3 tests/vnc_probe/smoke_terminal.py ./bin/mac-vnc-probe # POSIX 终端验证
bash scripts/build-vnc-probe.sh
```

交叉构建输出到独立目录 `dist/mac-vnc-probe/`，包含 9 个平台/架构二进制、`VERSION` 和 `SHA256SUMS`。它不加入现有 Shell-WoL Release 资产或安装器，也不会自动发布。实际 Windows 终端、Mac 服务兼容性和物理屏幕效果仍需在真实设备上验证。
