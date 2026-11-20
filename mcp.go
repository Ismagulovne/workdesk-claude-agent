package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
		apps, _ := filepath.Glob("/Applications/*.app/Contents/MacOS/*")
		out := []string{}
		for _, a := range apps {
			if b := filepath.Base(filepath.Dir(filepath.Dir(filepath.Dir(a)))); b == "WorkDesk.app" || b == "RustDesk.app" {
				out = append(out, a)
			}
		}
		return out
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
