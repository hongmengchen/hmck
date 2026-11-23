package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
	Host        string   `json:"host,omitempty"`         // 发布平台：github（默认）或 gitlab
	Repo        string   `json:"repo"`                   // owner/name
	Binary      string   `json:"binary,omitempty"`       // 安装后的可执行文件名（不含 .exe），默认同 Key
	Asset       string   `json:"asset,omitempty"`        // 资产名模板，默认 {key}_{goos}_{goarch}{ext}
	Archive     bool     `json:"archive,omitempty"`      // 资产是压缩包（.tar.gz，Windows 为 .zip）
	BinPath     string   `json:"bin_path,omitempty"`     // 压缩包里二进制的路径，默认 bin/<binary>
	Platforms   []string `json:"platforms"`              // 该工具发布过哪些 os/arch
	Checksums   bool     `json:"checksums,omitempty"`    // 发布里有没有 checksums.txt
	Skill       bool     `json:"skill,omitempty"`        // 支不支持 `<tool> skill --quiet`
	PostInstall []string `json:"post_install,omitempty"` // 装完/升级后要跑的子命令（忽略失败）
	VersionArgs []string `json:"version_args,omitempty"` // 问版本号的子命令，默认 version；kubectl 用 version --client

	OwnDir       bool   `json:"-"` // 只管理 hmck 目录里的那一份，不接管 PATH 上别人装的副本（如 glab、kubectl）
	Status       bool   `json:"-"` // hmck 能读出它自己的状态（登录 / 集群）
	StatusSource string `json:"-"` // 状态怎么读：glab（默认，看登录）/ kubectl（看集群）
	Custom       bool   `json:"-"` // 来自本机配置，不是内置
	Self         bool   `json:"-"` // hmck 自己：路径取当前可执行文件，版本用内置的
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
		// glab 是 GitLab 官方 CLI，在 gitlab.com 发版，资产是 tar.gz / zip。
		// 很多人的 glab 是 brew / apt 装的：hmck 只认自己目录里的那一份，
		// PATH 上的副本不接管、只在卡片上说明；登录状态读 `glab auth status`。
		Key:     "glab",
		Name:    "glab",
		Desc:    "GitLab 命令行：MR、issue、流水线都在终端里",
		Host:    "gitlab",
		Repo:    "gitlab-org/cli",
		Binary:  "glab",
		Asset:   "glab_{ver}_{goos}_{goarch}.{archive}",
		Archive: true,
		Platforms: []string{
			"darwin/amd64", "darwin/arm64",
			"linux/amd64", "linux/arm64", "linux/386",
			"windows/amd64", "windows/arm64", "windows/386",
		},
		Checksums: true,
		OwnDir:    true,
		Status:    true,
	},
	{
		// kubectl 不在 GitHub / GitLab 发版：最新版读 dl.k8s.io 的 stable.txt，
		// 资产是每个平台一个裸二进制，旁边配一个同名的 .sha256。
		// Docker Desktop 常自带一份：跟 glab 一样只认 hmck 目录里的那份。
		// 版本号问 `kubectl version --client`，不连集群；状态页才去连。
		Key:    "kubectl",
		Name:   "kubectl",
		Desc:   "Kubernetes 命令行：Pod、日志、集群都在终端里",
		Host:   "kubernetes",
		Repo:   "kubernetes/kubernetes",
		Binary: "kubectl",
		Asset:  "kubectl{ext}",
		Platforms: []string{
			"darwin/amd64", "darwin/arm64",
			"linux/amd64", "linux/arm64",
			"windows/amd64", "windows/arm64",
		},
		Checksums:    true,
		OwnDir:       true,
		Status:       true,
		StatusSource: "kubectl",
		VersionArgs:  []string{"version", "--client"},
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

// releaseHost 返回发布平台；现在支持 GitHub（默认）、GitLab 和 dl.k8s.io
func (t tool) releaseHost() string {
	switch strings.ToLower(t.Host) {
	case "gitlab":
		return "gitlab"
	case "kubernetes", "k8s":
		return "kubernetes"
	}
	return "github"
}

// assetName 按工具的模板拼出资产名。模板里可用 {key} {ver} {goos} {goarch}
// {ext}（Windows 的 .exe）和 {archive}（tar.gz / zip）。
// 不写模板就是 `<key>_<goos>_<goarch>[.exe]`，和内置工具的发布一致。
func assetName(t tool, goos, goarch, ver string) string {
	tmpl := t.Asset
	if tmpl == "" {
		tmpl = "{key}_{goos}_{goarch}{ext}"
	}
	ext, archive := "", ""
	if goos == "windows" {
		ext = ".exe"
	}
	if t.Archive {
		archive = "tar.gz"
		if goos == "windows" {
			archive = "zip"
		}
	}
	r := strings.NewReplacer(
		"{key}", t.Key,
		"{ver}", strings.TrimPrefix(strings.TrimSpace(ver), "v"),
		"{goos}", goos,
		"{goarch}", goarch,
		"{ext}", ext,
		"{archive}", archive,
	)
	return r.Replace(tmpl)
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
var runningExe = os.Executable   // 测试里替换：不跟着测试二进制所在的目录走

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
	if exe, err := runningExe(); err == nil {
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

// installTarget 是 hmck 会把这份工具装到哪：和 hmck 同一个目录。
// 不管现在装没装都能算出来，确认页和提示要先把它说给用户听
func installTarget(t tool) string {
	dir, err := installDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, binaryName(t, runtime.GOOS))
}

// pathRank 是一个目录在 PATH 里排第几；不在 PATH 里返回 -1
func pathRank(dir string) int {
	dir = filepath.Clean(dir)
	for i, d := range filepath.SplitList(os.Getenv("PATH")) {
		if d == "" {
			continue
		}
		if filepath.Clean(d) == dir {
			return i
		}
	}
	return -1
}

// shadowNote 按 PATH 顺序说清楚：hmck 装一份以后，终端里敲同一个命令会用哪份。
// glab 这类工具 brew 里常有一份，装到前面的目录就会把用户在用的那份顶掉，
// 所以这里不猜，直接按 PATH 排位算
func shadowNote(target, foreign string) (string, string) {
	if target == "" || foreign == "" {
		return "", ""
	}
	targetDir, foreignDir := filepath.Dir(target), filepath.Dir(foreign)
	switch {
	case filepath.Clean(targetDir) == filepath.Clean(foreignDir):
		return "和原来那份在同一个目录，装的位置会完全一样", "warn"
	case pathRank(targetDir) < 0:
		return shortenHome(targetDir) + " 不在 PATH 里，装完不影响终端里的命令", ""
	case pathRank(targetDir) < pathRank(foreignDir) || pathRank(foreignDir) < 0:
		return "装完终端里会先用 hmck 装的这份（" + shortenHome(targetDir) +
			" 排在 " + shortenHome(foreignDir) + " 前面）", "warn"
	default:
		return "装完终端里的命令还是原来那份（" + shortenHome(targetDir) +
			" 排在 " + shortenHome(foreignDir) + " 后面）", "ok"
	}
}

// managedPath 是 hmck 自己装出来的那一份：和 hmck 在同一个目录
func managedPath(t tool) (string, bool) {
	dir, err := installDir()
	if err != nil {
		return "", false
	}
	p := filepath.Join(dir, binaryName(t, runtime.GOOS))
	if st, err := os.Stat(p); err == nil && !st.IsDir() {
		return p, true
	}
	return "", false
}

// findTool 先在 PATH 里找，再找 hmck 旁边的标准安装目录。
// OwnDir 的工具（glab）只认 hmck 目录里的那一份，PATH 上别人装的副本归别人管。
func findTool(t tool) (string, bool) {
	if !t.OwnDir {
		if p, err := exec.LookPath(t.Binary); err == nil {
			return p, true
		}
	}
	return managedPath(t)
}

// toolBinary 是本机上能直接跑的那份：hmck 自己装的那份优先，没有就退到 PATH 上
// 别人装的（brew / apt 的 glab）。都没有才返回空。启动和分析都用它，
// 别因为 hmck 没装过就假装本机没有这个工具
func toolBinary(t tool) string {
	if p, ok := findTool(t); ok {
		return p
	}
	if copies := foreignCopies(t, ""); len(copies) > 0 {
		return copies[0]
	}
	return ""
}

// foreignCopies 列出 PATH 上其他位置的同名程序（比如 brew 装的 glab）。
// hmck 不碰它们，只在卡片和卸载提示里说明它们还在。
func foreignCopies(t tool, managed string) []string {
	name := binaryName(t, runtime.GOOS)
	managedReal := realPath(managed)
	seen := make(map[string]bool)
	var out []string
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			continue
		}
		p := filepath.Join(dir, name)
		st, err := os.Stat(p)
		if err != nil || st.IsDir() {
			continue
		}
		real := realPath(p)
		if real == managedReal || seen[real] {
			continue
		}
		seen[real] = true
		out = append(out, real)
	}
	return out
}

