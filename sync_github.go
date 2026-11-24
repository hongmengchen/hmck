package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// GitHub 那边只做三件事：认人、建/找私有仓库、读写一个文件。
// 登录借用 gh CLI 的 token（gh auth login 已经做过浏览器登录），
// hmck 自己不保存 GitHub 凭据。
const (
	githubAPIBase   = "https://api.github.com"
	syncHTTPTimeout = 25 * time.Second
)

type ghClient struct {
	base  string
	token string
	http  *http.Client
}

// ghToken 从 gh CLI 拿用户的 GitHub token。gh 没装或没登录时给出能照着做的提示，
// 不打印 token，也不把它写进任何文件。
func ghToken() (string, error) {
	path, ok := ghPath()
	if !ok {
		return "", errors.New("没找到 gh。先装 GitHub CLI（hmck 的 TUI 同步页里可以直接装），再运行 gh auth login")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "auth", "token").Output()
	if err != nil {
		if ctx.Err() != nil {
			return "", errors.New("gh auth token 超时；检查网络后重试")
		}
		return "", errors.New("gh 还没登录。先运行 gh auth login")
	}
	token := strings.TrimSpace(string(out))
	if token == "" {
		return "", errors.New("gh 没有给出 token。先运行 gh auth login")
	}
	return token, nil
}

// syncGHToken 留成变量，测试里换成假 token，不去碰真实的 gh
var syncGHToken = ghToken

// ghDownloadURL 是没找到包管理器时给用户打开的 GitHub CLI 官方下载页
const ghDownloadURL = "https://cli.github.com/"

// ghFallbackPaths 是 gh 的常见安装位置。Windows 上刚用 winget 装完时，
// 当前进程的 PATH 还是旧的，光靠 LookPath 会找不到，所以直接去这些目录里看
func ghFallbackPaths() []string {
	var paths []string
	switch runtime.GOOS {
	case "windows":
		if d := os.Getenv("ProgramFiles"); d != "" {
			paths = append(paths, filepath.Join(d, "GitHub CLI", "gh.exe"))
		}
		if d := os.Getenv("ProgramFiles(x86)"); d != "" {
			paths = append(paths, filepath.Join(d, "GitHub CLI", "gh.exe"))
		}
		if d := os.Getenv("LOCALAPPDATA"); d != "" {
			paths = append(paths, filepath.Join(d, "Programs", "GitHub CLI", "gh.exe"))
		}
		if d := os.Getenv("USERPROFILE"); d != "" {
			paths = append(paths, filepath.Join(d, "scoop", "shims", "gh.exe"))
		}
		if d := os.Getenv("ProgramData"); d != "" {
			paths = append(paths, filepath.Join(d, "chocolatey", "bin", "gh.exe"))
		}
	case "darwin":
		paths = []string{"/opt/homebrew/bin/gh", "/usr/local/bin/gh"}
	default:
		paths = []string{"/usr/bin/gh", "/usr/local/bin/gh", "/snap/bin/gh"}
		if home, err := os.UserHomeDir(); err == nil {
			paths = append(paths, filepath.Join(home, ".local", "bin", "gh"))
		}
	}
	return paths
}

// ghPath 先找 PATH，再找常见安装目录
func ghPath() (string, bool) {
	if p, err := exec.LookPath("gh"); err == nil {
		return p, true
	}
	for _, p := range ghFallbackPaths() {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, true
		}
	}
	return "", false
}

// ghVersion 读 `gh --version` 里的版本号；读不到就返回空串，不影响使用
func ghVersion(path string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		return ""
	}
	fields := strings.Fields(string(out))
	if len(fields) >= 3 && fields[1] == "version" {
		return fields[2]
	}
	return ""
}

// ghStatus 是同步页要知道的两件事：装没装、登录没登录
type ghStatus struct {
	Found    bool
	Path     string
	Version  string
	LoggedIn bool
}

// inspectGH 看一眼本机的 gh：找得到就顺便试一下能不能拿到 token（只看成败，不留 token）
func inspectGH() ghStatus {
	path, ok := ghPath()
	if !ok {
		return ghStatus{}
	}
	st := ghStatus{Found: true, Path: path, Version: ghVersion(path)}
	if token, err := syncGHToken(); err == nil && token != "" {
		st.LoggedIn = true
	}
	return st
}

