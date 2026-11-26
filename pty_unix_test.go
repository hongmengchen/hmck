//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// 真起一个子进程走伪终端，把「读输出 → 喂虚拟终端 → 画屏幕」这条路走通。
// 脚本只在终端里说话：先打招呼，等一行输入，再把它念回来。
func TestPaneRunsRealChild(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "demo.sh")
	if err := os.WriteFile(script, []byte(
		"#!/bin/sh\nprintf 'hello-tui\\r\\n'\nread x\nprintf 'got:%s\\r\\n' \"$x\"\nsleep 30\n",
	), 0o755); err != nil {
		t.Fatal(err)
	}

	p, err := startPane(tool{Key: "demo"}, script, 40, 10)
	if err != nil {
		t.Fatalf("起面板失败: %v", err)
	}
	defer p.close()

	screen := func() string { return strings.Join(p.lines(), "\n") }
	waitFor := func(want string) {
		t.Helper()
		deadline := time.After(10 * time.Second)
		for {
			select {
			case msg := <-p.ch:
				if _, ok := msg.(paneExitMsg); ok {
					t.Fatalf("子进程提前退出，等的是 %q，屏幕是:\n%s", want, screen())
				}
			case <-deadline:
				t.Fatalf("等不到 %q，屏幕是:\n%s", want, screen())
			}
			if strings.Contains(screen(), want) {
				return
			}
		}
	}

	waitFor("hello-tui")
	p.write(paneKeyBytes(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("abc")}))
	p.write(paneKeyBytes(tea.KeyMsg{Type: tea.KeyEnter}))
	waitFor("got:abc")
}
