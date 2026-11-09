package vncprobe

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/md5"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

// RFC 2409 group 2; a fixed public fixture, never a production secret.
const primeHex = "FFFFFFFFFFFFFFFFC90FDAA22168C234C4C6628B80DC1CD1" +
	"29024E088A67CC74020BBEA63B139B22514A08798E3404DD" +
	"EF9519B3CD3A431B302B0A6DF25F14374FE1356D6D51C245" +
	"E485B576625E7EC6F44C42E9A637ED6B0BFF5CB6F406B7ED" +
	"EE386BFB5A899FA5AE9F24117C4B1FE649286651ECE65381FFFFFFFFFFFFFFFF"

func testConfig() Config {
	return Config{Host: "127.0.0.1", Port: 5900, Stage: Auth, Username: "test-user", Password: []byte("test-password-123"), Timeout: 2 * time.Second}
}

func fakeServer(t *testing.T, handle func(net.Conn) error) (string, int, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	done := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(4 * time.Second))
		done <- handle(conn)
	}()
	host, portString, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := strconv.Atoi(portString)
	return host, port, func() {
		t.Helper()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("fake server did not finish")
		}
	}
}

func expectEOF(conn net.Conn) error {
	var next [1]byte
	n, err := conn.Read(next[:])
	if n != 0 || err == nil {
		return fmt.Errorf("unexpected extra client message: %x", next[:n])
	}
	if errors.Is(err, io.EOF) {
		return nil
	}
	return fmt.Errorf("expected disconnect: %w", err)
}

func offerDH(conn net.Conn, minor int) error {
	// Fragmented writes exercise stream framing, including Apple's 3.889 banner.
	for _, part := range []string{"RF", fmt.Sprintf("B 003.%03d", minor), "\n"} {
		if err := writeFull(conn, []byte(part)); err != nil {
			return err
		}
	}
	var reply [12]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil {
		return err
	}
	want := "RFB 003.008\n"
	if minor == 7 {
		want = "RFB 003.007\n"
	}
	if string(reply[:]) != want {
		return fmt.Errorf("version reply: %q", reply[:])
	}
	if err := writeFull(conn, []byte{3, 1, 2, 30}); err != nil {
		return err
	}
	var selected [1]byte
	if _, err := io.ReadFull(conn, selected[:]); err != nil {
		return err
	}
	if selected[0] != 30 {
		return errors.New("client did not choose DH(30)")
	}
	return nil
}

func authenticateFake(conn net.Conn, c Config, minor int, result uint32) error {
	if err := offerDH(conn, minor); err != nil {
		return err
	}
	p, _ := new(big.Int).SetString(primeHex, 16)
	serverPrivate := big.NewInt(123456789)
	serverPublic := new(big.Int).Exp(big.NewInt(2), serverPrivate, p)
	params := make([]byte, 4+128*2)
	binary.BigEndian.PutUint16(params[:2], 2)
	binary.BigEndian.PutUint16(params[2:4], 128)
	p.FillBytes(params[4:132])
	serverPublic.FillBytes(params[132:])
	if err := writeFull(conn, params); err != nil {
		return err
	}
	var response [256]byte
	if _, err := io.ReadFull(conn, response[:]); err != nil {
		return err
	}
	clientPublic := new(big.Int).SetBytes(response[128:])
	if clientPublic.Cmp(big.NewInt(2)) < 0 || clientPublic.Cmp(p) >= 0 {
		return errors.New("invalid client public value")
	}
	secret := make([]byte, 128)
	new(big.Int).Exp(clientPublic, serverPrivate, p).FillBytes(secret)
	key := md5.Sum(secret)
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return err
	}
	var plaintext [128]byte
	for i := 0; i < 128; i += 16 {
		block.Decrypt(plaintext[i:i+16], response[i:i+16])
	}
	for i, want := range [][]byte{[]byte(c.Username), c.Password} {
		field := plaintext[i*64 : (i+1)*64]
		end := bytes.IndexByte(field, 0)
		if end < 0 || !bytes.Equal(field[:end], want) {
			return errors.New("DH credential wire format or AES key derivation is wrong")
		}
	}
	var status [4]byte
	binary.BigEndian.PutUint32(status[:], result)
	return writeFull(conn, status[:])
}

func serverInit() []byte {
	data := make([]byte, 24)
	binary.BigEndian.PutUint16(data[:2], 1920)
	binary.BigEndian.PutUint16(data[2:4], 1080)
	copy(data[4:20], []byte{32, 24, 0, 1, 0, 255, 0, 255, 0, 255, 16, 8, 0, 0, 0, 0})
	name := []byte("private desktop \x1b[31m")
	binary.BigEndian.PutUint32(data[20:24], uint32(len(name)))
	return append(data, name...)
}

