// Package config locates and reads dbmeta/config.json, the single file that
// holds both connection settings and tool settings.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"dbmeta-cli/internal/guard"
)

const (
	DirName        = "dbmeta"
	FileName       = "config.json"
	DefaultTTL     = 168 * time.Hour
	DefaultPort    = 3306
	DefaultTLS     = "preferred"
	EnvDir         = "DBMETA_DIR"
	gitignoreName  = ".gitignore"
	configFileMode = 0o600
)

// ErrNotFound means no dbmeta/config.json exists (exit code 2, hint: run init).
var ErrNotFound = errors.New("未找到 dbmeta/config.json,请先运行 dbmeta-cli init")

var tlsModes = map[string]bool{"false": true, "preferred": true, "true": true, "skip-verify": true}

type Instance struct {
	Host           string   `json:"host"`
	Port           int      `json:"port,omitempty"`
	User           string   `json:"user"`
	Password       string   `json:"password"`
	TLS            string   `json:"tls,omitempty"`
	Env            string   `json:"env,omitempty"`
	Desc           string   `json:"desc,omitempty"`
	AllowDatabases []string `json:"allow_databases,omitempty"`
}

func (i *Instance) EffectivePort() int {
	if i.Port == 0 {
		return DefaultPort
	}
	return i.Port
}

func (i *Instance) EffectiveTLS() string {
	if i.TLS == "" {
		return DefaultTLS
	}
	return i.TLS
}

func (i *Instance) Addr() string {
	host := i.Host
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	return host + ":" + strconv.Itoa(i.EffectivePort())
}

type Config struct {
	TTL       string               `json:"ttl,omitempty"`
	Instances map[string]*Instance `json:"instances"`
}

// EffectiveTTL returns the configured cache lifetime, or the default.
func (c *Config) EffectiveTTL() (time.Duration, error) {
	if c.TTL == "" {
		return DefaultTTL, nil
	}
	return ParseTTL(c.TTL)
}

// ParseTTL accepts Go durations plus a "d" (days) suffix such as "7d".
func ParseTTL(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if n, ok := strings.CutSuffix(s, "d"); ok {
		days, err := strconv.ParseFloat(n, 64)
		if err != nil || days < 0 {
			return 0, fmt.Errorf("无效的时长 %q", s)
		}
		return time.Duration(days * float64(24*time.Hour)), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("无效的时长 %q(示例: 168h、7d、30m)", s)
	}
	return d, nil
}

// ResolveDir maps a user-supplied path to the dbmeta directory: a path ending
// in "dbmeta" is used as is, anything else is treated as a project root.
func ResolveDir(p string) string {
	p = filepath.Clean(p)
	if filepath.Base(p) == DirName {
		return p
	}
	return filepath.Join(p, DirName)
}

func ConfigPath(dir string) string { return filepath.Join(dir, FileName) }

// Find returns the dbmeta directory. An explicit path (flag or DBMETA_DIR) wins;
// otherwise it walks up from start looking for dbmeta/config.json, like git does for .git.
func Find(start, explicit string) (string, error) {
	if explicit != "" {
		dir := ResolveDir(explicit)
		if _, err := os.Stat(ConfigPath(dir)); err != nil {
			return "", fmt.Errorf("%w(已检查 %s)", ErrNotFound, dir)
		}
		return dir, nil
	}
	d, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		dir := filepath.Join(d, DirName)
		if st, err := os.Stat(ConfigPath(dir)); err == nil && !st.IsDir() {
			return dir, nil
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", ErrNotFound
		}
		d = parent
	}
}

// Init creates the directory, an empty config (0600) and .gitignore. It never
// overwrites an existing config. created lists the files it wrote.
func Init(dir string) (created []string, err error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	cp := ConfigPath(dir)
	if _, err := os.Stat(cp); os.IsNotExist(err) {
		if err := Save(dir, &Config{Instances: map[string]*Instance{}}); err != nil {
			return nil, err
		}
		created = append(created, cp)
	} else if err != nil {
		return nil, err
	}
	gp := filepath.Join(dir, gitignoreName)
	if _, err := os.Stat(gp); os.IsNotExist(err) {
		if err := os.WriteFile(gp, []byte(FileName+"\n"), 0o644); err != nil {
			return nil, err
		}
		created = append(created, gp)
	}
	return created, nil
}

// Load reads and validates dbmeta/config.json.
func Load(dir string) (*Config, error) {
	path := ConfigPath(dir)
	st, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if runtime.GOOS != "windows" && st.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s 含有密码,权限 %04o 过宽,请运行: chmod 600 %s", path, st.Mode().Perm(), path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var c Config
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("解析 %s 失败: %w", path, err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("解析 %s 失败: JSON 之后还有多余内容", path)
	}
	if c.Instances == nil {
		c.Instances = map[string]*Instance{}
	}
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

// Validate checks field values. Names are visited in sorted order so errors are stable.
func (c *Config) Validate() error {
	if _, err := c.EffectiveTTL(); err != nil {
		return fmt.Errorf("ttl: %w", err)
	}
	for _, name := range c.Names() {
		if err := guard.InstanceName(name); err != nil {
			return err
		}
		if err := c.Instances[name].Validate(); err != nil {
			return fmt.Errorf("instances.%s: %w", name, err)
		}
	}
	return nil
}

func (i *Instance) Validate() error {
	if i == nil {
		return errors.New("配置为空")
	}
	if strings.TrimSpace(i.Host) == "" {
		return errors.New("host 不能为空")
	}
	if i.Port < 0 || i.Port > 65535 {
		return fmt.Errorf("port %d 超出范围", i.Port)
	}
	if i.TLS != "" && !tlsModes[i.TLS] {
		return fmt.Errorf("tls %q 无效,可选: false, preferred, true, skip-verify", i.TLS)
	}
	for _, db := range i.AllowDatabases {
		if err := guard.Ident("allow_databases 中的库名", db); err != nil {
			return err
		}
	}
	return nil
}

func (c *Config) Names() []string {
	names := make([]string, 0, len(c.Instances))
	for n := range c.Instances {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Instance looks up an instance by name.
func (c *Config) Instance(name string) (*Instance, error) {
	if err := guard.InstanceName(name); err != nil {
		return nil, err
	}
	inst, ok := c.Instances[name]
	if !ok {
		return nil, fmt.Errorf("实例 %q 不存在(可用: %s)", name, strings.Join(c.Names(), ", "))
	}
	return inst, nil
}

// Save writes the config atomically with mode 0600. encoding/json sorts map
// keys, so instances come out ordered by name.
func Save(dir string, c *Config) error {
	if c.Instances == nil {
		c.Instances = map[string]*Instance{}
	}
	if err := c.Validate(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(configFileMode); err != nil && runtime.GOOS != "windows" {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), ConfigPath(dir))
}
