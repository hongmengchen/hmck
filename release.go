package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// 测试里会换成 httptest 地址
var (
	latestPageURL = func(repo string) string {
		return "https://github.com/" + repo + "/releases/latest"
	}
	releaseAssetURL = func(repo, tag, asset string) string {
		return fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", repo, tag, asset)
	}
	releaseAPIURL = func(repo string) string {
		return "https://api.github.com/repos/" + repo + "/releases/latest"
	}
)

var (
	apiClient      = &http.Client{Timeout: 20 * time.Second}
	downloadClient = &http.Client{Timeout: 5 * time.Minute}
)

// latestTag 先看 releases/latest 的跳转（不吃 API 限流），失败再问 API
func latestTag(repo string) (string, error) {
	if tag, err := latestTagViaRedirect(repo); err == nil {
		return tag, nil
	}
	return latestTagViaAPI(repo)
}

func latestTagViaRedirect(repo string) (string, error) {
	client := &http.Client{
		Timeout: 15 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, err := http.NewRequest(http.MethodGet, latestPageURL(repo), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "hmck/"+version)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound && resp.StatusCode != http.StatusMovedPermanently {
		return "", fmt.Errorf("releases/latest 返回 %s", resp.Status)
	}
	loc, err := resp.Location()
	if err != nil {
		return "", err
	}
	tag := strings.TrimSpace(path.Base(strings.TrimRight(loc.Path, "/")))
	if !strings.HasPrefix(tag, "v") {
		return "", fmt.Errorf("没能从跳转里解析出版本号: %s", loc)
	}
	return tag, nil
}

func latestTagViaAPI(repo string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, releaseAPIURL(repo), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "hmck/"+version)
	resp, err := apiClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusForbidden, http.StatusTooManyRequests:
		return "", errors.New("GitHub API 限流了，稍后再试")
	default:
		return "", fmt.Errorf("GitHub 返回 %s", resp.Status)
	}
	var rel struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return "", err
	}
	if rel.TagName == "" {
		return "", errors.New("没有解析到版本号")
	}
	return rel.TagName, nil
}

// releaseInfo 是一次 release 查询的结果，用来在 `hmck add` 时推断平台和校验文件
type releaseInfo struct {
	Tag    string
	Assets []string
}

