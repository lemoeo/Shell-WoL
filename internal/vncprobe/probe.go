// Package vncprobe implements a bounded, one-shot RFB probe for Mac screen sharing.
// It supports Apple DH authentication (security type 30), not a desktop viewer.
package vncprobe

import (
	"context"
	"crypto/aes"
	"crypto/md5" // Required by the Apple DH wire protocol, not a password hash.
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type Stage string

const (
	TCP  Stage = "tcp"
	Auth Stage = "auth"
	Init Stage = "init"
)

var (
	ErrAuthRejected = errors.New("Mac 拒绝了凭据；请检查用户名、密码和屏幕共享访问权限")
	ErrAuthLimited  = errors.New("Mac 拒绝认证：失败尝试过多；请停止测试并检查账户状态")
)

type Config struct {
	Host     string
	Port     int
	Stage    Stage
	Username string
	Password []byte
	Hold     time.Duration
	Timeout  time.Duration
}

// Validate checks connection settings before the CLI collects a password.
func (c Config) Validate() error {
	if c.Stage != TCP && c.Stage != Auth && c.Stage != Init {
		return errors.New("stage 须为 tcp、auth 或 init")
	}
	if c.Host == "" || len(c.Host) > 253 {
		return errors.New("请提供 --host（IP 或主机名，不含端口）")
	}
	if _, err := netip.ParseAddr(c.Host); err != nil {
		for _, label := range strings.Split(strings.TrimSuffix(c.Host, "."), ".") {
			if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
				return errors.New("host 须为有效的 IP 或 ASCII 主机名，不含端口")
			}
			for _, ch := range label {
				if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-') {
					return errors.New("host 须为有效的 IP 或 ASCII 主机名，不含端口")
				}
			}
		}
	}
	if c.Port < 1 || c.Port > 65535 {
		return errors.New("port 须为 1–65535")
	}
	if c.Timeout <= 0 || c.Timeout > 2*time.Minute || c.Hold < 0 || c.Hold >= c.Timeout {
		return errors.New("timeout 须大于 0 且不超过 2 分钟；hold 须大于等于 0 且小于 timeout")
	}
	if c.Stage != TCP && (c.Username == "" || len(c.Username) > 63 || !utf8.ValidString(c.Username) || strings.ContainsAny(c.Username, "\x00\r\n")) {
		return errors.New("auth/init 需要 --user；DH 用户名须为 1–63 字节的 UTF-8 文本，不能含 NUL 或换行")
	}
	return nil
}

func ValidatePassword(password []byte) error {
	if len(password) < 1 || len(password) > 63 || !utf8.Valid(password) || strings.ContainsAny(string(password), "\x00\r\n") {
		return errors.New("DH 密码须为 1–63 字节的 UTF-8 文本，不能含 NUL 或换行；工具不会截断密码")
	}
	return nil
}

// Run makes exactly one connection and never retries credentials or sends WoL.
// Reaching a stage is not proof that the physical display has turned on.
func Run(parent context.Context, c Config, report func(string)) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if c.Stage != TCP {
		if err := ValidatePassword(c.Password); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(parent, c.Timeout)
	defer cancel()
	if report == nil {
		report = func(string) {}
	}
	report("开始 TCP 连接")
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(c.Host, strconv.Itoa(c.Port)))
	if err != nil {
		return fmt.Errorf("TCP 连接失败: %w", err)
	}
	defer conn.Close()
	// Cancellation closes blocked I/O immediately; deadlines bound stalled peers.
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	if err = conn.SetDeadline(deadline); err != nil {
		return err
	}
	err = runConn(ctx, conn, c, report)
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		var timeout net.Error
		if errors.As(err, &timeout) && timeout.Timeout() && !time.Now().Before(deadline) {
			return context.DeadlineExceeded
		}
	}
	return err
}

func runConn(ctx context.Context, conn net.Conn, c Config, report func(string)) error {
	report("TCP 已连接")
	if c.Stage != TCP {
		if err := negotiate(conn, report); err != nil {
			return err
		}
		if err := authenticate(ctx, conn, c.Username, c.Password, report); err != nil {
			return err
		}
		if c.Stage == Init {
			if err := initialise(conn, report); err != nil {
				return err
			}
		}
	}
	report("已到达 " + string(c.Stage) + " 阶段")
	if c.Hold > 0 {
		report("开始保持连接：" + c.Hold.String())
		if err := hold(ctx, conn, c.Hold); err != nil {
			return fmt.Errorf("保持连接失败: %w", err)
		}
	}
	report("即将主动断开连接")
	return nil
}