func TestStagesStopAtExactMessageBoundary(t *testing.T) {
	for _, stage := range []Stage{TCP, Auth, Init} {
		for _, minor := range []int{7, 889} {
			t.Run(fmt.Sprintf("%s/3.%d", stage, minor), func(t *testing.T) {
				c := testConfig()
				c.Stage = stage
				expected := c
				host, port, finish := fakeServer(t, func(conn net.Conn) error {
					if stage == TCP {
						return expectEOF(conn) // No RFB client bytes at all.
					}
					if err := authenticateFake(conn, expected, minor, 0); err != nil {
						return err
					}
					if stage == Init {
						var shared [1]byte
						if _, err := io.ReadFull(conn, shared[:]); err != nil {
							return err
						}
						if shared[0] != 1 {
							return errors.New("ClientInit must request a shared session")
						}
						if err := writeFull(conn, serverInit()); err != nil {
							return err
						}
					}
					// Auth must not send ClientInit; Init must not request frames,
					// change encodings, or send keyboard/pointer messages.
					return expectEOF(conn)
				})
				c.Host, c.Port = host, port
				var logs []string
				if err := Run(context.Background(), c, func(s string) { logs = append(logs, s) }); err != nil {
					t.Fatal(err)
				}
				finish()
				log := strings.Join(logs, "\n")
				for _, private := range []string{c.Username, string(c.Password), "private desktop", "\x1b"} {
					if strings.Contains(log, private) {
						t.Fatal("private data present in logs")
					}
				}
				if !strings.Contains(log, "已到达 "+string(stage)+" 阶段") {
					t.Fatal("missing reached-stage event")
				}
			})
		}
	}
}

func TestAuthFailuresDoNotInitialiseOrRetry(t *testing.T) {
	for code, want := range map[uint32]error{1: ErrAuthRejected, 2: ErrAuthLimited} {
		t.Run(strconv.Itoa(int(code)), func(t *testing.T) {
			c := testConfig()
			c.Stage = Init
			expected := c
			host, port, finish := fakeServer(t, func(conn net.Conn) error {
				if err := authenticateFake(conn, expected, 8, code); err != nil {
					return err
				}
				return expectEOF(conn)
			})
			c.Host, c.Port = host, port
			if err := Run(context.Background(), c, nil); !errors.Is(err, want) {
				t.Fatalf("got %v, want %v", err, want)
			}
			finish()
		})
	}
}

func TestMalformedOrUnsupportedVersionAndSecurity(t *testing.T) {
	for _, banner := range []string{"RFB 003.003\n", "RFB 004.008\n", "RFB 003.00x\n", "RFB 003.008!"} {
		t.Run(banner, func(t *testing.T) {
			host, port, finish := fakeServer(t, func(conn net.Conn) error {
				if err := writeFull(conn, []byte(banner)); err != nil {
					return err
				}
				return expectEOF(conn)
			})
			c := testConfig()
			c.Host, c.Port = host, port
			if err := Run(context.Background(), c, nil); err == nil {
				t.Fatal("bad version accepted")
			}
			finish()
		})
	}
	for _, types := range [][]byte{{0}, {2, 1, 2}} {
		host, port, finish := fakeServer(t, func(conn net.Conn) error {
			if err := writeFull(conn, []byte("RFB 003.008\n")); err != nil {
				return err
			}
			var version [12]byte
			if _, err := io.ReadFull(conn, version[:]); err != nil {
				return err
			}
			if err := writeFull(conn, types); err != nil {
				return err
			}
			return expectEOF(conn)
		})
		c := testConfig()
		c.Host, c.Port = host, port
		if err := Run(context.Background(), c, nil); err == nil {
			t.Fatal("unsupported authentication accepted")
		}
		finish()
	}
}

func TestDHBoundsAndParameterValidation(t *testing.T) {
	for _, length := range []uint16{0, 127, 513, 65535} {
		t.Run(fmt.Sprintf("length/%d", length), func(t *testing.T) {
			host, port, finish := fakeServer(t, func(conn net.Conn) error {
				if err := offerDH(conn, 8); err != nil {
					return err
				}
				var header [4]byte
				binary.BigEndian.PutUint16(header[:2], 2)
				binary.BigEndian.PutUint16(header[2:], length)
				if err := writeFull(conn, header[:]); err != nil {
					return err
				}
				return expectEOF(conn)
			})
			c := testConfig()
			c.Host, c.Port = host, port
			if err := Run(context.Background(), c, nil); err == nil {
				t.Fatal("unbounded DH length accepted")
			}
			finish()
		})
	}
	for _, kind := range []string{"generator", "public-zero", "public-one", "public-upper", "even-prime", "composite"} {
		t.Run(kind, func(t *testing.T) {
			host, port, finish := fakeServer(t, func(conn net.Conn) error {
				if err := offerDH(conn, 8); err != nil {
					return err
				}
				p, _ := new(big.Int).SetString(primeHex, 16)
				pub, generator, length := big.NewInt(4), uint16(2), 128
				switch kind {
				case "generator":
					generator = 1
				case "public-zero":
					pub.SetInt64(0)
				case "public-one":
					pub.SetInt64(1)
				case "public-upper":
					pub.Sub(p, big.NewInt(1))
				case "even-prime":
					p.Sub(p, big.NewInt(1))
				case "composite":
					p.Mul(p, big.NewInt(3))
					length = 129
				}
				data := make([]byte, 4+length*2)
				binary.BigEndian.PutUint16(data[:2], generator)
				binary.BigEndian.PutUint16(data[2:4], uint16(length))
				p.FillBytes(data[4 : 4+length])
				pub.FillBytes(data[4+length:])
				if err := writeFull(conn, data); err != nil {
					return err
				}
				return expectEOF(conn)
			})
			c := testConfig()
			c.Host, c.Port = host, port
			if err := Run(context.Background(), c, nil); err == nil {
				t.Fatal("invalid DH parameters accepted")
			}
			finish()
		})
	}
}