func fetchReleaseInfo(repo string) (releaseInfo, error) {
	req, err := http.NewRequest(http.MethodGet, releaseAPIURL(repo), nil)
	if err != nil {
		return releaseInfo{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "hmck/"+version)
	resp, err := apiClient.Do(req)
	if err != nil {
		return releaseInfo{}, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusForbidden, http.StatusTooManyRequests:
		return releaseInfo{}, errors.New("GitHub API 限流了，稍后再试")
	case http.StatusNotFound:
		return releaseInfo{}, fmt.Errorf("%s 还没有发布 release", repo)
	default:
		return releaseInfo{}, fmt.Errorf("GitHub 返回 %s", resp.Status)
	}

	var rel struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name string `json:"name"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return releaseInfo{}, err
	}
	info := releaseInfo{Tag: rel.TagName}
	for _, a := range rel.Assets {
		info.Assets = append(info.Assets, a.Name)
	}
	return info, nil
}

// userCacheDir 存“自动下载”下来的新版二进制；测试里替换
var userCacheDir = os.UserCacheDir

// downloadCacheDir 是预下载目录：先下好、校验好，安装时只做本地替换
func downloadCacheDir() (string, error) {
	root, err := userCacheDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, "hmck", "downloads")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

func cachedBinary(t tool, ver string) (string, error) {
	dir, err := downloadCacheDir()
	if err != nil {
		return "", err
	}
	name := fmt.Sprintf("%s_%s_%s_%s", t.Key, ver, runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(dir, name), nil
}

// fetchTool 下载并校验到一个版本对应的缓存文件；已经下过就直接复用。
// 返回缓存路径和版本号。
func fetchTool(st toolState) (string, string, error) {
	t := st.Tool
	if !supports(t, runtime.GOOS, runtime.GOARCH) {
		return "", "", fmt.Errorf("%s 没有 %s/%s 的发布", t.Key, runtime.GOOS, runtime.GOARCH)
	}

	tag := st.Latest
	if tag == "" {
		var err error
		if tag, err = latestTag(t.Repo); err != nil {
			return "", "", err
		}
	}
	if !strings.HasPrefix(tag, "v") {
		tag = "v" + tag
	}
	ver := strings.TrimPrefix(tag, "v")

	cached, err := cachedBinary(t, ver)
	if err != nil {
		return "", "", err
	}
	if fi, err := os.Stat(cached); err == nil && fi.Size() >= 100_000 {
		return cached, ver, nil
	}

	dir, err := downloadCacheDir()
	if err != nil {
		return "", "", err
	}
	asset := assetName(t, runtime.GOOS, runtime.GOARCH)
	tmp, err := downloadAsset(releaseAssetURL(t.Repo, tag, asset), dir)
	if err != nil {
		return "", "", err
	}
	if t.Checksums {
		if err := verifyChecksum(t.Repo, tag, asset, tmp); err != nil {
			os.Remove(tmp)
			return "", "", err
		}
	}
	if err := os.Rename(tmp, cached); err != nil {
		os.Remove(tmp)
		return "", "", err
	}
	return cached, ver, nil
}

// installTool 下载并替换成最新版；返回装好的版本号
func installTool(st toolState) (string, error) {
	t := st.Tool
	target := st.Path
	if target == "" {
		dir, err := installDir()
		if err != nil {
			return "", err
		}
		target = filepath.Join(dir, binaryName(t, runtime.GOOS))
	}
	dir := filepath.Dir(target)
	if !writable(dir) {
		return "", fmt.Errorf("%s 不可写（程序在运行？或用 sudo 重试）", dir)
	}

	cached, ver, err := fetchTool(st)
	if err != nil {
		return "", err
	}
	tmp, err := copyToTemp(cached, dir)
	if err != nil {
		return "", err
	}
	if err := installBinary(tmp, target); err != nil {
		os.Remove(tmp)
		return "", err
	}

	runPostInstall(t, target)
	return ver, nil
}

// copyToTemp 把缓存里的二进制复制到目标目录，再交给 installBinary 原子替换
func copyToTemp(src, dir string) (string, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	out, err := os.CreateTemp(dir, ".hmck-inst-*")
	if err != nil {
		return "", err
	}
	name := out.Name()
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(name)
		return "", err
	}
	if err := out.Close(); err != nil {
		os.Remove(name)
		return "", err
	}
	return name, nil
}

func downloadAsset(assetURL, dir string) (string, error) {
	resp, err := downloadClient.Get(assetURL)
	if err != nil {
		return "", fmt.Errorf("下载失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载失败: %s（%s）", resp.Status, assetURL)
	}

	tmp, err := os.CreateTemp(dir, ".hmck-dl-*")
	if err != nil {
		return "", err
	}
	name := tmp.Name()
	n, err := io.Copy(tmp, resp.Body)
	closeErr := tmp.Close()
	if err != nil || closeErr != nil {
		os.Remove(name)
		if err == nil {
			err = closeErr
		}
		return "", fmt.Errorf("下载中断: %w", err)
	}
	if n < 100_000 {
		os.Remove(name)
		return "", fmt.Errorf("只下载到 %d 字节，可能不是二进制", n)
	}
	return name, nil
}

func verifyChecksum(repo, tag, asset, file string) error {
	resp, err := downloadClient.Get(releaseAssetURL(repo, tag, "checksums.txt"))
	if err != nil {
		return fmt.Errorf("下载 checksums.txt 失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载 checksums.txt 失败: %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	want := ""
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && strings.TrimPrefix(fields[len(fields)-1], "*") == asset {
			want = strings.ToLower(fields[0])
			break
		}
	}
	if want == "" {
		return fmt.Errorf("checksums.txt 里没有 %s", asset)
	}

	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != want {
		return fmt.Errorf("校验失败: %s", asset)
	}
	return nil
}

// installBinary 同目录临时文件 + rename；Windows 上正被占用就先挪开旧的
func installBinary(tmpName, target string) error {
	if err := os.Chmod(tmpName, 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmpName, target); err == nil {
		return nil
	}
	old := target + ".old"
	_ = os.Remove(old)
	if err := os.Rename(target, old); err == nil {
		if err2 := os.Rename(tmpName, target); err2 == nil {
			_ = os.Remove(old)
			return nil
		}
		_ = os.Rename(old, target)
	}
	return fmt.Errorf("无法覆盖 %s（权限不足或程序正在运行）", target)
}

func runPostInstall(t tool, target string) {
	if len(t.PostInstall) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, target, t.PostInstall...)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	_ = cmd.Run() // 失败不影响升级结果
}
