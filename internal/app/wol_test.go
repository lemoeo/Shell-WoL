package app

import (
	"bytes"
	"context"
	"net"
	"strconv"
	"testing"
	"time"
)

func TestMagicPacket(t *testing.T) {
	p, e := MagicPacket("aa:bb:cc:dd:ee:ff")
	if e != nil {
		t.Fatal(e)
	}
	if len(p) != 102 || !bytes.Equal(p[:6], bytes.Repeat([]byte{255}, 6)) {
		t.Fatalf("bad header: %x", p)
	}
	for i := 6; i < 102; i += 6 {
		if !bytes.Equal(p[i:i+6], []byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}) {
			t.Fatal("bad MAC repetition")
		}
	}
	for _, s := range []string{"nope", "aa:bb:cc:dd:ee:ff:00:11"} {
		if _, e = MagicPacket(s); e == nil {
			t.Fatal("accepted invalid MAC")
		}
	}
}
func TestWakeActualUDP(t *testing.T) {
	c, e := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	d := Device{ID: "pc", Name: "PC", MAC: "01:23:45:67:89:ab", Broadcast: "127.0.0.1", Port: c.LocalAddr().(*net.UDPAddr).Port, Repeat: 3}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if e = Wake(ctx, d); e != nil {
		t.Fatal(e)
	}
	c.SetReadDeadline(time.Now().Add(time.Second))
	want, _ := MagicPacket(d.MAC)
	for i := 0; i < 3; i++ {
		buf := make([]byte, 2048)
		n, _, e := c.ReadFromUDP(buf)
		if e != nil {
			t.Fatal(e)
		}
		if !bytes.Equal(buf[:n], want) {
			t.Fatal("unexpected UDP payload")
		}
	}
	cancel()
	if e = Wake(ctx, d); e == nil {
		t.Fatal("cancellation ignored")
	}
}
func TestProbe(t *testing.T) {
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	host, port, _ := net.SplitHostPort(listener.Addr().String())
	p, _ := strconv.Atoi(port)
	d := Device{IP: host, ProbePort: p}
	ok, e := Probe(context.Background(), d)
	if e != nil || !ok {
		t.Fatalf("open port: %v %v", ok, e)
	}
	listener.Close()
	ok, e = Probe(context.Background(), d)
	if e != nil || ok {
		t.Fatalf("closed port: %v %v", ok, e)
	}
	if _, e = Probe(context.Background(), Device{}); e == nil {
		t.Fatal("missing probe configuration accepted")
	}
}