func TestServerInitLengthIsBounded(t *testing.T) {
	c := testConfig()
	c.Stage = Init
	expected := c
	host, port, finish := fakeServer(t, func(conn net.Conn) error {
		if err := authenticateFake(conn, expected, 8, 0); err != nil {
			return err
		}
		var client [1]byte
		if _, err := io.ReadFull(conn, client[:]); err != nil {
			return err
		}
		data := serverInit()[:24]
		binary.BigEndian.PutUint32(data[20:24], 0xffffffff)
		if err := writeFull(conn, data); err != nil {
			return err
		}
		return expectEOF(conn)
	})
	c.Host, c.Port = host, port
	if err := Run(context.Background(), c, nil); err == nil || !strings.Contains(err.Error(), "ServerInit") {
		t.Fatalf("bad name length accepted: %v", err)
	}
	finish()
}

func TestDeadlineAndCancellationCloseStalledConnection(t *testing.T) {
	for _, cancelParent := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelParent), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			host, port, finish := fakeServer(t, func(conn net.Conn) error {
				if cancelParent {
					cancel()
				}
				return expectEOF(conn) // Never send an RFB banner.
			})
			c := testConfig()
			c.Host, c.Port, c.Timeout = host, port, 100*time.Millisecond
			want := context.DeadlineExceeded
			if cancelParent {
				want = context.Canceled
			}
			if err := Run(ctx, c, nil); !errors.Is(err, want) {
				t.Fatalf("got %v, want %v", err, want)
			}
			finish()
		})
	}
}

func TestHoldAndEarlyDisconnect(t *testing.T) {
	for _, disconnect := range []bool{false, true} {
		c := testConfig()
		c.Stage, c.Hold = TCP, 50*time.Millisecond
		host, port, finish := fakeServer(t, func(conn net.Conn) error {
			if disconnect {
				return nil
			}
			return expectEOF(conn)
		})
		c.Host, c.Port = host, port
		start := time.Now()
		err := Run(context.Background(), c, nil)
		if disconnect && err == nil || !disconnect && err != nil {
			t.Fatalf("disconnect=%t: %v", disconnect, err)
		}
		if !disconnect && time.Since(start) < c.Hold {
			t.Fatal("hold returned before its interval")
		}
		finish()
	}
}

func TestConfigAndCredentialsRejectedBeforeNetwork(t *testing.T) {
	changes := []func(*Config){
		func(c *Config) { c.Stage = "frame" },
		func(c *Config) { c.Host = "host:5900" },
		func(c *Config) { c.Host = "bad\nname" },
		func(c *Config) { c.Host = "-bad.test" },
		func(c *Config) { c.Host = "" },
		func(c *Config) { c.Port = 0 },
		func(c *Config) { c.Port = 65536 },
		func(c *Config) { c.Timeout = 0 },
		func(c *Config) { c.Timeout = 3 * time.Minute },
		func(c *Config) { c.Hold = c.Timeout },
		func(c *Config) { c.Hold = -time.Second },
		func(c *Config) { c.Username = "" },
		func(c *Config) { c.Username = strings.Repeat("a", 64) },
		func(c *Config) { c.Username = "name\x00other" },
		func(c *Config) { c.Password = nil },
		func(c *Config) { c.Password = bytes.Repeat([]byte{1}, 64) },
		func(c *Config) { c.Password = []byte("pwd\x00other") },
		func(c *Config) { c.Password = []byte{0xff} },
	}
	for i, change := range changes {
		c := testConfig()
		change(&c)
		called := false
		err := Run(context.Background(), c, func(string) { called = true })
		if err == nil || called {
			t.Fatalf("case %d was not rejected before network: %v", i, err)
		}
	}
	for _, host := range []string{"192.168.1.10", "::1", "fe80::1%eth0", "mac-mini.local", "Mac-Mini.local."} {
		c := testConfig()
		c.Host = host
		if err := c.Validate(); err != nil {
			t.Fatalf("valid host %q: %v", host, err)
		}
	}
	c := testConfig()
	c.Username = strings.Repeat("中", 21) // Exactly 63 UTF-8 bytes.
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePassword([]byte(strings.Repeat("中", 21))); err != nil {
		t.Fatal(err)
	}
}

type shortWriter struct {
	bytes.Buffer
}

func (w *shortWriter) Write(p []byte) (int, error) { return w.Buffer.Write(p[:1]) }

func TestWriteFullHandlesPartialWrites(t *testing.T) {
	var w shortWriter
	if err := writeFull(&w, []byte("abc")); err != nil || w.String() != "abc" {
		t.Fatalf("partial write: %q %v", w.String(), err)
	}
}