// realPath 解开软链接，拿不到就返回原路径；空路径返回空
func realPath(p string) string {
	if p == "" {
		return ""
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// versionPattern 匹配输出里第一个像版本号的词。
// 有的工具版本号后面还跟着构建信息（glab 是 `glab 1.130.0 (9b430a11f)`），
// 取最后一个字段会拿到 commit 号，所以按版本号的样子找。
var versionPattern = regexp.MustCompile(`(?i)\bv?\d+\.\d+(?:\.\d+)?(?:[-+][0-9a-z.-]+)?\b`)

// parseVersionOutput 从 `<tool> version` 的输出里读版本号，读不出来返回空
func parseVersionOutput(out string) string {
	if m := versionPattern.FindString(out); m != "" {
		return strings.TrimPrefix(m, "v")
	}
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return ""
	}
	return strings.TrimPrefix(fields[len(fields)-1], "v")
}

// installedVersion 跑 `<tool> version` 取版本号；失败返回空。
// 有的工具问版本要带参数（kubectl 用 `version --client`，不碰集群），
// 参数写在工具的 VersionArgs 里
func installedVersion(t tool, path string) string {
	args := t.VersionArgs
	if len(args) == 0 {
		args = []string{"version"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = append(os.Environ(), "HMCK_NO_MIGRATE=1") // 只问版本，别让子进程顺手搬家
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return parseVersionOutput(string(out))
}

// toolState 是一次检查的全部结果
type toolState struct {
	Tool       tool
	Path       string
	Installed  string    // 已安装版本；空 = 未安装
	Latest     string    // 最新版本；空 = 没查出来
	UpdatedAt  time.Time // 上一次装上/升级的时间；零值 = 不知道
	CheckErr   string
	Foreign    []string // PATH 上其他位置的同名程序，hmck 不管它们
	ForeignVer string   // PATH 上那份的版本号；hmck 没接管它，但说清楚它是什么版本

	Login     string      // 状态的一句话（如「已登录 alice@git.example.com」「✓ 已连通 eks-prod」）；空 = 不知道或不支持
	LoginKind string      // ok / warn / missing，决定这句话怎么染色
	Logins    []glabLogin // 读到的各家主机登录情况（glab 这类工具才有）
	Kube      *kubeStatus // 当前 context / API server / 连不连得上（kubectl 才有）
}

func (s toolState) Missing() bool { return s.Path == "" || s.Installed == "" }
func (s toolState) Behind() bool {
	return !s.Missing() && s.Latest != "" && isNewer(s.Latest, s.Installed)
}

func (s toolState) Status() (string, string) {
	switch {
	case s.Missing() && len(s.Foreign) > 0:
		// 本机上其实有一份能用的（brew 的 glab），只是不归 hmck 管
		if s.ForeignVer != "" {
			return "用 PATH 上的 " + s.ForeignVer, "warn"
		}
		return "用 PATH 上的那份", "warn"
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
		if exe, err := runningExe(); err == nil {
			if resolved, err := filepath.EvalSymlinks(exe); err == nil {
				exe = resolved
			}
			st.Path = exe
		}
		st.Installed = version
		st.UpdatedAt = updatedAtFor(t.Key, st.Installed, st.Path)
	} else if p, ok := findTool(t); ok {
		st.Path = p
		st.Installed = installedVersion(t, p)
		st.UpdatedAt = updatedAtFor(t.Key, st.Installed, p)
	}
	if !t.Self {
		st.Foreign = foreignCopies(t, st.Path)
		// 只装了别人的那份时（brew 的 glab、Docker Desktop 的 kubectl），
		// 问一下它的版本，界面上好说清楚
		if st.Installed == "" && len(st.Foreign) > 0 {
			st.ForeignVer = installedVersion(t, st.Foreign[0])
		}
	}
	if t.Status {
		// 状态问的是工具自己：hmck 目录里的优先，没有就用 PATH 上那份
		if bin := toolCopy(st); bin != "" {
			if t.StatusSource == "kubectl" {
				st.Kube = readKubeStatus(bin)
				if st.Kube == nil {
					st.Login, st.LoginKind = "读不出集群状态", "warn"
				} else {
					st.Login, st.LoginKind = kubeBadge(st.Kube)
				}
			} else {
				logins, err := glabAuthLogins(bin)
				switch {
				case err != nil:
					st.Login, st.LoginKind = "读不出登录状态", "warn"
				default:
					st.Logins = logins
					st.Login, st.LoginKind = loginBadgeOf(logins)
				}
			}
		}
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
	tag, err := latestTag(t)
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
