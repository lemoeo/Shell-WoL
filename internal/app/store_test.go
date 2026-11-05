package app

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func testDevice(id string) Device {
	return Device{ID: id, Name: "书房电脑", MAC: "aa:bb:cc:dd:ee:ff", Broadcast: "192.168.1.255", Port: 9, Repeat: 1}
}
func newStore(t *testing.T) *Store {
	t.Helper()
	s, e := OpenStore(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func TestYAMLRoundTripAndLock(t *testing.T) {
	dir := t.TempDir()
	s, e := OpenStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = OpenStore(dir); e == nil {
		t.Fatal("second writer accepted")
	}
	if e = s.SaveDevice(testDevice("pc"), true); e != nil {
		t.Fatal(e)
	}
	s.Close()
	p := filepath.Join(dir, "devices.yaml")
	b, e := os.ReadFile(p)
	if e != nil {
		t.Fatal(e)
	}
	b = []byte(strings.Replace(string(b), "name: 书房电脑", "name: 书房电脑 # keep me", 1))
	if e = os.WriteFile(p, b, 0600); e != nil {
		t.Fatal(e)
	}
	s, e = OpenStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	d, _ := s.Get("pc")
	d.Note = "hello"
	d.IP = "192.168.1.10"
	if e = s.SaveDevice(d, false); e != nil {
		t.Fatal(e)
	}
	d.IP = ""
	d.Note = ""
	if e = s.SaveDevice(d, false); e != nil {
		t.Fatal(e)
	}
	b, _ = os.ReadFile(p)
	if !strings.Contains(string(b), "# keep me") {
		t.Fatal("comment lost")
	}
	var saved deviceFile
	if _, e = readYAML(p, &saved); e != nil {
		t.Fatal(e)
	}
	if saved.Devices[0].IP != "" || saved.Devices[0].Note != "" {
		t.Fatal("cleared fields retained")
	}
	if e = s.DeleteDevice("pc"); e != nil {
		t.Fatal(e)
	}
	if len(s.Devices()) != 0 {
		t.Fatal("delete failed")
	}
}
func TestBadYAMLReleasesLock(t *testing.T) {
	dir := t.TempDir()
	if e := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("listen: [bad"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := OpenStore(dir); e == nil {
		t.Fatal("accepted invalid YAML")
	}
	if _, e := os.Stat(filepath.Join(dir, ".lock")); !os.IsNotExist(e) {
		t.Fatal("failed open left lock")
	}
	for _, b := range []string{"a: 1\na: 2", "x: &x [*x]", "a: 1\n---\nb: 2"} {
		if _, e := parseYAML([]byte(b)); e == nil {
			t.Fatalf("accepted %s", b)
		}
	}
}
func TestConcurrentStoreAndHistoryLimit(t *testing.T) {
	s := newStore(t)
	c := s.Config()
	c.LogLimit = 2
	if e := s.SaveConfig(c); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Devices()
			s.Config()
			if e := s.Record(Event{Result: "sent"}); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	if len(s.Events()) != 2 {
		t.Fatal("history unbounded")
	}
	c.LogLimit = 0
	if e := s.SaveConfig(c); e != nil {
		t.Fatal(e)
	}
	if len(s.Events()) != 0 {
		t.Fatal("zero log limit ignored")
	}
}
func TestSaveFailureDoesNotChangeMemory(t *testing.T) {
	s := newStore(t)
	s.dir = filepath.Join(s.dir, "missing")
	if e := s.SaveDevice(testDevice("pc"), true); e == nil {
		t.Fatal("write should fail")
	}
	if len(s.Devices()) != 0 {
		t.Fatal("failed save mutated memory")
	}
}

func TestCLIReadsWhileWriterRunning(t *testing.T) {
	s := newStore(t)
	if e := s.SaveDevice(testDevice("pc"), true); e != nil {
		t.Fatal(e)
	}
	_, d, e := ReadWakeDefaults(s.dir, "pc")
	if e != nil || d.ID != "pc" {
		t.Fatalf("read snapshot failed: %v", e)
	}
}
