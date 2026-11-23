package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
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

	// GitLab：permalink/latest 会 307 到最新 tag，资产挂在项目的 releases 下
	gitlabLatestURL = func(repo string) string {
		return "https://gitlab.com/api/v4/projects/" + url.PathEscape(repo) + "/releases/permalink/latest"
	}
	gitlabAssetURL = func(repo, tag, asset string) string {
		return fmt.Sprintf("https://gitlab.com/%s/-/releases/%s/downloads/%s", repo, tag, asset)
	}
	gitlabReleasesAPIURL = func(repo string) string {
		return "https://gitlab.com/api/v4/projects/" + url.PathEscape(repo) + "/releases?per_page=1"
	}

	// kubectl 走 dl.k8s.io：最新版是一行 stable.txt，资产按 平台/架构 分目录，
	// 校验值是资产旁边同名的 .sha256（里面只有一行哈希）
	kubernetesStableURL = func() string {
		return "https://dl.k8s.io/release/stable.txt"
	}
	kubernetesAssetURL = func(tag, goos, goarch, asset string) string {
		return fmt.Sprintf("https://dl.k8s.io/release/%s/bin/%s/%s/%s", tag, goos, goarch, asset)
	}
)

var (
	apiClient      = &http.Client{Timeout: 20 * time.Second}
	downloadClient = &http.Client{Timeout: 5 * time.Minute}
)

// latestTag 先看 releases/latest 的跳转（不吃 API 限流），失败再问 API
func latestTag(t tool) (string, error) {
	if t.releaseHost() == "kubernetes" {
		return kubernetesLatestTag()
	}
	if t.releaseHost() == "gitlab" {
		if tag, err := gitlabLatestTagViaRedirect(t.Repo); err == nil {
			return tag, nil
		}
		return gitlabLatestTagViaAPI(t.Repo)
	}
	if tag, err := latestTagViaRedirect(t.Repo); err == nil {
		return tag, nil
	}
	return latestTagViaAPI(t.Repo)
}

func latestTagViaRedirect(repo string) (string, error) {
	return redirectTag(latestPageURL(repo))
}

func gitlabLatestTagViaRedirect(repo string) (string, error) {
	return redirectTag(gitlabLatestURL(repo))
}

// redirectTag 拿到「最新版本」跳转的 Location，从路径末尾取 tag。
// GitHub 用 302，GitLab 用 307，都按同一套解析。
func redirectTag(pageURL string) (string, error) {
	client := &http.Client{
		Timeout: 15 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, err := http.NewRequest(http.MethodGet, pageURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "hmck/"+version)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
	default:
		return "", fmt.Errorf("按最新版本跳转失败: %s", resp.Status)
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

// gitlabLatestTagViaAPI 退到 projects/<path>/releases?per_page=1，取第一条的 tag_name
func gitlabLatestTagViaAPI(repo string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, gitlabReleasesAPIURL(repo), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "hmck/"+version)
	resp, err := apiClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests:
		return "", errors.New("GitLab 拒绝了请求（私有仓库或限流），稍后再试")
	case http.StatusNotFound:
		return "", fmt.Errorf("%s 在 GitLab 上不存在或没有发布", repo)
	default:
		return "", fmt.Errorf("GitLab 返回 %s", resp.Status)
	}
	var rels []struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rels); err != nil {
		return "", err
	}
	if len(rels) == 0 || rels[0].TagName == "" {
		return "", fmt.Errorf("%s 还没有发布 release", repo)
	}
	return rels[0].TagName, nil
}

// kubernetesLatestTag 读 dl.k8s.io/release/stable.txt：一把 vX.Y.Z，指当前稳定版
func kubernetesLatestTag() (string, error) {
	req, err := http.NewRequest(http.MethodGet, kubernetesStableURL(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "hmck/"+version)
	resp, err := apiClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("读 stable.txt 失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("dl.k8s.io 返回 %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<10))
	if err != nil {
		return "", err
	}
	tag := strings.TrimSpace(string(data))
	if !strings.HasPrefix(tag, "v") || strings.ContainsAny(tag, " \t\n/") {
		return "", fmt.Errorf("stable.txt 里不是版本号: %q", tag)
	}
	return tag, nil
}

