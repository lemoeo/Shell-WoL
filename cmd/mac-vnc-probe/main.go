package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/lemoeo/Shell-WoL/internal/vncprobe"
	"golang.org/x/term"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, func() ([]byte, error) {
		return readHidden(ctx, os.Stdin, os.Stderr)
	})
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, input io.Reader, output, errorOutput io.Writer, hidden func() ([]byte, error)) int {
	c := vncprobe.Config{}
	var stage string
	var passwordStdin, showVersion bool
	flags := flag.NewFlagSet("mac-vnc-probe", flag.ContinueOnError)
	flags.SetOutput(errorOutput)
	flags.StringVar(&c.Host, "host", "", "Mac 的 IP 或主机名（不含端口）")
	flags.IntVar(&c.Port, "port", 5900, "屏幕共享 TCP 端口")
	flags.StringVar(&stage, "stage", "tcp", "停止阶段：tcp / auth / init")
	flags.StringVar(&c.Username, "user", "", "Mac 用户名，auth/init 必填")
	flags.DurationVar(&c.Hold, "hold", 0, "到达目标阶段后保持连接的时间，如 2s")
	flags.DurationVar(&c.Timeout, "timeout", 30*time.Second, "网络操作与保持连接的总时限，最多 2m")
	flags.BoolVar(&passwordStdin, "password-stdin", false, "从标准输入读取一行密码；默认在终端隐藏输入")
	flags.BoolVar(&showVersion, "version", false, "显示工具版本")
	flags.Usage = func() {
		fmt.Fprintln(errorOutput, "Mac 屏幕共享分阶段验证工具（DH 安全类型 30）。")
		fmt.Fprintln(errorOutput, "用法：mac-vnc-probe --host IP --stage tcp|auth|init [--user USER] [选项]")
		fmt.Fprintln(errorOutput, "一次连接，不自动重试，不发送 WoL、画面请求或键鼠事件。")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if showVersion {
		fmt.Fprintln(output, version)
		return 0
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(errorOutput, "错误：不接受位置参数；密码请在提示时输入或使用 --password-stdin")
		return 2
	}
	c.Stage = vncprobe.Stage(stage)
	if err := c.Validate(); err != nil {
		fmt.Fprintln(errorOutput, "错误：", err)
		return 2
	}
	if passwordStdin && c.Stage == vncprobe.TCP {
		fmt.Fprintln(errorOutput, "错误：tcp 阶段不需要密码，请去掉 --password-stdin")
		return 2
	}
	if c.Stage != vncprobe.TCP {
		var err error
		if passwordStdin {
			c.Password, err = readPasswordLine(input)
		} else {
			c.Password, err = hidden()
		}
		defer clear(c.Password)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				fmt.Fprintln(errorOutput, "已取消")
				return 130
			}
			fmt.Fprintln(errorOutput, "读取密码失败：", err)
			return 2
		}
		if err = vncprobe.ValidatePassword(c.Password); err != nil {
			fmt.Fprintln(errorOutput, "错误：", err)
			return 2
		}
	}
	log := func(message string) {
		fmt.Fprintf(output, "%s %s\n", time.Now().Format("2006-01-02 15:04:05.000 -0700"), message)
	}
	log("mac-vnc-probe " + version + "；仅在可信网络中测试，DH 认证不提供服务器证书身份校验")
	start := time.Now()
	err := vncprobe.Run(ctx, c, log)
	if err != nil {
		fmt.Fprintln(errorOutput, "测试失败：", err)
		if errors.Is(err, context.Canceled) {
			return 130
		}
		if errors.Is(err, vncprobe.ErrAuthRejected) || errors.Is(err, vncprobe.ErrAuthLimited) {
			return 3
		}
		return 1
	}
	log("连接已关闭；耗时 " + time.Since(start).Round(time.Millisecond).String())
	fmt.Fprintln(output, "协议阶段已完成；请自行观察 Mac 物理屏幕是否亮起，工具不判断亮屏结果。")
	return 0
}

func readPasswordLine(input io.Reader) ([]byte, error) {
	// 63 UTF-8 bytes plus CRLF fit in 65 bytes; an oversized line is rejected.
	line, err := bufio.NewReader(io.LimitReader(input, 66)).ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		clear(line)
		return nil, errors.New("无法读取标准输入")
	}
	if len(line) > 0 && line[len(line)-1] == '\n' {
		line = line[:len(line)-1]
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
	}
	if err := vncprobe.ValidatePassword(line); err != nil {
		clear(line)
		return nil, err
	}
	return line, nil
}

func readHidden(ctx context.Context, input *os.File, output io.Writer) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fd := int(input.Fd())
	if !term.IsTerminal(fd) {
		return nil, errors.New("标准输入不是终端；请在终端运行，或明确使用 --password-stdin")
	}
	state, err := term.MakeRaw(fd)
	if err != nil {
		return nil, errors.New("无法关闭终端回显")
	}
	defer term.Restore(fd, state)
	terminal := term.NewTerminal(struct {
		io.Reader
		io.Writer
	}{input, output}, "")
	type result struct {
		password []byte
		err      error
	}
	done := make(chan result)
	go func() {
		line, err := terminal.ReadPassword("Mac 密码（输入不显示）：")
		password := []byte(line)
		select {
		case done <- result{password, err}:
		case <-ctx.Done():
			clear(password)
		}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case value := <-done:
		if errors.Is(value.err, io.EOF) { // Raw-mode Ctrl+C/Ctrl+D.
			clear(value.password)
			return nil, context.Canceled
		}
		if value.err != nil {
			clear(value.password)
			return nil, errors.New("终端密码输入失败")
		}
		return value.password, nil
	}
}
