package app

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/lemoeo/Shell-WoL/internal/vncprobe"
	"gopkg.in/yaml.v3"
)

type credentialRecord struct {
	Binding string `yaml:"binding"`
	Data    string `yaml:"data"`
}
type credentialFile struct {
	Version     int                         `yaml:"version"`
	Credentials map[string]credentialRecord `yaml:"credentials"`
}

func credentialBinding(d Device) string {
	// Bind secrets to the account and destination, not the hold time or method.
	b, _ := json.Marshal([]any{d.ID, d.IP, d.VNC.Port, d.VNC.Username})
	h := sha256.Sum256(b)
	return base64.StdEncoding.EncodeToString(h[:])
}

func regularCredentialFile(path string) error {
	i, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !i.Mode().IsRegular() {
		return errors.New("凭据文件必须是普通文件，不能为符号链接")
	}
	return nil
}

func readCredentials(dir string) (credentialFile, error) {
	c := credentialFile{Version: 1, Credentials: map[string]credentialRecord{}}
	path := filepath.Join(dir, "credentials.yaml")
	if err := regularCredentialFile(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return c, nil
		}
		return c, err
	}
	if _, err := readYAML(path, &c); err != nil {
		return c, errors.New("credentials.yaml 无效，请恢复凭据文件")
	}
	if c.Version != 1 || len(c.Credentials) > 1000 {
		return c, errors.New("凭据文件版本或数量无效")
	}
	if c.Credentials == nil {
		c.Credentials = map[string]credentialRecord{}
	}
	return c, nil
}

func credentialKey(dir string, create bool) ([]byte, error) {
	path := filepath.Join(dir, "credentials.key")
	err := regularCredentialFile(path)
	if errors.Is(err, os.ErrNotExist) && create {
		key := make([]byte, 32)
		if _, err = rand.Read(key); err != nil {
			return nil, err
		}
		f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			clear(key)
			return nil, e
		}
		if e = protectPrivateFile(path); e == nil {
			_, e = f.Write(key)
		}
		if e == nil {
			e = f.Sync()
		}
		closeErr := f.Close()
		if e == nil {
			e = closeErr
		}
		if e != nil {
			os.Remove(path)
			clear(key)
			return nil, e
		}
		return key, nil
	}
	if err != nil {
		return nil, errors.New("凭据密钥缺失或不可用；请恢复 credentials.key，不能自动覆盖已有凭据")
	}
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	key, err := io.ReadAll(io.LimitReader(f, 33))
	if err != nil || len(key) != 32 {
		clear(key)
		return nil, errors.New("凭据密钥损坏；请恢复 credentials.key")
	}
	return key, nil
}

func credentialAEAD(key []byte) (cipher.AEAD, error) {
	b, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(b)
}

func saveCredentials(dir string, c credentialFile) error {
	if len(c.Credentials) > 1000 {
		return errors.New("凭据数量超过上限，请清理已删除设备的旧凭据")
	}
	path := filepath.Join(dir, "credentials.yaml")
	if err := regularCredentialFile(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	b, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	// ACLs/mode are applied to the temporary file before secret bytes are written.
	f, err := os.CreateTemp(dir, ".credentials-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = protectPrivateFile(tmp); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// readDevicePassword never returns a password through the public device DTO.
func readDevicePassword(dir string, d Device) ([]byte, error) {
	c, err := readCredentials(dir)
	if err != nil {
		return nil, err
	}
	r, ok := c.Credentials[d.ID]
	if !ok {
		return nil, errors.New("请先设置 Mac VNC 密码")
	}
	if r.Binding != credentialBinding(d) {
		return nil, errors.New("目标 IP、端口或用户名已改变，请重新设置 Mac VNC 密码")
	}
	key, err := credentialKey(dir, false)
	if err != nil {
		return nil, err
	}
	defer clear(key)
	return decryptCredential(r, key)
}

func decryptCredential(r credentialRecord, key []byte) ([]byte, error) {
	if len(r.Data) > 344 {
		return nil, errors.New("凭据密文无效")
	}
	a, err := credentialAEAD(key)
	if err != nil {
		return nil, err
	}
	data, err := base64.StdEncoding.DecodeString(r.Data)
	if err != nil || len(data) < a.NonceSize()+a.Overhead() || len(data) > 256 {
		return nil, errors.New("凭据密文无效")
	}
	p, err := a.Open(nil, data[:a.NonceSize()], data[a.NonceSize():], []byte(r.Binding))
	if err != nil {
		return nil, errors.New("凭据解密失败；请恢复匹配的密钥或重新配置密码")
	}
	if err = vncprobe.ValidatePassword(p); err != nil {
		clear(p)
		return nil, err
	}
	return p, nil
}

func setCredential(dir string, c *credentialFile, d Device, password []byte) error {
	if err := vncprobe.ValidatePassword(password); err != nil {
		return err
	}
	key, err := credentialKey(dir, len(c.Credentials) == 0)
	if err != nil {
		return err
	}
	defer clear(key)
	a, err := credentialAEAD(key)
	if err != nil {
		return err
	}
	nonce := make([]byte, a.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return err
	}
	binding := credentialBinding(d)
	data := a.Seal(nonce, nonce, password, []byte(binding))
	c.Credentials[d.ID] = credentialRecord{binding, base64.StdEncoding.EncodeToString(data)}
	return nil
}
