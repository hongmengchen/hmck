//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/creack/pty"
)

// paneSupported：这类系统上有伪终端，能把工具的 TUI 塞进右侧半屏
const paneSupported = true

// unixChild 是跑在伪终端上的子进程。它以为对面就是真终端，
// 才会进自己的全屏界面；我们守在另一头读写。
type unixChild struct {
	tty *os.File
	cmd *exec.Cmd
}

func openChild(path string, cols, rows int) (ptyChild, error) {
	cmd := exec.Command(path)
	// 子进程照样用当前环境，只确保它知道自己是 256 色的终端
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	tty, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		return nil, fmt.Errorf("开独立终端失败: %w", err)
	}
	return &unixChild{tty: tty, cmd: cmd}, nil
}

func (c *unixChild) Read(p []byte) (int, error)  { return c.tty.Read(p) }
func (c *unixChild) Write(p []byte) (int, error) { return c.tty.Write(p) }

func (c *unixChild) Resize(cols, rows int) error {
	return pty.Setsize(c.tty, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
}

func (c *unixChild) Kill() error {
	if c.cmd.Process == nil {
		return nil
	}
	return c.cmd.Process.Kill()
}

// Wait 收尸并把终端关掉；只该调一次
func (c *unixChild) Wait() error {
	err := c.cmd.Wait()
	c.tty.Close()
	return err
}