func negotiate(conn net.Conn, report func(string)) error {
	var banner [12]byte
	if _, err := io.ReadFull(conn, banner[:]); err != nil {
		return fmt.Errorf("读取 RFB 版本: %w", err)
	}
	if string(banner[:4]) != "RFB " || banner[7] != '.' || banner[11] != '\n' {
		return errors.New("服务器返回的 RFB 版本格式无效")
	}
	for _, i := range []int{4, 5, 6, 8, 9, 10} {
		if banner[i] < '0' || banner[i] > '9' {
			return errors.New("服务器返回的 RFB 版本格式无效")
		}
	}
	major, _ := strconv.Atoi(string(banner[4:7]))
	minor, _ := strconv.Atoi(string(banner[8:11]))
	if major != 3 || minor < 7 {
		return errors.New("本工具的 DH 认证需要 RFB 3.7 或 3.8")
	}
	selected := 8
	if minor == 7 {
		selected = 7
	}
	if err := writeFull(conn, []byte(fmt.Sprintf("RFB 003.%03d\n", selected))); err != nil {
		return fmt.Errorf("发送 RFB 版本: %w", err)
	}
	report(fmt.Sprintf("服务器 RFB 版本：%d.%d；协商版本：3.%d", major, minor, selected))
	var count [1]byte
	if _, err := io.ReadFull(conn, count[:]); err != nil {
		return fmt.Errorf("读取认证类型数量: %w", err)
	}
	if count[0] == 0 {
		// Do not print untrusted server reason strings, which may contain secrets.
		return errors.New("服务器拒绝连接，未提供认证类型")
	}
	types := make([]byte, int(count[0]))
	if _, err := io.ReadFull(conn, types); err != nil {
		return fmt.Errorf("读取认证类型: %w", err)
	}
	var names []string
	found := false
	for _, kind := range types {
		names = append(names, strconv.Itoa(int(kind)))
		found = found || kind == 30
	}
	report("服务器提供的认证类型：" + strings.Join(names, ", "))
	if !found {
		return errors.New("服务器未提供 DH(30) 认证；工具不会改用无认证或其他认证方式")
	}
	if err := writeFull(conn, []byte{30}); err != nil {
		return fmt.Errorf("选择 DH 认证: %w", err)
	}
	report("已选择 DH(30) 认证")
	return nil
}