// ghInstallPlan 是「安装 GitHub CLI」这一步跑什么：有包管理器就直接装，
// 没有就把 cmd 留空，让页面改成打开官方下载页
type ghInstallPlan struct {
	label string
	desc  string
	cmd   *exec.Cmd
}

// ghInstaller 留成变量，测试里换成不真的装东西的命令
var ghInstaller = planGHInstall

// planGHInstall 按平台挑一个现成的包管理器；sudo 用来装系统包，
// tea.ExecProcess 会把终端交回去，密码提示能正常显示
func planGHInstall() ghInstallPlan {
	switch runtime.GOOS {
	case "windows":
		if _, err := exec.LookPath("winget"); err == nil {
			return ghInstallPlan{"安装 GitHub CLI", "用 winget 装，装完回来自动接着登录",
				exec.Command("winget", "install", "--id", "GitHub.cli", "--exact",
					"--source", "winget", "--accept-package-agreements", "--accept-source-agreements")}
		}
		if _, err := exec.LookPath("scoop"); err == nil {
			return ghInstallPlan{"安装 GitHub CLI", "用 scoop 装，装完回来自动接着登录",
				exec.Command("scoop", "install", "gh")}
		}
		if _, err := exec.LookPath("choco"); err == nil {
			return ghInstallPlan{"安装 GitHub CLI", "用 choco 装，装完回来自动接着登录",
				exec.Command("choco", "install", "gh", "-y")}
		}
	case "darwin":
		if _, err := exec.LookPath("brew"); err == nil {
			return ghInstallPlan{"安装 GitHub CLI", "用 Homebrew 装，装完回来自动接着登录",
				exec.Command("brew", "install", "gh")}
		}
	default:
		switch {
		case hasCmd("apt-get"):
			return ghInstallPlan{"安装 GitHub CLI", "用 apt 装（会要 sudo 密码）",
				exec.Command("sudo", "apt-get", "install", "-y", "gh")}
		case hasCmd("dnf"):
			return ghInstallPlan{"安装 GitHub CLI", "用 dnf 装（会要 sudo 密码）",
				exec.Command("sudo", "dnf", "install", "-y", "gh")}
		case hasCmd("pacman"):
			return ghInstallPlan{"安装 GitHub CLI", "用 pacman 装（会要 sudo 密码）",
				exec.Command("sudo", "pacman", "-S", "--noconfirm", "github-cli")}
		}
	}
	return ghInstallPlan{"打开 GitHub CLI 下载页", "没找到能自动安装的包管理器；装好后按 r 重新检测", nil}
}

func hasCmd(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// openURLCommand 用系统默认浏览器打开一个网址
func openURLCommand(rawURL string) *exec.Cmd {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("cmd", "/c", "start", "", rawURL)
	case "darwin":
		return exec.Command("open", rawURL)
	}
	return exec.Command("xdg-open", rawURL)
}

func newGHClient() (*ghClient, error) {
	token, err := syncGHToken()
	if err != nil {
		return nil, err
	}
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("HMCK_GITHUB_API")), "/")
	if base == "" {
		base = githubAPIBase
	}
	return &ghClient{
		base:  base,
		token: token,
		http:  &http.Client{Timeout: syncHTTPTimeout},
	}, nil
}

// ghAPIError 带上 HTTP 状态和 GitHub 的原话，方便判断是登录、权限还是网络。
type ghAPIError struct {
	Status int
	Msg    string
}

func (e *ghAPIError) Error() string {
	if e.Msg == "" {
		return fmt.Sprintf("GitHub 返回 HTTP %d", e.Status)
	}
	return e.Msg
}

func (c *ghClient) do(method, path string, body any) ([]byte, int, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, c.base+path, reader)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "hmck/"+version)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("连不上 GitHub: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("读 GitHub 响应失败: %w", err)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return data, resp.StatusCode, nil
	}
	return data, resp.StatusCode, c.apiError(resp.StatusCode, data)
}

