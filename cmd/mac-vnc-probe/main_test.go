package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestPasswordLineLimitsAndCRLF(t *testing.T) {
	for _, tc := range []struct {
		input, want string
		valid       bool
	}{
		{"secret\n", "secret", true},
		{"secret\r\n", "secret", true},
		{"secret", "secret", true},
		{strings.Repeat("a", 63) + "\r\n", strings.Repeat("a", 63), true},
		{strings.Repeat("中", 21) + "\n", strings.Repeat("中", 21), true},
		{"", "", false}, {"\n", "", false}, {"bad\x00value\n", "", false},
		{strings.Repeat("a", 64) + "\n", "", false},
		{strings.Repeat("a", 10000), "", false},
		{"secret\r", "", false},
	} {
		got, err := readPasswordLine(strings.NewReader(tc.input))
		if (err == nil) != tc.valid || string(got) != tc.want {
			t.Fatalf("length=%d: got %q, %v", len(tc.input), got, err)
		}
	}
}

func TestInvalidCLIAndHelpNeverReadPassword(t *testing.T) {
	for _, tc := range []struct {
		args []string
		code int
	}{
		{[]string{"--help"}, 0}, {[]string{"--version"}, 0},
		{nil, 2}, {[]string{"--host", "127.0.0.1", "--stage", "frame"}, 2},
		{[]string{"--host", "127.0.0.1", "--stage", "auth"}, 2},
		{[]string{"--host", "127.0.0.1", "--password-stdin"}, 2},
		{[]string{"--host", "127.0.0.1", "--stage", "auth", "--user", strings.Repeat("x", 64)}, 2},
		{[]string{"--host", "127.0.0.1", "--hold", "30s"}, 2},
		{[]string{"--host", "127.0.0.1", "unexpected-positional"}, 2},
		{[]string{"--password", "not-accepted"}, 2},
	} {
		var output, errorOutput bytes.Buffer
		code := run(context.Background(), tc.args, strings.NewReader(""), &output, &errorOutput, func() ([]byte, error) {
			t.Fatal("password requested before validating CLI")
			return nil, nil
		})
		if code != tc.code {
			t.Fatalf("%v: %d, %s", tc.args, code, errorOutput.String())
		}
	}
}

func TestTCPCLINeverPromptsOrSendsRFB(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(time.Second))
		var data [1]byte
		n, err := conn.Read(data[:])
		if n != 0 || err != io.EOF {
			done <- fmt.Errorf("unexpected client data/connection: %d %v", n, err)
			return
		}
		done <- nil
	}()
	host, port, _ := net.SplitHostPort(listener.Addr().String())
	var output, errorOutput bytes.Buffer
	code := run(context.Background(), []string{"--host", host, "--port", port, "--stage", "tcp"}, strings.NewReader(""), &output, &errorOutput, func() ([]byte, error) {
		t.Fatal("TCP stage asked for a password")
		return nil, nil
	})
	if code != 0 || !strings.Contains(output.String(), "已到达 tcp 阶段") || !strings.Contains(output.String(), "不判断亮屏结果") {
		t.Fatalf("%d: %s %s", code, output.String(), errorOutput.String())
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestCredentialInputIsNotLoggedAndTemporaryBytesAreCleared(t *testing.T) {
	for _, stdin := range []bool{false, true} {
		t.Run(strconv.FormatBool(stdin), func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			done := make(chan error, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					done <- err
					return
				}
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(time.Second))
				if _, err := io.WriteString(conn, "RFB 003.008\n"); err != nil {
					done <- err
					return
				}
				var reply [12]byte
				if _, err := io.ReadFull(conn, reply[:]); err != nil {
					done <- err
					return
				}
				_, err = conn.Write([]byte{1, 1}) // No DH; must not select None.
				done <- err
			}()
			host, port, _ := net.SplitHostPort(listener.Addr().String())
			args := []string{"--host", host, "--port", port, "--stage", "auth", "--user", "private-user"}
			if stdin {
				args = append(args, "--password-stdin")
			}
			var output, errorOutput bytes.Buffer
			password := []byte("private-password")
			code := run(context.Background(), args, strings.NewReader("private-password\r\n"), &output, &errorOutput, func() ([]byte, error) {
				if stdin {
					t.Fatal("stdin mode asked for hidden terminal input")
				}
				return password, nil
			})
			if code != 1 {
				t.Fatalf("%d: %s", code, errorOutput.String())
			}
			for _, secret := range []string{"private-user", "private-password"} {
				if strings.Contains(output.String()+errorOutput.String(), secret) {
					t.Fatal("private data was logged")
				}
			}
			if !stdin && !bytes.Equal(password, make([]byte, len(password))) {
				t.Fatal("temporary password bytes were not cleared")
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}