func authenticate(ctx context.Context, conn net.Conn, username string, password []byte, report func(string)) error {
	var header [4]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		return fmt.Errorf("读取 DH 参数头: %w", err)
	}
	generator := binary.BigEndian.Uint16(header[:2])
	keyBytes := int(binary.BigEndian.Uint16(header[2:]))
	// Mac compatibility target: 1024–4096 bits. Bound bigint CPU and allocations.
	if keyBytes < 128 || keyBytes > 512 {
		return errors.New("DH 参数长度不受支持（支持 128–512 字节，即 1024–4096 位）")
	}
	data := make([]byte, keyBytes*2)
	if _, err := io.ReadFull(conn, data); err != nil {
		return fmt.Errorf("读取 DH 参数: %w", err)
	}
	p := new(big.Int).SetBytes(data[:keyBytes])
	peer := new(big.Int).SetBytes(data[keyBytes:])
	g := new(big.Int).SetUint64(uint64(generator))
	two := big.NewInt(2)
	upper := new(big.Int).Sub(p, two)
	if p.BitLen() < 1024 || p.Bit(0) == 0 || g.Cmp(two) < 0 || g.Cmp(upper) > 0 || peer.Cmp(two) < 0 || peer.Cmp(upper) > 0 {
		return errors.New("服务器 DH 参数无效")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !p.ProbablyPrime(20) {
		return errors.New("服务器 DH 模数不是有效素数")
	}
	// A fresh 256-bit exponent for every single attempt; never reuse keys.
	var randomExponent [32]byte
	if _, err := rand.Read(randomExponent[:]); err != nil {
		return fmt.Errorf("生成 DH 随机数: %w", err)
	}
	defer clear(randomExponent[:])
	x := new(big.Int).SetBytes(randomExponent[:])
	if x.Sign() == 0 {
		return errors.New("生成 DH 随机数失败")
	}
	defer x.SetInt64(0)
	shared := new(big.Int).Exp(peer, x, p)
	defer shared.SetInt64(0)
	if shared.Cmp(two) < 0 {
		return errors.New("服务器 DH 参数产生了无效共享秘密")
	}
	public := new(big.Int).Exp(g, x, p)
	if err := ctx.Err(); err != nil {
		return err
	}
	secret := make([]byte, keyBytes)
	defer clear(secret)
	shared.FillBytes(secret)
	key := md5.Sum(secret)
	defer clear(key[:])
	cipher, err := aes.NewCipher(key[:])
	if err != nil {
		return err
	}
	// Apple DH uses two NUL-terminated 64-byte fields, random-filled, encrypted
	// as eight AES-128 ECB blocks; no CBC IV or additional padding is sent.
	credentials := make([]byte, 128)
	defer clear(credentials)
	if _, err = rand.Read(credentials); err != nil {
		return fmt.Errorf("生成凭据填充: %w", err)
	}
	copy(credentials[:64], username)
	credentials[len(username)] = 0
	copy(credentials[64:], password)
	credentials[64+len(password)] = 0
	for offset := 0; offset < len(credentials); offset += aes.BlockSize {
		cipher.Encrypt(credentials[offset:offset+aes.BlockSize], credentials[offset:offset+aes.BlockSize])
	}
	response := make([]byte, 128+keyBytes)
	defer clear(response)
	copy(response, credentials)
	public.FillBytes(response[128:])
	report("DH 参数处理完成，准备发送认证数据")
	if err = writeFull(conn, response); err != nil {
		return fmt.Errorf("发送 DH 认证数据: %w", err)
	}
	report("认证数据已发送，等待认证结果")
	var result [4]byte
	if _, err = io.ReadFull(conn, result[:]); err != nil {
		return fmt.Errorf("读取认证结果: %w", err)
	}
	switch binary.BigEndian.Uint32(result[:]) {
	case 0:
		report("认证成功")
		return nil
	case 1:
		return ErrAuthRejected
	case 2:
		return ErrAuthLimited
	default:
		return errors.New("服务器返回了未知认证结果")
	}
}

func initialise(conn net.Conn, report func(string)) error {
	if err := writeFull(conn, []byte{1}); err != nil { // shared=true
		return fmt.Errorf("发送 ClientInit: %w", err)
	}
	report("ClientInit 已发送（请求共享会话）")
	var init [24]byte
	if _, err := io.ReadFull(conn, init[:]); err != nil {
		return fmt.Errorf("读取 ServerInit: %w", err)
	}
	width, height := binary.BigEndian.Uint16(init[:2]), binary.BigEndian.Uint16(init[2:4])
	nameLength := binary.BigEndian.Uint32(init[20:24])
	if width == 0 || height == 0 || nameLength > 64<<10 {
		return errors.New("ServerInit 的桌面尺寸或名称长度无效")
	}
	// Names may contain private data or terminal escapes. Discard, never log.
	if _, err := io.CopyN(io.Discard, conn, int64(nameLength)); err != nil {
		return fmt.Errorf("读取 ServerInit 名称: %w", err)
	}
	report(fmt.Sprintf("ServerInit 已接收（桌面 %d×%d）；未请求画面", width, height))
	return nil
}

func hold(ctx context.Context, conn net.Conn, duration time.Duration) error {
	until := time.Now().Add(duration)
	deadline, _ := ctx.Deadline()
	readUntil := until
	if deadline.Before(readUntil) {
		readUntil = deadline
	}
	if err := conn.SetReadDeadline(readUntil); err != nil {
		return err
	}
	// Detect an early server disconnect; do not let unsolicited data use unbounded
	// memory or keep the probe alive. No additional client messages are sent.
	buffer := make([]byte, 1024)
	total := 0
	for {
		n, err := conn.Read(buffer)
		total += n
		if total > 64<<10 {
			return errors.New("保持期间服务器发送的数据过多")
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			var timeout net.Error
			if errors.As(err, &timeout) && timeout.Timeout() && !time.Now().Before(until) {
				return nil
			}
			if errors.Is(err, io.EOF) {
				return errors.New("保持期间服务器提前关闭了连接")
			}
			return err
		}
		if !time.Now().Before(until) {
			return nil
		}
	}
}

func writeFull(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}