func (c *ghClient) apiError(status int, data []byte) error {
	var payload struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(data, &payload)
	msg := strings.TrimSpace(payload.Message)
	switch status {
	case http.StatusUnauthorized:
		return &ghAPIError{Status: status, Msg: "GitHub 不认 gh 的 token；运行 gh auth login 重新登录"}
	case http.StatusForbidden:
		if msg == "" {
			msg = "没有权限（token 可能缺 repo scope，或触发了限流）"
		}
		return &ghAPIError{Status: status, Msg: "GitHub 拒绝：" + msg}
	case http.StatusNotFound:
		return &ghAPIError{Status: status, Msg: "GitHub 找不到这个仓库或文件（可能没权限）"}
	case http.StatusConflict, http.StatusUnprocessableEntity:
		return &ghAPIError{Status: status, Msg: "GitHub 写入被拒绝：" + msg + "（可能别的设备刚推过，先 pull）"}
	default:
		if msg == "" {
			msg = "请求失败"
		}
		return &ghAPIError{Status: status, Msg: fmt.Sprintf("GitHub 返回 HTTP %d：%s", status, msg)}
	}
}

func (c *ghClient) login() (string, error) {
	data, _, err := c.do(http.MethodGet, "/user", nil)
	if err != nil {
		return "", err
	}
	var u struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(data, &u); err != nil || u.Login == "" {
		return "", errors.New("GitHub 没返回用户名；gh 的登录可能过期了")
	}
	return u.Login, nil
}

func splitSyncRepo(full string) (owner, name string, err error) {
	parts := strings.SplitN(strings.TrimSpace(full), "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("仓库要写成 owner/name：%q", full)
	}
	return parts[0], parts[1], nil
}

// repoInfo 返回仓库在不在、是不是私有的。仓库不存在时 exists=false。
func (c *ghClient) repoInfo(full string) (exists, private bool, err error) {
	owner, name, err := splitSyncRepo(full)
	if err != nil {
		return false, false, err
	}
	data, status, err := c.do(http.MethodGet, "/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name), nil)
	if status == http.StatusNotFound {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	var repo struct {
		Private bool `json:"private"`
	}
	if err := json.Unmarshal(data, &repo); err != nil {
		return false, false, err
	}
	return true, repo.Private, nil
}

// createRepo 在用户自己名下建一个私有仓库；auto_init 让默认分支立刻存在，
// 否则第一次 PUT 文件会因为仓库是空的而失败。
func (c *ghClient) createRepo(name string) error {
	body := map[string]any{
		"name":        name,
		"private":     true,
		"auto_init":   true,
		"description": "hmck 多端同步（配置已加密）",
	}
	_, status, err := c.do(http.MethodPost, "/user/repos", body)
	if err != nil {
		var ge *ghAPIError
		if errors.As(err, &ge) && ge.Status == http.StatusUnprocessableEntity {
			return fmt.Errorf("建不了仓库 %s（可能同名仓库已经存在）: %w", name, err)
		}
		return err
	}
	_ = status
	return nil
}

// getFile 读仓库里的一个文件。不存在时返回 (nil, "", nil)，让调用方决定是新建还是冲突。
func (c *ghClient) getFile(full, file string) (data []byte, sha string, err error) {
	owner, name, err := splitSyncRepo(full)
	if err != nil {
		return nil, "", err
	}
	path := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name) + "/contents/" + url.PathEscape(file)
	raw, status, err := c.do(http.MethodGet, path, nil)
	if status == http.StatusNotFound {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	var res struct {
		Content string `json:"content"`
		SHA     string `json:"sha"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, "", fmt.Errorf("GitHub 返回的文件格式不对: %w", err)
	}
	data, err = base64.StdEncoding.DecodeString(strings.TrimSpace(res.Content))
	if err != nil {
		return nil, "", fmt.Errorf("GitHub 返回的文件不是合法 base64: %w", err)
	}
	return data, res.SHA, nil
}

// putFile 写文件。sha 非空表示更新已有文件，GitHub 会拿它做乐观锁：
// 远端在读取之后又被改过就会 409，正好用来防两台机器互相覆盖。
func (c *ghClient) putFile(full, file string, data []byte, sha, message string) (string, error) {
	owner, name, err := splitSyncRepo(full)
	if err != nil {
		return "", err
	}
	body := map[string]any{
		"message": message,
		"content": base64.StdEncoding.EncodeToString(data),
	}
	if sha != "" {
		body["sha"] = sha
	}
	path := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name) + "/contents/" + url.PathEscape(file)
	raw, _, err := c.do(http.MethodPut, path, body)
	if err != nil {
		return "", err
	}
	var res struct {
		Content struct {
			SHA string `json:"sha"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &res); err != nil || res.Content.SHA == "" {
		return "", errors.New("GitHub 写成功了，但没返回新版本号；再运行一次 hmck sync status 确认")
	}
	return res.Content.SHA, nil
}
