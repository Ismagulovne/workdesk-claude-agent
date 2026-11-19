// workdesk-claude — агент WorkDesk+Claude: дает телефону доступ к Claude Code на этом компьютере
// через сервер WorkDesk. Протокол: console/docs/CLAUDE_RELAY.md в репозитории WorkDesk.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

var version = "0.1.0"

func usage() {
	fmt.Fprint(os.Stderr, `WorkDesk+Claude агент `+version+`

  workdesk-claude login [-server URL] [-device ID]   привязать компьютер к учетной записи WorkDesk
  workdesk-claude run [-cwd ПАПКА]                   подключиться и ждать команд с телефона
  workdesk-claude status                             показать настройки и наличие Claude Code
  workdesk-claude logout                             отвязать компьютер

Claude Code должен быть установлен на этом компьютере, вход в него — вашим аккаунтом Claude (claude, затем /login).
`)
}

func main() {
	log.SetFlags(log.LstdFlags)
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("настройки: %v", err)
	}
	fs := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	server := fs.String("server", "", "адрес сервера WorkDesk")
	device := fs.String("device", "", "ID WorkDesk этого компьютера")
	cwd := fs.String("cwd", "", "папка по умолчанию для Claude Code")
	claude := fs.String("claude", "", "путь к claude, если его нет в PATH")
	_ = fs.Parse(os.Args[2:])
	if *claude != "" {
		claudeBin = *claude
	}

	switch os.Args[1] {
	case "login":
		if err := cmdLogin(cfg, *server, *device); err != nil {
			log.Fatalf("вход не выполнен: %v", err)
		}
	case "logout":
		cfg.Token = ""
		if err := saveConfig(cfg); err != nil {
			log.Fatal(err)
		}
		fmt.Println("Компьютер отвязан.")
	case "status":
		v, logged := claudeInfo()
		fmt.Printf("Сервер: %s\nУчетная запись: %s\nID WorkDesk: %s\nClaude Code: %s (вход: %v)\n",
			cfg.Server, orDash(cfg.Username), orDash(cfg.Device), orDash(v), logged)
	case "run":
		if cfg.Token == "" {
			log.Fatal("сначала выполните: workdesk-claude login")
		}
		if *cwd != "" {
			cfg.Cwd = *cwd
		}
		if cfg.Cwd == "" {
			cfg.Cwd, _ = os.UserHomeDir()
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		runForever(ctx, cfg)
	default:
		usage()
		os.Exit(2)
	}
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// claudeInfo: версия CLI и признак входа (файл учетных данных; на macOS они в связке ключей — считаем, что вход есть).
func claudeInfo() (string, bool) {
	cmd := exec.Command(claudeBin, "--version")
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	v := strings.TrimSpace(string(out))
	if runtime.GOOS == "darwin" || os.Getenv("ANTHROPIC_API_KEY") != "" {
		return v, true
	}
	home, _ := os.UserHomeDir()
	_, err = os.Stat(filepath.Join(home, ".claude", ".credentials.json"))
	return v, err == nil
}

func wsURL(cfg *Config) (string, error) {
	u, err := url.Parse(strings.TrimRight(cfg.Server, "/") + "/api/claude/ws")
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	}
	q := u.Query()
	q.Set("role", "agent")
	q.Set("device", cfg.Device)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

var errUnauthorized = errors.New("токен недействителен")

func runForever(ctx context.Context, cfg *Config) {
	backoff := time.Second
	for ctx.Err() == nil {
		start := time.Now()
		err := runOnce(ctx, cfg)
		if errors.Is(err, errUnauthorized) {
			log.Fatal("сервер отклонил токен: выполните workdesk-claude login заново")
		}
		if ctx.Err() != nil {
			return
		}
		if time.Since(start) > time.Minute {
			backoff = time.Second
		}
		log.Printf("соединение потеряно (%v), повтор через %s", err, backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < time.Minute {
			backoff *= 2
		}
	}
}

type session interface{ Close() }

func runOnce(ctx context.Context, cfg *Config) error {
	u, err := wsURL(cfg)
	if err != nil {
		return err
	}
	c, resp, err := websocket.Dial(ctx, u, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + cfg.Token}},
	})
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			return errUnauthorized
		}
		return err
	}
	defer c.CloseNow()
	c.SetReadLimit(1 << 20)

	var wmu sync.Mutex
	send := func(m map[string]any) {
		b, _ := json.Marshal(m)
		wmu.Lock()
		defer wmu.Unlock()
		wctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		_ = c.Write(wctx, websocket.MessageText, b)
	}

	host, _ := os.Hostname()
	v, logged := claudeInfo()
	send(map[string]any{"t": "hello", "hostname": host, "os": runtime.GOOS + "/" + runtime.GOARCH,
		"agent_version": version, "claude_version": v, "claude_logged_in": logged, "cwd": cfg.Cwd})
	log.Printf("подключен к %s как %s (ID %s), Claude Code: %s", cfg.Server, cfg.Username, cfg.Device, orDash(v))

	sessions := map[string]session{}
	defer func() {
		for _, s := range sessions {
			s.Close()
		}
	}()

	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			if websocket.CloseStatus(err) == 4001 {
				log.Fatal("этот компьютер подключился к серверу из другого окна агента; это окно завершает работу")
			}
			return err
		}
		var m struct {
			T          string `json:"t"`
			Sid        string `json:"sid"`
			Mode       string `json:"mode"`
			Cwd        string `json:"cwd"`
			Resume     string `json:"resume"`
			Permission string `json:"permission"`
			Cols, Rows int
			Text       string `json:"text"`
			Data       string `json:"data"`
			ID         string `json:"id"`
			Allow      bool   `json:"allow"`
		}
		if json.Unmarshal(data, &m) != nil || m.Sid == "" {
			continue
		}
		dir := m.Cwd
		if dir == "" {
			dir = cfg.Cwd
		}
		switch m.T {
		case "open":
			if old := sessions[m.Sid]; old != nil {
				old.Close()
			}
			if m.Mode == "term" {
				t, err := startTerm(m.Sid, dir, m.Cols, m.Rows, send)
				if err != nil {
					send(map[string]any{"t": "exit", "sid": m.Sid, "code": -1, "error": "не удалось запустить терминал: " + err.Error()})
					continue
				}
				sessions[m.Sid] = t
			} else {
				sessions[m.Sid] = newChatSession(m.Sid, dir, m.Permission, m.Resume, send)
			}
			send(map[string]any{"t": "opened", "sid": m.Sid, "mode": m.Mode})
			log.Printf("сеанс %s открыт (%s, %s)", m.Sid, m.Mode, dir)
		case "input":
			if s, ok := sessions[m.Sid].(*chatSession); ok {
				s.Input(m.Text)
			}
		case "answer":
			if s, ok := sessions[m.Sid].(*chatSession); ok {
				s.Answer(m.ID, m.Allow)
			}
		case "interrupt":
			if s, ok := sessions[m.Sid].(*chatSession); ok {
				s.Interrupt()
			}
		case "in":
			if s, ok := sessions[m.Sid].(*termSession); ok {
				s.In(m.Data)
			}
		case "resize":
			if s, ok := sessions[m.Sid].(*termSession); ok {
				s.Resize(m.Cols, m.Rows)
			}
		case "close":
			if s := sessions[m.Sid]; s != nil {
				s.Close()
				delete(sessions, m.Sid)
				log.Printf("сеанс %s закрыт", m.Sid)
			}
		}
	}
}
