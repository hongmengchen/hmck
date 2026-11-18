package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// tool 是 hmck 管理的一个独立工具。加新工具只需在这里加一条。
type tool struct {
	Key         string   // 也用作发布资产前缀
	Name        string   // 展示名
	Desc        string   // 一句话说明
	Repo        string   // GitHub owner/name
	Binary      string   // 安装后的可执行文件名（不含 .exe）
	Platforms   []string // 该工具发布过哪些 os/arch
	Checksums   bool     // 发布里有没有 checksums.txt
	PostInstall []string // 装完/升级后要跑的子命令（忽略失败）
}

var registry = []tool{
	{
		Key:    "kube-token",
		Name:   "kube-token",
		Desc:   "粘贴新 token 替换 kubeconfig 里的旧值",
		Repo:   "hongmengchen/kube-token",
		Binary: "kube-token",
		Platforms: []string{
			"darwin/amd64", "darwin/arm64",
			"linux/amd64", "linux/arm64",
			"windows/amd64",
		},
		PostInstall: []string{"skill", "--quiet"},
	},
	{
		Key:    "npm-token",
		Name:   "npm-token",
		Desc:   "检查并更新 ~/.npmrc 里的 registry token",
		Repo:   "hongmengchen/npm-token",
		Binary: "npm-token",
		Platforms: []string{
			"darwin/amd64", "darwin/arm64",
			"linux/amd64", "linux/arm64",
			"windows/amd64", "windows/arm64",
		},
		Checksums: true,
	},
}

func toolByKey(key string) (tool, bool) {
	k := strings.ToLower(strings.TrimSpace(key))
	for _, t := range registry {
		if strings.ToLower(t.Key) == k || strings.ToLower(t.Binary) == k ||
			strings.TrimSuffix(strings.ToLower(t.Key), "-token") == k {
			return t, true
		}
	}
	return tool{}, false
}

func assetName(t tool, goos, goarch string) string {
	name := fmt.Sprintf("%s_%s_%s", t.Key, goos, goarch)
	if goos == "windows" {
		name += ".exe"
	}
	return name
}

func binaryName(t tool, goos string) string {
	if goos == "windows" {
		return t.Binary + ".exe"
	}
	return t.Binary
}

func archName(machine string) string {
	switch machine {
	case "x86_64", "amd64", "AMD64":
		return "amd64"
	case "arm64", "aarch64", "ARM64":
		return "arm64"
	}
	return ""
}

func supports(t tool, goos, goarch string) bool {
	want := goos + "/" + goarch
	for _, p := range t.Platforms {
		if p == want {
			return true
		}
	}
	return false
}

// installDir 是没在 PATH 里找到时的新工具安装位置：优先 hmck 自己所在目录
func installDir() (string, error) {
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		if writable(dir) {
			return dir, nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

func writable(dir string) bool {
	f, err := os.CreateTemp(dir, ".hmck-w-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
}

// findTool 先在 PATH 里找，再找 hmck 旁边的标准安装目录
func findTool(t tool) (string, bool) {
	if p, err := exec.LookPath(t.Binary); err == nil {
		return p, true
	}
	if dir, err := installDir(); err == nil {
		p := filepath.Join(dir, binaryName(t, runtime.GOOS))
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, true
		}
	}
	return "", false
}

// installedVersion 跑 `<tool> version` 取版本号；失败返回空
func installedVersion(path string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "version").Output()
	if err != nil {
		return ""
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return ""
	}
	return strings.TrimPrefix(fields[len(fields)-1], "v")
}

// toolState 是一次检查的全部结果
type toolState struct {
	Tool      tool
	Path      string
	Installed string // 已安装版本；空 = 未安装
	Latest    string // 最新版本；空 = 没查出来
	CheckErr  string
}

func (s toolState) Missing() bool { return s.Path == "" || s.Installed == "" }
func (s toolState) Behind() bool {
	return !s.Missing() && s.Latest != "" && isNewer(s.Latest, s.Installed)
}

func (s toolState) Status() (string, string) {
	switch {
	case s.Missing():
		return "未安装", "missing"
	case s.CheckErr != "":
		return "已安装（最新版没查到）", "warn"
	case s.Behind():
		return "可升级 " + s.Latest, "behind"
	default:
		return "已是最新", "ok"
	}
}

// inspect 查一个工具的本地版本和远端最新版
func inspect(t tool) toolState {
	st := toolState{Tool: t}
	if p, ok := findTool(t); ok {
		st.Path = p
		st.Installed = installedVersion(p)
	}
	if !supports(t, runtime.GOOS, runtime.GOARCH) {
		st.CheckErr = fmt.Sprintf("%s 没有 %s/%s 的发布", t.Key, runtime.GOOS, runtime.GOARCH)
		return st
	}
	tag, err := latestTag(t.Repo)
	if err != nil {
		st.CheckErr = err.Error()
		return st
	}
	st.Latest = strings.TrimPrefix(tag, "v")
	return st
}
