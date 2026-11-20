package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

// workdeskBin — клиент WorkDesk с режимом --mcp (MCP-сервер по stdio): через него Claude Code
// подключается к другим устройствам WorkDesk по ID. Пусто — режим не подключается.
var workdeskBin = ""

func workdeskCandidates() []string {
	switch runtime.GOOS {
	case "windows":
		pf := os.Getenv("ProgramFiles")
		if pf == "" {
			pf = `C:\Program Files`
		}
		return []string{filepath.Join(pf, "WorkDesk", "WorkDesk.exe")}
	case "darwin":
		// DMG WorkDesk ставит бандл RustDesk.app; обычный RustDesk без --mcp отсеет probeMcp
		return []string{
			"/Applications/WorkDesk.app/Contents/MacOS/WorkDesk",
			"/Applications/RustDesk.app/Contents/MacOS/RustDesk",
		}
	default:
		return []string{"/usr/bin/workdesk", "/usr/share/workdesk/workdesk", "/usr/lib/workdesk/workdesk"}
	}
}

func detectWorkdesk() string {
	if p, err := exec.LookPath("workdesk"); err == nil {
		return p
	}
	for _, c := range workdeskCandidates() {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	return ""
}

// mcpConfigArgs — аргументы claude для подключения WorkDesk как MCP-сервера.
func mcpConfigArgs() []string {
	if workdeskBin == "" {
		return nil
	}
	b, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{
		"workdesk": map[string]any{"type": "stdio", "command": workdeskBin, "args": []string{"--mcp"}},
	}})
	return []string{"--mcp-config", string(b)}
}

// probeMcp проверяет, что программа отвечает на initialize по MCP (stdio, JSON по строкам).
// Старые клиенты WorkDesk не знают --mcp и открыли бы окно — их не подключаем.
func probeMcp(bin string, timeout time.Duration) error {
	cmd := exec.Command(bin, "--mcp")
	hideWindow(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	defer func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	req := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"workdesk-claude","version":"` + version + `"}}}` + "\n"
	if _, err := stdin.Write([]byte(req)); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			var m struct {
				ID     json.RawMessage `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  json.RawMessage `json:"error"`
			}
			if json.Unmarshal(sc.Bytes(), &m) != nil || string(m.ID) != "1" {
				continue
			}
			if len(m.Result) > 0 {
				done <- nil
			} else {
				done <- fmt.Errorf("ошибка initialize: %s", m.Error)
			}
			return
		}
		done <- errors.New("нет ответа MCP")
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		return errors.New("нет ответа за " + timeout.String())
	}
}
