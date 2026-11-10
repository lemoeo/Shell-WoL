# 终端菜单

参考 [ShellClash](https://github.com/lemoeo/ShellClash) 的结构，只增加一个 `menu.sh`。Go 程序继续负责 Web 和唤醒，菜单直接调用已有的 systemd、procd 或 launchd 服务。

安装后重新登录，或按安装器提示加载 profile，即可输入：

```sh
wol          # 菜单：状态、PID、内存、Web 检查；启动、重启、停止、刷新
wol status
wol start
wol restart
wol stop
wol token    # 查看当前初始化令牌；不依赖系统日志工具
```

快捷命令采用 profile 别名。升级时将本安装的旧 `swol` 入口替换为 `wol`，保留其他自定义别名；若 `wol` 已被其他命令占用，安装器会提示冲突。系统安装用原安装账户操作；Linux 管理员也可直接执行 `sudo sh /opt/shell-wol/menu.sh`。别名本身不是独立可执行文件，不能直接用 `sudo wol`。没有配置后台服务时可查看状态，启停会提示重新运行安装器并加 `--service`。

完整路径始终可用，例如小米路由器：

```sh
sh /data/shell-wol/menu.sh
```

Linux 从 `/proc` 读取 PID 对应的 RSS；macOS 使用 `ps`。菜单核对程序路径和数据目录，避免显示另一个安装的进程；Web 状态由现有 `check --running` 检查。停止和重启交给系统服务，不强制杀死手动启动的进程，也不会删除已有数据锁。`status` 返回 0 表示进程和 Web 正常，1 表示未运行，2 表示无法确认或 Web 未就绪。

菜单不增加后台守护进程，也不管理安装源、自动更新、日志轮转或新的自启策略。停止服务保留现有的开机自启设置。小米 firewall 模式仍按原有 include 机制启动；防火墙重新加载可能再次启动已停止的程序，需要持续关闭自启时沿用 `boot.sh disable`。自启开启时，恢复任务也会按需补回 `/etc/profile` 中的别名；命令丢失时可先使用完整路径。

菜单从所选 GitHub/CDN 源的 `main/scripts/menu.sh` 下载，按 `main/deploy/SHA256SUMS` 校验后安装为根目录的 `menu.sh`。菜单、启动脚本和服务模板均跟随 main 更新，不绑定二进制版本或固定提交。更新脚本和校验清单并推送 main 即可分发，无需为 Shell 脚本单独发布程序 Release。本地菜单在下次运行安装器时更新。自定义仓库镜像需提供 main 下的相同文件结构。

新程序通过内核文件锁判断占用，锁文件存在不会阻止启动。菜单第 5 项和 `wol token` 查看私有令牌；当前管理员已初始化时不会显示历史令牌。新版二进制要求及旧锁迁移见 [运行状态文件](runtime-state.md)。

沿用原有 schema 1/2 安装记录，只增加菜单摘要和 profile 路径。升级保留数据，失败回滚菜单和别名；使用最新卸载器可一并清理二者，profile 的其他内容保留。
