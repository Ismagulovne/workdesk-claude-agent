package main

import (
	"encoding/base64"
	"os"
	"sync"

	"github.com/aymanbagabas/go-pty"
)

// termSession — режим term: интерактивный Claude Code в псевдотерминале (ConPTY на Windows, pty на Linux/macOS).
type termSession struct {
	sid  string
	p    pty.Pty
	cmd  *pty.Cmd
	once sync.Once
}

func startTerm(sid, cwd string, cols, rows int, send func(map[string]any)) (*termSession, error) {
	p, err := pty.New()
	if err != nil {
		return nil, err
	}
	if err := p.Resize(cols, rows); err != nil {
		p.Close()
		return nil, err
	}
	cmd := p.Command(claudeBin)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "COLORTERM=truecolor")
	if err := cmd.Start(); err != nil {
		p.Close()
		return nil, err
	}
	t := &termSession{sid: sid, p: p, cmd: cmd}
	go func() {
		buf := make([]byte, 32<<10)
		for {
			n, err := p.Read(buf)
			if n > 0 {
				send(map[string]any{"t": "out", "sid": sid, "data": base64.StdEncoding.EncodeToString(buf[:n])})
			}
			if err != nil {
				break
			}
		}
	}()
	go func() {
		code := 0
		if err := cmd.Wait(); err != nil {
			code = 1
			if cmd.ProcessState != nil {
				code = cmd.ProcessState.ExitCode()
			}
		}
		t.Close()
		send(map[string]any{"t": "exit", "sid": sid, "code": code})
	}()
	return t, nil
}

func (t *termSession) In(b64 string) {
	b, err := base64.StdEncoding.DecodeString(b64)
	if err == nil && len(b) > 0 {
		_, _ = t.p.Write(b)
	}
}

func (t *termSession) Resize(cols, rows int) { _ = t.p.Resize(cols, rows) }

func (t *termSession) Close() {
	t.once.Do(func() {
		if t.cmd.Process != nil {
			_ = t.cmd.Process.Kill()
		}
		_ = t.p.Close()
	})
}
