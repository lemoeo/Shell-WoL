package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/lemoeo/Shell-WoL/internal/app"
)

var version = "dev"

func main() {
	if e := run(os.Args[1:]); e != nil {
		slog.Error(e.Error())
		os.Exit(1)
	}
}
func run(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" {
		fmt.Println(`Shell-WoL — 网络唤醒工具
命令：
  serve       启动 Web 管理（默认 127.0.0.1:9000）
  init        创建 YAML 数据；--password-stdin 从标准输入设置管理员密码
  wake        --device ID 或 --mac MAC [--broadcast IPv4 --port 9 --repeat 3 --interface 名称]
  check       只读校验 YAML；--require-password 检查密码；--running 检查服务接口
  token       查看当前服务的初始化令牌（仅本地账户）
  lock        检查数据锁；POSIX 维护可使用 lock --data-dir PATH -- 命令 参数
  interfaces  列出 IPv4 网卡与广播地址
  version     查看版本
所有数据命令支持 --data-dir PATH（默认 ./data）；serve 支持 --listen HOST:PORT。
示例：shell-wol wake --mac AA:BB:CC:DD:EE:FF --broadcast 192.168.1.255`)
		return nil
	}
	if args[0] == "version" {
		fmt.Println(version)
		return nil
	}
	if args[0] == "interfaces" {
		n, e := app.Networks()
		if e != nil {
			return e
		}
		return json.NewEncoder(os.Stdout).Encode(n)
	}
	command := args[0]
	if command != "init" && command != "serve" && command != "wake" && command != "check" && command != "token" && command != "lock" {
		return fmt.Errorf("未知命令 %q，使用 --help 查看帮助", command)
	}
	f := flag.NewFlagSet(command, flag.ContinueOnError)
	dir := f.String("data-dir", "./data", "YAML 数据目录")
	var listen, device, mac, broadcast, iface *string
	var port, repeat *int
	var password, requirePassword, running *bool
	var logLimit *int
	switch command {
	case "check":
		requirePassword = f.Bool("require-password", false, "要求管理员已初始化")
		running = f.Bool("running", false, "请求本机服务接口验证就绪")
	case "serve":
		listen = f.String("listen", "", "覆盖监听地址")
	case "init":
		password = f.Bool("password-stdin", false, "从标准输入读取管理员密码")
		logLimit = f.Int("log-limit", -1, "初始化操作记录数量；-1 保留当前值")
	case "wake":
		device = f.String("device", "", "已保存设备 ID")
		mac = f.String("mac", "", "目标 MAC")
		broadcast = f.String("broadcast", "", "IPv4 广播地址")
		iface = f.String("interface", "", "发送网卡")
		port = f.Int("port", 0, "目标 UDP 端口")
		repeat = f.Int("repeat", 0, "发送次数")
	}
	if e := f.Parse(args[1:]); e != nil {
		if errors.Is(e, flag.ErrHelp) {
			return nil
		}
		return e
	}
	provided := map[string]bool{}
	f.Visit(func(v *flag.Flag) { provided[v.Name] = true })
	if f.NArg() != 0 && command != "lock" {
		return errors.New("不支持额外的位置参数")
	}
	if command == "lock" {
		lock, err := app.AcquireDataLock(*dir)
		if err != nil {
			return err
		}
		defer lock.Close()
		if f.NArg() != 0 {
			return lock.Run(f.Args())
		}
		fmt.Println("数据锁可用")
		return nil
	}
	if command == "token" {
		token, err := app.ReadSetupToken(*dir)
		if errors.Is(err, app.ErrAdminInitialized) {
			fmt.Println(app.ErrAdminInitialized)
			return nil
		}
		if err != nil {
			return err
		}
		fmt.Println(token)
		return nil
	}
	if command == "check" {
		if err := app.CheckData(*dir, *requirePassword, *running); err != nil {
			return err
		}
		fmt.Println("检查通过")
		return nil
	}
	if command == "wake" && *device == "" && *mac == "" {
		return errors.New("请指定 --device 或 --mac")
	}
	if command == "wake" {
		if *device != "" && *mac != "" {
			return errors.New("--device 与 --mac 不能同时使用")
		}
		c, d, e := app.ReadWakeDefaults(*dir, *device)
		if e != nil {
			return e
		}
		if *device == "" {
			d = app.Device{ID: "cli", Name: "命令行设备", MAC: *mac, Broadcast: c.Broadcast, Port: c.Port, Repeat: c.Repeat}
		}
		if *broadcast != "" {
			d.Broadcast = *broadcast
		}
		if provided["port"] {
			d.Port = *port
		}
		if provided["repeat"] {
			d.Repeat = *repeat
		}
		if provided["interface"] {
			d.Interface = *iface
		}
		if d.UsesVNC() && (provided["port"] || provided["repeat"] || provided["broadcast"] || provided["interface"]) {
			return errors.New("Mac 模式请通过设备配置设置端口和参数，不接受 WoL 命令行覆盖")
		}
		var secret []byte
		if d.UsesVNC() {
			secret, e = app.ReadDevicePassword(*dir, d)
			if e != nil {
				return e
			}
			defer clear(secret)
		}
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		result := app.ExecuteWake(ctx, d, secret, func(message string) { fmt.Println(message) })
		fmt.Println(result.Message)
		if result.Status != "done" {
			return errors.New("唤醒流程未全部完成，请检查以上结果")
		}

		return nil
	}
	store, e := app.OpenStore(*dir)
	if e != nil {
		return e
	}
	defer store.Close()
	switch command {
	case "init":
		if *logLimit != -1 {
			c := store.Config()
			c.LogLimit = *logLimit
			if e = store.SaveConfig(c); e != nil {
				return e
			}
		}
		if *password {
			b, e := io.ReadAll(io.LimitReader(os.Stdin, 1025))
			if e != nil {
				return e
			}
			if len(b) > 1024 {
				return errors.New("输入过长")
			}
			if e = store.SetPassword(strings.TrimRight(string(b), "\r\n")); e != nil {
				return e
			}
		}
		fmt.Println("YAML 数据已初始化：", *dir)
		return nil
	case "serve":
		c := store.Config()
		addr := c.Listen
		if *listen != "" {
			addr = *listen
		}
		if e = app.ValidateListen(addr); e != nil {
			return e
		}
		listener, e := net.Listen("tcp", addr)
		if e != nil {
			return e
		}
		defer listener.Close()
		server := app.NewServer(store)
		defer server.Close()
		if e = store.PublishSetupToken(server.SetupToken()); e != nil {
			return fmt.Errorf("保存初始化令牌: %w", e)
		}
		if c.PasswordHash == "" {
			fmt.Println("首次启动：请打开管理界面，使用以下初始化令牌设置密码（本次启动有效）：")
			fmt.Println(server.SetupToken())
		}
		httpServer := &http.Server{Addr: addr, Handler: server.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		done := make(chan error, 1)
		go func() { done <- httpServer.Serve(listener) }()
		slog.Info("Web 管理服务启动", "listen", addr)
		select {
		case e = <-done:
			if errors.Is(e, http.ErrServerClosed) {
				return nil
			}
			return e
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return httpServer.Shutdown(shutdown)
		}
	}
	return nil
}
