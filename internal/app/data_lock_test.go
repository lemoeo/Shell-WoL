package app

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestDataLockChild(t *testing.T) {
	dir := os.Getenv("SHELL_WOL_LOCK_TEST_DIR")
	if dir == "" {
		return
	}
	if os.Getenv("SHELL_WOL_LOCK_TEST_EXEC") == "1" {
		lock, err := AcquireDataLock(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Close()
		if err = lock.Run([]string{"sh", "-c", "printf 'READY\\n'; IFS= read -r value || :"}); err != nil {
			t.Fatal(err)
		}
		return
	}
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.PublishSetupToken(randomToken()); err != nil {
		t.Fatal(err)
	}
	fmt.Println("READY")
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		if scanner.Text() == "password" {
			if err = s.SetPassword("test-lock-password-123"); err != nil {
				t.Fatal(err)
			}
			fmt.Println("PASSWORD")
		}
	}
}

func lockChild(t *testing.T, dir string) (*exec.Cmd, func(string)) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestDataLockChild$")
	cmd.Env = append(os.Environ(), "SHELL_WOL_LOCK_TEST_DIR="+dir)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stdin.Close()
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	})
	lines := make(chan string, 8)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		close(lines)
	}()
	wait := func(expected string) {
		t.Helper()
		select {
		case got := <-lines:
			if got != expected {
				t.Fatalf("child output %q, expected %q", got, expected)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("child readiness timed out")
		}
	}
	wait("READY")
	return cmd, func(command string) { fmt.Fprintln(stdin, command); wait("PASSWORD") }
}

func TestKernelLockSurvivesSecondWriterAndRecoversAfterKill(t *testing.T) {
	dir := t.TempDir()
	cmd, _ := lockChild(t, dir)
	before, _ := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if _, err := OpenStore(dir); !errors.Is(err, ErrDataLocked) {
		t.Fatalf("second writer: %v", err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if !bytes.Equal(before, after) {
		t.Fatal("rejected writer changed data")
	}
	cmd.Process.Kill()
	cmd.Wait()
	if token, err := ReadSetupToken(dir); err == nil || token != "" {
		t.Fatal("stale token exposed after kill")
	}
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal("kernel lock was not released:", err)
	}
	if _, err = os.Stat(filepath.Join(dir, ".setup-token")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("new writer did not remove stale token")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal("Close must be idempotent:", err)
	}
	if info, err := os.Stat(filepath.Join(dir, ".lock")); err != nil || !info.Mode().IsRegular() {
		t.Fatal("stable lock file should remain")
	}
}

func TestMaintenanceLockInheritedAcrossExec(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX installer maintenance")
	}
	t.Setenv("SHELL_WOL_LOCK_TEST_EXEC", "1")
	dir := t.TempDir()
	cmd, _ := lockChild(t, dir)
	if _, err := OpenStore(dir); !errors.Is(err, ErrDataLocked) {
		t.Fatal("exec lost the kernel lock:", err)
	}
	cmd.Process.Kill()
	cmd.Wait()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal("maintenance process did not release lock:", err)
	}
	s.Close()
}

func TestSetupTokenBoundToLiveInstanceAndRemovedOnPassword(t *testing.T) {
	dir := t.TempDir()
	cmd, password := lockChild(t, dir)
	token, err := ReadSetupToken(dir)
	if err != nil || len(token) != 64 {
		t.Fatalf("token read failed: %v", err)
	}
	path := filepath.Join(dir, ".setup-token")
	oldFile, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatal("token mode is not private")
	}
	password("password")
	if _, err = ReadSetupToken(dir); !errors.Is(err, ErrAdminInitialized) {
		t.Fatal(err)
	}
	if _, err = os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("token survived password setup")
	}
	cmd.Process.Kill()
	cmd.Wait()

	// Reset only this test's admin hash to start another uninitialized service.
	config := filepath.Join(dir, "config.yaml")
	data, _ := os.ReadFile(config)
	lines := strings.Split(string(data), "\n")
	for i := range lines {
		if strings.HasPrefix(lines[i], "password_hash:") {
			lines[i] = "password_hash: \"\""
		}
	}
	if err = os.WriteFile(config, []byte(strings.Join(lines, "\n")), 0600); err != nil {
		t.Fatal(err)
	}
	next, _ := lockChild(t, dir)
	fresh, err := ReadSetupToken(dir)
	if err != nil || fresh == token {
		t.Fatal("restart did not rotate token:", err)
	}
	if err = os.WriteFile(path, oldFile, 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadSetupToken(dir); got != "" || err == nil {
		t.Fatal("old instance token accepted")
	}
	next.Process.Kill()
	next.Wait()
}

func TestLockRefusesLegacyDirectoryAndLinks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".lock")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(dir); err == nil || !strings.Contains(err.Error(), "旧版目录锁") {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		t.Fatal("legacy lock was deleted")
	}
	os.Remove(path)
	target := filepath.Join(t.TempDir(), "outside")
	os.WriteFile(target, []byte("unchanged"), 0600)
	if err := os.Symlink(target, path); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	if _, err := OpenStore(dir); err == nil {
		t.Fatal("lock symlink accepted")
	}
	b, _ := os.ReadFile(target)
	if string(b) != "unchanged" {
		t.Fatal("outside file changed")
	}
}

func TestUnlinkedLockDescriptorIsRejected(t *testing.T) {
	dir := t.TempDir()
	f, err := openLockFile(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not allow unlinking this open handle")
	}
	path := filepath.Join(dir, ".lock")
	os.Remove(path)
	os.WriteFile(path, []byte("replacement"), 0600)
	if err = sameLockFile(f, path); err == nil {
		t.Fatal("detached lock descriptor accepted")
	}
}

func TestTokenFileContainsNoPublicConfiguration(t *testing.T) {
	s := newStore(t)
	token := randomToken()
	if err := s.PublishSetupToken(token); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(s.dir, ".setup-token"))
	if err != nil {
		t.Fatal(err)
	}
	var record setupTokenFile
	if err = json.Unmarshal(b, &record); err != nil || record.Token != token {
		t.Fatal("invalid private token file")
	}
	for _, name := range []string{"config.yaml", "devices.yaml"} {
		b, err = os.ReadFile(filepath.Join(s.dir, name))
		if err != nil || bytes.Contains(b, []byte(token)) {
			t.Fatal("token leaked into", name)
		}
	}
}

func TestClosedStoreCannotWriteAfterLockReleased(t *testing.T) {
	dir := t.TempDir()
	first, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	first.Close()
	second, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	for _, write := range []func() error{
		func() error { return first.SaveConfig(first.Config()) },
		func() error { return first.SaveDevice(testDevice("pc"), true) },
		func() error { return first.DeleteDevice("pc") },
		func() error { return first.Record(Event{Result: "sent"}) },
		func() error { return first.SetPassword("closed-store-password") },
		func() error { return first.PublishSetupToken(randomToken()) },
	} {
		if err := write(); !errors.Is(err, ErrStoreClosed) {
			t.Fatal("closed store accepted mutation:", err)
		}
	}
}
