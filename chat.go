package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"sync"
)

// claudeBin — путь к CLI Claude Code; в тестах подменяется.
var claudeBin = "claude"

// chatSession — режим chat: на каждое сообщение запускается `claude -p` с --resume (docs/CLAUDE_RELAY.md).
type chatSession struct {
	sid        string
	cwd        string
	permission string
	send       func(map[string]any)

	mu        sync.Mutex
	sessionID string
	allowed   map[string]bool
	asks      map[string]string // id запроса разрешения -> инструмент
	cancel    context.CancelFunc
	running   bool
	queue     []string
}

func newChatSession(sid, cwd, permission, resume string, send func(map[string]any)) *chatSession {
	return &chatSession{sid: sid, cwd: cwd, permission: permission, sessionID: resume, send: send,
		allowed: map[string]bool{}, asks: map[string]string{}}
}

func (s *chatSession) args(text string) []string {
	a := []string{"-p", text, "--output-format", "stream-json", "--verbose", "--permission-mode", s.permission}
	if s.sessionID != "" {
		a = append(a, "--resume", s.sessionID)
	}
	a = append(a, mcpConfigArgs()...)
	if len(s.allowed) > 0 {
		tools := make([]string, 0, len(s.allowed))
		for t := range s.allowed {
			tools = append(tools, t)
		}
		sort.Strings(tools)
		a = append(a, "--allowedTools", strings.Join(tools, ","))
	}
	return a
}

// Input ставит сообщение в очередь: одновременно идет один запуск claude на сеанс.
func (s *chatSession) Input(text string) {
	s.mu.Lock()
	if s.running {
		s.queue = append(s.queue, text)
		s.mu.Unlock()
		return
	}
	s.running = true
	s.mu.Unlock()
	go s.loop(text)
}

func (s *chatSession) loop(text string) {
	for {
		s.runOnce(text)
		s.mu.Lock()
		if len(s.queue) == 0 {
			s.running = false
			s.mu.Unlock()
			return
		}
		text = s.queue[0]
		s.queue = s.queue[1:]
		s.mu.Unlock()
	}
}

func (s *chatSession) runOnce(text string) {
	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	args := s.args(text)
	s.cancel = cancel
	s.mu.Unlock()
	defer cancel()

	cmd := exec.CommandContext(ctx, claudeBin, args...)
	cmd.Dir = s.cwd
	hideWindow(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		s.fail(err)
		return
	}
	var stderr strings.Builder
	cmd.Stderr = &limitWriter{w: &stderr, n: 8 << 10}
	if err := cmd.Start(); err != nil {
		s.fail(fmt.Errorf("не удалось запустить Claude Code (%s): %w", claudeBin, err))
		return
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	for sc.Scan() {
		var ev map[string]any
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		s.send(map[string]any{"t": "event", "sid": s.sid, "event": ev})
		s.observe(ev)
	}
	err = cmd.Wait()
	if ctx.Err() != nil {
		s.send(map[string]any{"t": "event", "sid": s.sid, "event": map[string]any{"type": "workdesk", "subtype": "interrupted"}})
		return
	}
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		s.fail(fmt.Errorf("Claude Code завершился с ошибкой: %s", msg))
	}
}

// observe запоминает session_id и превращает отказы в разрешениях в запросы на телефон.
func (s *chatSession) observe(ev map[string]any) {
	if id, ok := ev["session_id"].(string); ok && id != "" {
		s.mu.Lock()
		s.sessionID = id
		s.mu.Unlock()
	}
	if ev["type"] != "result" {
		return
	}
	denials, _ := ev["permission_denials"].([]any)
	for _, d := range denials {
		m, _ := d.(map[string]any)
		tool, _ := m["tool_name"].(string)
		id, _ := m["tool_use_id"].(string)
		if tool == "" || id == "" {
			continue
		}
		s.mu.Lock()
		s.asks[id] = tool
		s.mu.Unlock()
		s.send(map[string]any{"t": "ask", "sid": s.sid, "id": id, "tool": tool, "input": m["tool_input"],
			"reason": "Claude Code запросил действие, которое не разрешено в этом сеансе"})
	}
}

// Answer: при разрешении инструмент добавляется в --allowedTools и Claude продолжает работу.
func (s *chatSession) Answer(id string, allow bool) {
	s.mu.Lock()
	tool, ok := s.asks[id]
	delete(s.asks, id)
	if ok && allow {
		s.allowed[tool] = true
	}
	s.mu.Unlock()
	if !ok {
		return
	}
	if allow {
		s.Input(fmt.Sprintf("Пользователь разрешил инструмент %s. Продолжай задачу.", tool))
	} else {
		s.Input(fmt.Sprintf("Пользователь запретил инструмент %s. Не используй его; предложи другой способ или остановись.", tool))
	}
}

func (s *chatSession) Interrupt() {
	s.mu.Lock()
	s.queue = nil
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Unlock()
}

func (s *chatSession) Close() { s.Interrupt() }

func (s *chatSession) fail(err error) {
	s.send(map[string]any{"t": "event", "sid": s.sid, "event": map[string]any{"type": "workdesk", "subtype": "error", "message": err.Error()}})
}

type limitWriter struct {
	w interface{ Write([]byte) (int, error) }
	n int
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if l.n <= 0 {
		return len(p), nil
	}
	q := p
	if len(q) > l.n {
		q = q[:l.n]
	}
	l.n -= len(q)
	_, err := l.w.Write(q)
	return len(p), err
}
