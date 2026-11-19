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

// tool 是 hmck 管理的一个独立工具：内置的写在 registry 里，
// 以后新增的可以直接 `hmck add owner/repo` 存进本机配置，不用重新编译 hmck。
type tool struct {
	Key         string   `json:"key"`                    // 也用作发布资产前缀
	Name        string   `json:"name,omitempty"`         // 展示名，默认同 Key
	Desc        string   `json:"desc,omitempty"`         // 一句话说明
	Repo        string   `json:"repo"`                   // GitHub owner/name
	Binary      string   `json:"binary,omitempty"`       // 安装后的可执行文件名（不含 .exe），默认同 Key
	Platforms   []string `json:"platforms"`              // 该工具发布过哪些 os/arch
	Checksums   bool     `json:"checksums,omitempty"`    // 发布里有没有 checksums.txt
	Skill       bool     `json:"skill,omitempty"`        // 支不支持 `<tool> skill --quiet`
	PostInstall []string `json:"post_install,omitempty"` // 装完/升级后要跑的子命令（忽略失败）

	Custom bool `json:"-"` // 来自本机配置，不是内置
	Self   bool `json:"-"` // hmck 自己：路径取当前可执行文件，版本用内置的
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
			"windows/amd64", "windows/arm64",
		},
		Checksums:   true,
		Skill:       true,
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
		Checksums:   true,
		Skill:       true,
		PostInstall: []string{"skill", "--quiet"},
	},
	{
		// hmck 自己也在 GitHub 发版，升级时和别的工具走同一套下载/校验/替换
		Key:    "hmck",
		Name:   "hmck",
		Desc:   "管理这些命令行小工具，包括它自己",
		Repo:   "hongmengchen/hmck",
		Binary: "hmck",
		Platforms: []string{
			"darwin/amd64", "darwin/arm64",
			"linux/amd64", "linux/arm64",
			"windows/amd64", "windows/arm64",
		},
		Checksums:   true,
		PostInstall: []string{"skill", "--quiet"},
		Self:        true,
	},
}

// toolMarks 是每个工具在终端里的招牌，和各自仓库 docs/logo.svg 同一套几何：
// hmck 方框 + 向上箭头、kube-token 六边形 + K、npm-token 方框 + N。
// 三行、五格宽，行高对齐卡片，不依赖图片或字体。
var toolMarks = map[string][]string{
	"hmck":       {"╭───╮", "│ ↑ │", "╰───╯"},
	"kube-token": {"╱───╲", "│ K │", "╲───╱"},
	"npm-token":  {"┌───┐", "│ N │", "└───┘"},
}

// toolLogo 取工具的招牌；自定义工具用同款方框放名字首字母
func toolLogo(t tool) []string {
	if mark, ok := toolMarks[strings.ToLower(t.Key)]; ok {
		return mark
	}
	ch := "*"
	key := strings.TrimSpace(t.Key)
	if key != "" {
		r := []rune(strings.ToUpper(key))[0]
		// 中文等宽字符会撑破五格宽的方框，只有 ASCII 字母数字才放进去
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			ch = string(r)
		}
	}
	return []string{"┌───┐", "│ " + ch + " │", "└───┘"}
}

// allTools 是内置 + 本机配置里的自定义工具；key 冲突时内置优先
func allTools() ([]tool, error) {
	out := append([]tool(nil), registry...)
	custom, err := loadCustomTools()
	if err != nil {
		return out, err
	}
	seen := make(map[string]bool, len(out))
	for _, t := range out {
		seen[strings.ToLower(t.Key)] = true
	}
	for _, t := range custom {
		if seen[strings.ToLower(t.Key)] {
			continue
		}
		seen[strings.ToLower(t.Key)] = true
		out = append(out, t)
	}
	return out, nil
}

func toolByKey(key string) (tool, bool) {
	k := strings.ToLower(strings.TrimSpace(key))
	tools, _ := allTools()
	for _, t := range tools {
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

var userHomeDir = os.UserHomeDir // 测试里替换

// defaultBinDir 是所有工具统一的安装目录：~/.local/bin
// （Windows 上是 %USERPROFILE%\.local\bin，和 Claude Code、codex 一样的放法）
func defaultBinDir() (string, error) {
	home, err := userHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "bin"), nil
}

// installDir 是新工具的安装位置：跟 hmck 自己放在同一个目录。
// hmck 装在统一目录（~/.local/bin）时，新工具自然也在那里；
// 从旧版 Windows 目录启动时不再往里装，直接落到统一目录。
func installDir() (string, error) {
	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		dir := filepath.Dir(exe)
		if writable(dir) && !isLegacyPath(exe) {
			return dir, nil
		}
	}
	dir, err := defaultBinDir()
	if err != nil {
		return "", err
	}
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
	cmd := exec.CommandContext(ctx, path, "version")
	cmd.Env = append(os.Environ(), "HMCK_NO_MIGRATE=1") // 只问版本，别让子进程顺手搬家
	out, err := cmd.Output()
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
	Installed string    // 已安装版本；空 = 未安装
	Latest    string    // 最新版本；空 = 没查出来
	UpdatedAt time.Time // 上一次装上/升级的时间；零值 = 不知道
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

// inspectLocal 只看本机：装在哪、是什么版本，不联网
func inspectLocal(t tool) toolState {
	st := toolState{Tool: t}
	if t.Self {
		// 正在运行的这一份才是“已安装”的版本；不要去 PATH 里找旧副本
		if exe, err := os.Executable(); err == nil {
			if resolved, err := filepath.EvalSymlinks(exe); err == nil {
				exe = resolved
			}
			st.Path = exe
		}
		st.Installed = version
		st.UpdatedAt = updatedAtFor(t.Key, st.Installed, st.Path)
	} else if p, ok := findTool(t); ok {
		st.Path = p
		st.Installed = installedVersion(p)
		st.UpdatedAt = updatedAtFor(t.Key, st.Installed, p)
	}
	if !supports(t, runtime.GOOS, runtime.GOARCH) {
		st.CheckErr = fmt.Sprintf("%s 没有 %s/%s 的发布", t.Key, runtime.GOOS, runtime.GOARCH)
	}
	return st
}

// inspectLatest 问 GitHub 最新版；平台不支持时直接原样返回
func inspectLatest(st toolState) toolState {
	t := st.Tool
	if !supports(t, runtime.GOOS, runtime.GOARCH) {
		return st
	}
	tag, err := latestTag(t.Repo)
	if err != nil {
		st.CheckErr = err.Error()
		return st
	}
	st.Latest = strings.TrimPrefix(tag, "v")
	st.CheckErr = ""
	return st
}

// inspect 查一个工具的本地版本和远端最新版
func inspect(t tool) toolState {
	return inspectLatest(inspectLocal(t))
}