// releaseAssetURLFor 按工具的发布平台拼资产地址
func releaseAssetURLFor(t tool, tag, asset string) string {
	switch t.releaseHost() {
	case "gitlab":
		return gitlabAssetURL(t.Repo, tag, asset)
	case "kubernetes":
		return kubernetesAssetURL(tag, runtime.GOOS, runtime.GOARCH, asset)
	}
	return releaseAssetURL(t.Repo, tag, asset)
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

// progressFunc 报告一次下载的进度：已经下了多少字节、一共多少。
// 对方没给 Content-Length 时 total 为 0，调用方只能说「已下载多少」。
// 从下载函数里同步调用，可能被调得很频繁，实现要能随手丢弃多余的帧。
type progressFunc func(done, total int64)

// progressWriter 包住落盘的目标文件，边写边报进度。
// 一次 20MB 的下载有几百次 Write，调用方要画界面，按 100ms 节流，
// 不然消息比帧还多，TUI 来不及画
type progressWriter struct {
	w     io.Writer
	on    progressFunc
	done  int64
	total int64
	last  time.Time
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.done += int64(n)
	if p.on != nil {
		if now := time.Now(); p.last.IsZero() || now.Sub(p.last) >= 100*time.Millisecond || err != nil {
			p.last = now
			p.on(p.done, p.total)
		}
	}
	return n, err
}

// fetchTool 下载并校验到一个版本对应的缓存文件；已经下过就直接复用。
// 返回缓存路径和版本号。on 收下载进度，可以是 nil。
func fetchTool(st toolState, on progressFunc) (string, string, error) {
	t := st.Tool
	if !supports(t, runtime.GOOS, runtime.GOARCH) {
		return "", "", fmt.Errorf("%s 没有 %s/%s 的发布", t.Key, runtime.GOOS, runtime.GOARCH)
	}

	tag := st.Latest
	if tag == "" {
		var err error
		if tag, err = latestTag(t); err != nil {
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
	asset := assetName(t, runtime.GOOS, runtime.GOARCH, ver)
	tmp, err := downloadAsset(releaseAssetURLFor(t, tag, asset), dir, on)
	if err != nil {
		return "", "", err
	}
	if t.Checksums {
		// 校验的是下载到的那个资产本身：压缩包就校验压缩包
		if err := verifyChecksum(t, tag, asset, tmp); err != nil {
			os.Remove(tmp)
			return "", "", err
		}
	}
	if t.Archive {
		extracted, err := extractBinary(t, tmp, dir)
		os.Remove(tmp)
		if err != nil {
			return "", "", err
		}
		tmp = extracted
	}
	_ = os.Remove(cached) // Windows 上 rename 不能覆盖已存在的文件
	if err := os.Rename(tmp, cached); err != nil {
		os.Remove(tmp)
		return "", "", err
	}
	return cached, ver, nil
}

// installTool 下载并替换成最新版；返回装好的版本号。on 收下载进度，可以是 nil
func installTool(st toolState, on progressFunc) (string, error) {
	t := st.Tool
	target := st.Path
	if target == "" {
		dir, err := installDir()
		if err != nil {
			return "", err
		}
		target = filepath.Join(dir, binaryName(t, runtime.GOOS))
	}
	if t.Self && isLegacyPath(target) {
		// 自己刚从旧目录搬过来时，升级直接写到统一目录，别再回到旧的
		if dir, err := defaultBinDir(); err == nil {
			if err := os.MkdirAll(dir, 0o755); err == nil {
				target = filepath.Join(dir, binaryName(t, runtime.GOOS))
			}
		}
	}
	dir := filepath.Dir(target)
	if !writable(dir) {
		return "", fmt.Errorf("%s 不可写（程序在运行？或用 sudo 重试）", dir)
	}

	cached, ver, err := fetchTool(st, on)
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
	recordUpdate(t.Key, ver) // 记下版本更新时间，TUI 里显示成「3 天前更新」
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

func downloadAsset(assetURL, dir string, on progressFunc) (string, error) {
	resp, err := downloadClient.Get(assetURL)
	if err != nil {
		return "", fmt.Errorf("下载失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载失败: %s（%s）", resp.Status, assetURL)
	}

	// 头一到就知道大小，先报一帧 0%，进度条能立刻把总长画出来
	total := resp.ContentLength
	if total < 0 {
		total = 0
	}
	if on != nil {
		on(0, total)
	}

	tmp, err := os.CreateTemp(dir, ".hmck-dl-*")
	if err != nil {
		return "", err
	}
	name := tmp.Name()
	pw := &progressWriter{w: tmp, on: on, total: total}
	n, err := io.Copy(pw, resp.Body)
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
	if on != nil {
		on(n, total) // 收尾一帧，进度条画到满
	}
	return name, nil
}

func verifyChecksum(t tool, tag, asset, file string) error {
	var want string
	if t.releaseHost() == "kubernetes" {
		// kubectl 没有 checksums.txt：每个资产旁边一个 .sha256，里面只有一行哈希
		resp, err := downloadClient.Get(releaseAssetURLFor(t, tag, asset) + ".sha256")
		if err != nil {
			return fmt.Errorf("下载 %s.sha256 失败: %w", asset, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("下载 %s.sha256 失败: %s", asset, resp.Status)
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<10))
		if err != nil {
			return err
		}
		if fields := strings.Fields(string(data)); len(fields) > 0 {
			want = strings.ToLower(fields[0])
		}
		if want == "" {
			return fmt.Errorf("%s.sha256 里没有哈希", asset)
		}
	} else {
		resp, err := downloadClient.Get(releaseAssetURLFor(t, tag, "checksums.txt"))
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
// errEntryMissing 表示压缩包里没有要找的文件，换下一个候选名再试
var errEntryMissing = errors.New("压缩包里没有这个文件")

// extractBinary 从 .tar.gz / .zip 里取出二进制，落到缓存目录的临时文件。
// 先按 BinPath（默认 bin/<binary>）精确找，再退到任意同名文件。
func extractBinary(t tool, archivePath, dir string) (string, error) {
	wanted := t.BinPath
	if wanted == "" {
		wanted = "bin/" + binaryName(t, runtime.GOOS)
	}
	for _, name := range []string{wanted, binaryName(t, runtime.GOOS)} {
		out, err := extractEntry(archivePath, dir, name)
		if err == nil {
			return out, nil
		}
		if !errors.Is(err, errEntryMissing) {
			return "", err
		}
	}
	return "", fmt.Errorf("%s 里没找到 %s", filepath.Base(archivePath), wanted)
}

// extractEntry 按文件头判断格式——下载下来的临时文件没有扩展名
func extractEntry(archivePath, dir, name string) (string, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return "", err
	}
	head := make([]byte, 4)
	n, _ := io.ReadFull(f, head)
	f.Close()
	switch {
	case n >= 2 && head[0] == 0x1f && head[1] == 0x8b:
		return extractEntryTarGz(archivePath, dir, name)
	case bytes.HasPrefix(head[:n], []byte("PK\x03\x04")):
		return extractEntryZip(archivePath, dir, name)
	}
	return "", fmt.Errorf("%s 不是 tar.gz 也不是 zip", filepath.Base(archivePath))
}

// entryMatches：带目录的按完整路径比，只有文件名的按 basename 比
func entryMatches(entry, name string) bool {
	if strings.Contains(name, "/") {
		return entry == name
	}
	return path.Base(entry) == name
}

func extractEntryTarGz(archivePath, dir, name string) (string, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", fmt.Errorf("解压 %s 失败: %w", filepath.Base(archivePath), err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return "", errEntryMissing
		}
		if err != nil {
			return "", fmt.Errorf("解压 %s 失败: %w", filepath.Base(archivePath), err)
		}
		if hdr.Typeflag != tar.TypeReg || !entryMatches(hdr.Name, name) {
			continue
		}
		return writeExtracted(tr, dir)
	}
}

func extractEntryZip(archivePath, dir, name string) (string, error) {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return "", fmt.Errorf("解压 %s 失败: %w", filepath.Base(archivePath), err)
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || !entryMatches(f.Name, name) {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return "", err
		}
		out, err := writeExtracted(rc, dir)
		rc.Close()
		return out, err
	}
	return "", errEntryMissing
}

func writeExtracted(src io.Reader, dir string) (string, error) {
	out, err := os.CreateTemp(dir, ".hmck-x-*")
	if err != nil {
		return "", err
	}
	name := out.Name()
	if _, err := io.Copy(out, src); err != nil {
		out.Close()
		os.Remove(name)
		return "", err
	}
	if err := out.Close(); err != nil {
		os.Remove(name)
		return "", err
	}
	if err := os.Chmod(name, 0o755); err != nil {
		os.Remove(name)
		return "", err
	}
	return name, nil
}

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
	cmd.Env = append(os.Environ(), "HMCK_NO_MIGRATE=1") // 迁移留到下次用户自己启动时
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	_ = cmd.Run() // 失败不影响升级结果
}
