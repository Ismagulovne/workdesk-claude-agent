package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// TestMain: с FAKE_CLAUDE=1 тестовый бинарник ведет себя как `claude -p ... --output-format stream-json`.
func TestMain(m *testing.M) {
	if os.Getenv("FAKE_CLAUDE") == "1" {
		fakeClaude(os.Args[1:])
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func fakeClaude(args []string) {
	if len(args) == 1 && args[0] == "--version" {
		fmt.Println("2.1.0 (Claude Code)")
		return
	}
	get := func(flag string) string {
		for i, a := range args {
			if a == flag && i+1 < len(args) {
				return args[i+1]
			}
		}
		return ""
	}
	sid := get("--resume")
	if sid == "" {
		sid = "sess-1"
	}
	out := func(v any) { b, _ := json.Marshal(v); fmt.Println(string(b)) }
	out(map[string]any{"type": "system", "subtype": "init", "session_id": sid, "permissionMode": get("--permission-mode")})
	prompt, allowed := get("-p"), get("--allowedTools")
	if strings.Contains(prompt, "удали") && !strings.Contains(allowed, "Bash") {
		out(map[string]any{"type": "result", "subtype": "success", "session_id": sid, "result": "нужно разрешение",
			"permission_denials": []any{map[string]any{"tool_name": "Bash", "tool_use_id": "tu1", "tool_input": map[string]any{"command": "rm tmp.txt"}}}})
		return
	}
	out(map[string]any{"type": "result", "subtype": "success", "session_id": sid, "result": "ok: " + prompt + " | allowed=" + allowed})
}

func useFakeClaude(t *testing.T) {
	t.Setenv("FAKE_CLAUDE", "1")
	old := claudeBin
	claudeBin = os.Args[0]
	t.Cleanup(func() { claudeBin = old })
}

type collector struct{ ch chan map[string]any }

func (c *collector) send(m map[string]any) {
	b, _ := json.Marshal(m)
	var cp map[string]any
	_ = json.Unmarshal(b, &cp)
	c.ch <- cp
}

func (c *collector) next(t *testing.T, typ string) map[string]any {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case m := <-c.ch:
			if m["t"] == typ {
				return m
			}
		case <-timeout:
			t.Fatalf("нет сообщения %s", typ)
		}
	}
}

func resultText(t *testing.T, c *collector) string {
	t.Helper()
	for {
		ev := c.next(t, "event")["event"].(map[string]any)
		if ev["type"] == "workdesk" {
			t.Fatalf("ошибка агента: %v", ev["message"])
		}
		if ev["type"] == "result" {
			return ev["result"].(string)
		}
	}
}

func TestChatResumeAndPermissions(t *testing.T) {
	useFakeClaude(t)
	c := &collector{ch: make(chan map[string]any, 100)}
	s := newChatSession("s1", t.TempDir(), "default", "", c.send)

	s.Input("привет")
	if got := resultText(t, c); got != "ok: привет | allowed=" {
		t.Fatalf("первый ответ: %q", got)
	}
	if s.sessionID != "sess-1" {
		t.Fatalf("session_id не запомнен: %q", s.sessionID)
	}

	s.Input("удали tmp.txt")
	resultText(t, c)
	ask := c.next(t, "ask")
	if ask["tool"] != "Bash" || ask["id"] != "tu1" {
		t.Fatalf("ask: %v", ask)
	}

	s.Answer("tu1", true)
	if got := resultText(t, c); !strings.Contains(got, "allowed=Bash") || !strings.Contains(got, "разрешил") {
		t.Fatalf("после разрешения: %q", got)
	}
	s.Answer("tu1", true) // повторный ответ игнорируется
	s.Close()
}

func TestAgentOverWebSocket(t *testing.T) {
	useFakeClaude(t)
	got := make(chan map[string]any, 100)
	toAgent := make(chan map[string]any, 10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" || r.URL.Query().Get("role") != "agent" || r.URL.Query().Get("device") != "123456789" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		go func() {
			for m := range toAgent {
				b, _ := json.Marshal(m)
				_ = c.Write(r.Context(), websocket.MessageText, b)
			}
		}()
		for {
			_, data, err := c.Read(r.Context())
			if err != nil {
				return
			}
			var m map[string]any
			_ = json.Unmarshal(data, &m)
			got <- m
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := &Config{Server: srv.URL, Token: "tok", Device: "123456789", Cwd: t.TempDir(), Username: "owner"}
	done := make(chan error, 1)
	go func() { done <- runOnce(ctx, cfg) }()

	c := &collector{ch: got}
	hello := c.next(t, "hello")
	if hello["claude_version"] != "2.1.0 (Claude Code)" {
		t.Fatalf("hello: %v", hello)
	}
	toAgent <- map[string]any{"t": "open", "sid": "x1", "mode": "chat", "permission": "plan"}
	c.next(t, "opened")
	toAgent <- map[string]any{"t": "input", "sid": "x1", "text": "статус"}
	if r := resultText(t, c); r != "ok: статус | allowed=" {
		t.Fatalf("ответ: %q", r)
	}
	close(toAgent)
	cancel()
	<-done

	bad := &Config{Server: srv.URL, Token: "wrong", Device: "123456789"}
	if err := runOnce(context.Background(), bad); err != errUnauthorized {
		t.Fatalf("ожидался errUnauthorized, получено %v", err)
	}
}

func TestDeviceIDParsing(t *testing.T) {
	cases := map[string]string{
		"id = '123456789'\nenc_id = 'x'\n": "123456789",
		"salt = 'a'\nid = \"987654321\"\n":   "987654321",
		"enc_id = '00'\n":                    "",
	}
	for in, want := range cases {
		m := idLine.FindStringSubmatch(in)
		got := ""
		if m != nil {
			got = m[1]
		}
		if got != want {
			t.Errorf("%q: получено %q, ожидалось %q", in, got, want)
		}
	}
}

func TestMcpConfigArgs(t *testing.T) {
	old := workdeskBin
	t.Cleanup(func() { workdeskBin = old })

	workdeskBin = ""
	for _, a := range newChatSession("s", ".", "default", "", nil).args("x") {
		if a == "--mcp-config" {
			t.Fatal("без WorkDesk --mcp-config не нужен")
		}
	}

	workdeskBin = "/opt/WorkDesk/WorkDesk"
	args := newChatSession("s", ".", "default", "", nil).args("x")
	i := -1
	for k, a := range args {
		if a == "--mcp-config" {
			i = k
		}
	}
	if i < 0 || i+1 >= len(args) {
		t.Fatalf("нет --mcp-config: %v", args)
	}
	var cfg struct {
		McpServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(args[i+1]), &cfg); err != nil {
		t.Fatal(err)
	}
	s := cfg.McpServers["workdesk"]
	if s.Command != workdeskBin || len(s.Args) != 1 || s.Args[0] != "--mcp" {
		t.Fatalf("конфиг MCP: %+v", cfg)
	}
}
