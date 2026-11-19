package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
)

const defaultServer = "https://workdesk.kz"

type Config struct {
	Server   string `json:"server"`
	Token    string `json:"token"`
	Username string `json:"username"`
	Device   string `json:"device"`
	Cwd      string `json:"cwd"`
}

func configPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "WorkDesk", "claude-agent.json"), nil
}

func loadConfig() (*Config, error) {
	p, err := configPath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return &Config{Server: defaultServer}, nil
	}
	if err != nil {
		return nil, err
	}
	c := &Config{}
	if err := json.Unmarshal(b, c); err != nil {
		return nil, err
	}
	if c.Server == "" {
		c.Server = defaultServer
	}
	return c, nil
}

// saveConfig пишет файл только для владельца: в нем токен WorkDesk.
func saveConfig(c *Config) error {
	p, err := configPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(p, b, 0o600)
}

var idLine = regexp.MustCompile(`(?m)^\s*id\s*=\s*['"]?([0-9]{6,12})['"]?\s*$`)

// workdeskConfigFiles — где клиент WorkDesk (форк RustDesk, APP_NAME=WorkDesk) хранит свой ID.
func workdeskConfigFiles() []string {
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "windows":
		files := []string{}
		if a := os.Getenv("APPDATA"); a != "" {
			files = append(files, filepath.Join(a, "WorkDesk", "config", "WorkDesk.toml"))
		}
		sys := os.Getenv("SystemRoot")
		if sys == "" {
			sys = `C:\Windows`
		}
		return append(files,
			filepath.Join(sys, `ServiceProfiles\LocalService\AppData\Roaming\WorkDesk\config\WorkDesk.toml`),
			filepath.Join(sys, `System32\config\systemprofile\AppData\Roaming\WorkDesk\config\WorkDesk.toml`))
	case "darwin":
		return []string{
			filepath.Join(home, "Library", "Preferences", "com.carriez.WorkDesk", "WorkDesk.toml"),
			"/var/root/Library/Preferences/com.carriez.WorkDesk/WorkDesk.toml",
		}
	default:
		return []string{
			filepath.Join(home, ".config", "WorkDesk", "WorkDesk.toml"),
			"/root/.config/WorkDesk/WorkDesk.toml",
		}
	}
}

// detectDeviceID читает ID WorkDesk этого компьютера; пусто, если клиент не установлен.
func detectDeviceID() string {
	for _, f := range workdeskConfigFiles() {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		if m := idLine.FindSubmatch(b); m != nil {
			return string(m[1])
		}
	}
	return ""
}
