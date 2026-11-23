package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/term"
)

// clampInt 把 v 收在 [lo, hi] 里
func clampInt(v, lo, hi int) int {
	if hi < lo {
		hi = lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// isTTY 判断是不是真终端。/dev/null 也是字符设备，光看 ModeCharDevice
// 会把它当成终端——脚本里 `< /dev/null` 跑卸载时就会以为有人能敲名字
func isTTY(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
}

// 进度条的填充和底槽：实心方块画条，比竖线、等号看着整齐
const (
	barFill  = "█"
	barEmpty = "░"
)

// humanSize 把字节数写成人看的大小，进度条旁边最多占几个字
func humanSize(n int64) string {
	switch {
	case n < 1<<10:
		return fmt.Sprintf("%d B", n)
	case n < 1<<20:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	}
}

// shortenHome 把家目录前缀换成 ~，输出短一点
func shortenHome(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if strings.HasPrefix(p, home+string(filepath.Separator)) {
		return "~" + p[len(home):]
	}
	return p
}
