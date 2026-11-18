package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestIsNewer(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"0.2.0", "0.1.9", true},
		{"0.1.10", "0.1.9", true},
		{"v1.0.0", "0.9.9", true},
		{"0.1.0", "0.1.0", false},
		{"0.1.0", "0.1.1", false},
	}
	for _, c := range cases {
		if got := isNewer(c.a, c.b); got != c.want {
			t.Errorf("isNewer(%q,%q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestAssetNamesAndSupport(t *testing.T) {
	var npmTool tool
	for _, t0 := range registry {
		if t0.Key == "npm-token" {
			npmTool = t0
		}
	}
	if got := assetName(npmTool, "darwin", "arm64"); got != "npm-token_darwin_arm64" {
		t.Errorf("asset = %q", got)
	}
	if got := assetName(npmTool, "windows", "amd64"); got != "npm-token_windows_amd64.exe" {
		t.Errorf("windows asset = %q", got)
	}
	if got := binaryName(npmTool, "windows"); got != "npm-token.exe" {
		t.Errorf("binary = %q", got)
	}
	if !supports(npmTool, "windows", "arm64") {
		t.Error("npm-token 应当支持 windows/arm64")
	}

	var kube tool
	for _, t0 := range registry {
		if t0.Key == "kube-token" {
			kube = t0
		}
	}
	if !supports(kube, "windows", "arm64") {
		t.Error("kube-token 应当支持 windows/arm64")
	}
	if !kube.Checksums {
		t.Error("kube-token 发布带 checksums.txt，应当开启校验")
	}
}

func TestSelfIsManaged(t *testing.T) {
	tl, ok := toolByKey("hmck")
	if !ok || !tl.Self {
		t.Fatalf("hmck 应当作为自己出现在工具列表里: %+v, %v", tl, ok)
	}
	if code := run([]string{"run", "hmck"}); code != 2 {
		t.Errorf("hmck run hmck 应当拒绝，返回 %d", code)
	}
}

func TestFetchToolReusesCache(t *testing.T) {
	payload := bytes.Repeat([]byte("BINARY"), 40_000)
	srv := fakeReleaseServer(t, payload, "")
	defer srv.Close()

	old := releaseAssetURL
	releaseAssetURL = func(repo, tag, asset string) string { return srv.URL + "/download/" + tag + "/" + asset }
	defer func() { releaseAssetURL = old }()

	tl := tool{Key: "cachefake", Repo: "o/r", Binary: "cachefake",
		Platforms: []string{runtime.GOOS + "/" + runtime.GOARCH}}
	st := toolState{Tool: tl, Latest: "1.2.3"}
	p1, v1, err := fetchTool(st)
	if err != nil {
		t.Fatal(err)
	}
	p2, v2, err := fetchTool(st)
	if err != nil {
		t.Fatal(err)
	}
	if p1 != p2 || v1 != "1.2.3" || v2 != v1 {
		t.Errorf("第二次应当复用缓存: %q/%q vs %q/%q", p1, v1, p2, v2)
	}
	if got, _ := os.ReadFile(p2); !bytes.Equal(got, payload) {
		t.Error("缓存内容不对")
	}
}

func TestToolByKey(t *testing.T) {
	for _, key := range []string{"npm-token", "npM-token", "npm"} {
		if got, ok := toolByKey(key); !ok || got.Key != "npm-token" {
			t.Errorf("toolByKey(%q) = %v, %v", key, got.Key, ok)
		}
	}
	if _, ok := toolByKey("nope"); ok {
		t.Error("不该认识 nope")
	}
}

func TestLatestTagRedirectAndFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases/latest":
			w.Header().Set("Location", "/o/r/releases/tag/v9.9.9")
			w.WriteHeader(http.StatusFound)
		case "/notfound":
			w.WriteHeader(http.StatusNotFound)
		case "/api/releases/latest":
			fmt.Fprint(w, `{"tag_name":"v8.8.8"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	oldPage, oldAPI := latestPageURL, releaseAPIURL
	defer func() { latestPageURL, releaseAPIURL = oldPage, oldAPI }()

	latestPageURL = func(string) string { return srv.URL + "/releases/latest" }
	if tag, err := latestTag("o/r"); err != nil || tag != "v9.9.9" {
		t.Fatalf("redirect: tag=%q err=%v", tag, err)
	}

	latestPageURL = func(string) string { return srv.URL + "/notfound" }
	releaseAPIURL = func(string) string { return srv.URL + "/api/releases/latest" }
	if tag, err := latestTag("o/r"); err != nil || tag != "v8.8.8" {
		t.Fatalf("api fallback: tag=%q err=%v", tag, err)
	}
}

func fakeReleaseServer(t *testing.T, payload []byte, checksum string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/checksums.txt"):
			fmt.Fprintf(w, "%s  %s\n", checksum, "fake_"+runtime.GOOS+"_"+runtime.GOARCH)
		case strings.Contains(r.URL.Path, "/download/"):
			w.Write(payload)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestInstallToolWithChecksum(t *testing.T) {
	payload := bytes.Repeat([]byte("BINARY"), 40_000) // > 100KB
	sum := sha256.Sum256(payload)
	srv := fakeReleaseServer(t, payload, hex.EncodeToString(sum[:]))
	defer srv.Close()

	old := releaseAssetURL
	releaseAssetURL = func(repo, tag, asset string) string { return srv.URL + "/download/" + tag + "/" + asset }
	defer func() { releaseAssetURL = old }()

	tl := tool{Key: "fake", Repo: "o/r", Binary: "fake",
		Platforms: []string{runtime.GOOS + "/" + runtime.GOARCH}, Checksums: true}
	dir := t.TempDir()
	target := filepath.Join(dir, binaryName(tl, runtime.GOOS))
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := installTool(toolState{Tool: tl, Path: target, Installed: "0.0.1", Latest: "9.9.9"})
	if err != nil {
		t.Fatalf("installTool: %v", err)
	}
	if got != "9.9.9" {
		t.Errorf("version = %q", got)
	}
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(content, payload) {
		t.Errorf("文件内容没被替换，长度 %d", len(content))
	}
	st, _ := os.Stat(target)
	if st.Mode().Perm()&0o100 == 0 {
		t.Errorf("缺少可执行位: %v", st.Mode())
	}
}

func TestInstallToolChecksumMismatch(t *testing.T) {
	payload := bytes.Repeat([]byte("BINARY"), 40_000)
	srv := fakeReleaseServer(t, payload, strings.Repeat("0", 64))
	defer srv.Close()

	old := releaseAssetURL
	releaseAssetURL = func(repo, tag, asset string) string { return srv.URL + "/download/" + tag + "/" + asset }
	defer func() { releaseAssetURL = old }()

	tl := tool{Key: "fake", Repo: "o/r", Binary: "fake",
		Platforms: []string{runtime.GOOS + "/" + runtime.GOARCH}, Checksums: true}
	dir := t.TempDir()
	target := filepath.Join(dir, binaryName(tl, runtime.GOOS))
	os.WriteFile(target, []byte("old"), 0o755)

	// 用不同的版本号，避免复用到上一个用例下载好的缓存
	if _, err := installTool(toolState{Tool: tl, Path: target, Latest: "9.9.8"}); err == nil {
		t.Fatal("校验不匹配却成功了")
	}
	content, _ := os.ReadFile(target)
	if string(content) != "old" {
		t.Errorf("校验失败时不该动原文件: %q", content)
	}
}

func TestInstallToolUnsupportedPlatform(t *testing.T) {
	tl := tool{Key: "fake", Repo: "o/r", Binary: "fake", Platforms: []string{"plan9/mips"}}
	if _, err := installTool(toolState{Tool: tl, Latest: "1.0.0"}); err == nil {
		t.Error("不支持的平台应当报错")
	}
}

func TestUpgradeAllCmd(t *testing.T) {
	payload := bytes.Repeat([]byte("BINARY"), 40_000)
	srv := fakeReleaseServer(t, payload, "")
	defer srv.Close()

	old := releaseAssetURL
	releaseAssetURL = func(repo, tag, asset string) string { return srv.URL + "/download/" + tag + "/" + asset }
	defer func() { releaseAssetURL = old }()

	dir := t.TempDir()
	mk := func(key string) toolState {
		tl := tool{Key: key, Repo: "o/r", Binary: key,
			Platforms: []string{runtime.GOOS + "/" + runtime.GOARCH}}
		target := filepath.Join(dir, binaryName(tl, runtime.GOOS))
		if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
			t.Fatal(err)
		}
		return toolState{Tool: tl, Path: target, Installed: "0.0.0", Latest: "9.9.9"}
	}
	states := []toolState{mk("tool-a"), mk("tool-b")}
	states[0].Installed, states[1].Installed = "0.0.1", "0.0.2"

	ch := make(chan tea.Msg, 16)
	type doneResult struct{ msg tea.Msg }
	done := make(chan doneResult, 1)
	go func() { done <- doneResult{upgradeAllCmd(states, ch, 7)()} }()

	var progress []batchProgressMsg
	for msg := range ch {
		if p, ok := msg.(batchProgressMsg); ok && p.gen == 7 {
			progress = append(progress, p)
		}
	}
	res := (<-done).msg
	batch, ok := res.(batchDoneMsg)
	if !ok {
		t.Fatalf("最后一条消息不是 batchDoneMsg: %T", res)
	}
	if batch.gen != 7 || len(batch.results) != 2 {
		t.Fatalf("batch = %+v", batch)
	}
	if len(progress) != 2 || progress[0].index != 1 || progress[len(progress)-1].key != "tool-b" {
		t.Errorf("进度消息不对: %+v", progress)
	}
	for i, r := range batch.results {
		if r.err != nil {
			t.Fatalf("%s 升级失败: %v", r.key, r.err)
		}
		if r.from != states[i].Installed || r.to != "9.9.9" {
			t.Errorf("%s 结果 = %s → %s", r.key, r.from, r.to)
		}
		got, err := os.ReadFile(states[i].Path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, payload) {
			t.Errorf("%s 没被替换成新二进制", r.key)
		}
	}
}

func TestBatchDoneUpdatesModel(t *testing.T) {
	m := newTUIModel()
	m.state = working
	m.updGen = 3
	m.states = []toolState{
		{Tool: tool{Key: "tool-a"}, Installed: "0.1.0", Latest: "0.2.0"},
		{Tool: tool{Key: "tool-b"}, Installed: "0.1.0", Latest: "0.1.0"},
	}

	// 老一代的消息必须忽略
	updated, _ := m.Update(batchDoneMsg{gen: 2, results: []installResult{{key: "tool-a", to: "0.2.0"}}})
	if got := updated.(tuiModel); got.state != working || got.states[0].Installed != "0.1.0" {
		t.Fatalf("旧消息被采纳了: state=%v ver=%s", got.state, got.states[0].Installed)
	}

	updated, _ = m.Update(batchDoneMsg{gen: 3, results: []installResult{
		{key: "tool-a", from: "0.1.0", to: "0.2.0"},
		{key: "tool-b", from: "0.1.0", to: "0.1.0"},
	}})
	got := updated.(tuiModel)
	if got.state != result || len(got.resultLines) != 2 {
		t.Fatalf("state=%v lines=%+v", got.state, got.resultLines)
	}
	if got.states[0].Installed != "0.2.0" || got.states[0].Behind() {
		t.Errorf("tool-a 版本没更新: %+v", got.states[0])
	}
	if !strings.Contains(got.resultLines[0].text, "0.1.0 → 0.2.0") || !got.resultLines[0].ok {
		t.Errorf("结果行 = %+v", got.resultLines[0])
	}
	if !got.resultLines[1].ok || !strings.Contains(got.resultLines[1].text, "已是最新") {
		t.Errorf("结果行 = %+v", got.resultLines[1])
	}
}

func TestVersionText(t *testing.T) {
	cases := []struct {
		st   toolState
		want string
	}{
		{toolState{Installed: "0.4.2", Latest: "0.4.2"}, "0.4.2 · 最新 0.4.2"},
		{toolState{Installed: "0.4.2", Latest: "0.4.3"}, "0.4.2 → 0.4.3"},
		{toolState{Latest: "0.1.0"}, "未安装 · 最新 0.1.0"},
		{toolState{Installed: "0.1.0"}, "已装 0.1.0"},
	}
	for _, c := range cases {
		if got := versionText(c.st); got != c.want {
			t.Errorf("versionText(%+v) = %q, want %q", c.st, got, c.want)
		}
	}
}

func TestDownloadDoneStages(t *testing.T) {
	m := newTUIModel()
	m.state = working
	m.updGen = 1
	m.states = []toolState{
		{Tool: tool{Key: "tool-a"}, Installed: "0.1.0", Latest: "0.2.0"},
	}
	updated, _ := m.Update(downloadDoneMsg{gen: 1, results: []downloadResult{{key: "tool-a", ver: "0.2.0"}}})
	got := updated.(tuiModel)
	if got.state != result {
		t.Errorf("state = %v", got.state)
	}
	if got.staged["tool-a"] != "0.2.0" {
		t.Errorf("staged = %+v", got.staged)
	}
	if len(got.resultLines) != 1 || !strings.Contains(got.resultLines[0].text, "已下载") {
		t.Errorf("结果行 = %+v", got.resultLines)
	}
}
