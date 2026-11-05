package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
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
  serve       启动 Web 管理（默认 127.0.0.1:8080）
  init        创建 YAML 数据；--password-stdin 从标准输入设置管理员密码
  wake        --device ID 或 --mac MAC [--broadcast IPv4 --port 9 --repeat 3 --interface 名称]
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
	if command != "init" && command != "serve" && command != "wake" {
		return fmt.Errorf("未知命令 %q，使用 --help 查看帮助", command)
	}
	f := flag.NewFlagSet(command, flag.ContinueOnError)
	dir := f.String("data-dir", "./data", "YAML 数据目录")
	var listen, device, mac, broadcast, iface *string
	var port, repeat *int
	var password *bool
	switch command {
	case "serve":
		listen = f.String("listen", "", "覆盖监听地址")
	case "init":
		password = f.Bool("password-stdin", false, "从标准输入读取管理员密码")
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
	if f.NArg() != 0 {
		return errors.New("不支持额外的位置参数")
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
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if e = app.Wake(ctx, d); e != nil {
			return e
		}
		fmt.Println("唤醒包已发送（尚未确认设备上线）")
		return nil
	}
	store, e := app.OpenStore(*dir)
	if e != nil {
		return e
	}
	defer store.Close()
	switch command {
	case "init":
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
		if e = app.ValidateListen(addr, c.PasswordHash != ""); e != nil {
			return e
		}
		server := app.NewServer(store)
		if c.PasswordHash == "" {
			fmt.Println("首次启动：请在本机打开管理界面，使用以下一次性令牌设置密码：")
			fmt.Println(server.SetupToken())
		}
		httpServer := &http.Server{Addr: addr, Handler: server.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		done := make(chan error, 1)
		go func() { done <- httpServer.ListenAndServe() }()
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
