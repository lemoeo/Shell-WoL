package app

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckDataReadOnly(t *testing.T) {
	store := newStore(t)
	before, err := os.ReadFile(filepath.Join(store.dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err = CheckData(store.dir, false, false); err != nil {
		t.Fatal(err)
	}
	if err = CheckData(store.dir, true, false); err == nil {
		t.Fatal("uninitialized admin passed")
	}
	after, _ := os.ReadFile(filepath.Join(store.dir, "config.yaml"))
	if string(before) != string(after) {
		t.Fatal("check mutated YAML")
	}
	if _, err = os.Stat(filepath.Join(store.dir, ".lock")); err != nil {
		t.Fatal("writer lock disturbed")
	}
	if err = store.SetPassword("check-test-password"); err != nil {
		t.Fatal(err)
	}
	if err = CheckData(store.dir, true, false); err != nil {
		t.Fatal(err)
	}
}
func TestCheckRunning(t *testing.T) {
	store := newStore(t)
	if err := store.SetPassword("check-test-password"); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewServer(store).Handler())
	defer server.Close()
	config := store.Config()
	config.Listen = strings.TrimPrefix(server.URL, "http://")
	if err := store.SaveConfig(config); err != nil {
		t.Fatal(err)
	}
	if err := CheckData(store.dir, true, true); err != nil {
		t.Fatal(err)
	}
	wrong := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"ok":true}`)) }))
	defer wrong.Close()
	config.Listen = strings.TrimPrefix(wrong.URL, "http://")
	if err := store.SaveConfig(config); err != nil {
		t.Fatal(err)
	}
	if err := CheckData(store.dir, true, true); err == nil {
		t.Fatal("unrelated HTTP service accepted")
	}
}
func TestCheckMissingDataDoesNotCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "missing")
	if err := CheckData(dir, false, false); err == nil {
		t.Fatal("missing YAML accepted")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("check created directory")
	}
}
