package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"golang.org/x/term"
)

type loginResponse struct {
	Type        string `json:"type"`
	AccessToken string `json:"access_token"`
	Error       string `json:"error"`
}

// login получает токен WorkDesk тем же запросом, что и клиент WorkDesk (POST /api/login).
// uuid отличается от uuid клиента, чтобы вход агента не завершал сеанс клиента на этом ПК.
func login(server, username, password, device string) (string, error) {
	host, _ := os.Hostname()
	body, _ := json.Marshal(map[string]any{
		"username": username, "password": password, "id": device, "uuid": "claude-agent-" + device,
		"autoLogin": true, "type": "account",
		"deviceInfo": map[string]string{"os": runtime.GOOS, "type": "claude-agent", "name": host},
	})
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Post(strings.TrimRight(server, "/")+"/api/login", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var r loginResponse
	_ = json.Unmarshal(raw, &r)
	if resp.StatusCode != http.StatusOK {
		if r.Error != "" {
			return "", errors.New(r.Error)
		}
		return "", fmt.Errorf("сервер ответил %d", resp.StatusCode)
	}
	if r.Type != "access_token" || r.AccessToken == "" {
		return "", errors.New("нужна двухэтапная проверка: войдите через консоль, вход по коду агент пока не поддерживает")
	}
	return r.AccessToken, nil
}

func prompt(in *bufio.Reader, label string) string {
	fmt.Print(label)
	s, _ := in.ReadString('\n')
	return strings.TrimSpace(s)
}

func cmdLogin(cfg *Config, server, device string) error {
	if server != "" {
		cfg.Server = server
	}
	if device == "" {
		device = detectDeviceID()
	}
	in := bufio.NewReader(os.Stdin)
	if device == "" {
		device = prompt(in, "ID WorkDesk этого компьютера (окно программы WorkDesk): ")
	}
	if device == "" {
		return errors.New("не указан ID WorkDesk")
	}
	user := prompt(in, "Логин WorkDesk: ")
	fmt.Print("Пароль: ")
	pw, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		return err
	}
	token, err := login(cfg.Server, user, string(pw), device)
	if err != nil {
		return err
	}
	cfg.Token, cfg.Username, cfg.Device = token, user, device
	if err := saveConfig(cfg); err != nil {
		return err
	}
	fmt.Printf("Готово: компьютер %s привязан к учетной записи %s.\n", device, user)
	return nil
}
