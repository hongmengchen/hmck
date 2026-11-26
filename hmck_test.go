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
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

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

// argocd 的版本号带着构建信息，读出来要去掉 + 后面那段，否则装完还是显示可升级
func TestParseVersionOutput(t *testing.T) {
	cases := []struct{ out, want string }{
		{"argocd: v3.6.2+edd8eb6\n  BuildDate: 2026-11-01T00:00:00Z\n", "3.6.2"},
		{"glab 1.130.0 (9b430a11f)", "1.130.0"},
		{"kubectl version v1.37.2", "1.37.2"},
		{"", ""},
	}
	for _, c := range cases {
		if got := parseVersionOutput(c.out); got != c.want {
			t.Errorf("parseVersionOutput(%q) = %q，想要 %q", c.out, got, c.want)
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
	if got := assetName(npmTool, "darwin", "arm64", ""); got != "npm-token_darwin_arm64" {
		t.Errorf("asset = %q", got)
	}
	if got := assetName(npmTool, "windows", "amd64", ""); got != "npm-token_windows_amd64.exe" {
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
	p1, v1, err := fetchTool(st, nil)
	if err != nil {
		t.Fatal(err)
	}
	p2, v2, err := fetchTool(st, nil)
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

// 下载时得把进度报出来，TUI 和命令行据此画进度条：
// 至少要有开始和结束两帧，最后一帧正好是文件大小，中间不能倒退
func TestFetchToolReportsProgress(t *testing.T) {
	payload := bytes.Repeat([]byte("BINARY"), 40_000)
	srv := fakeReleaseServer(t, payload, "")
	defer srv.Close()

	old := releaseAssetURL
	releaseAssetURL = func(repo, tag, asset string) string { return srv.URL + "/download/" + tag + "/" + asset }
	defer func() { releaseAssetURL = old }()

	tl := tool{Key: "progfake", Repo: "o/r", Binary: "progfake",
		Platforms: []string{runtime.GOOS + "/" + runtime.GOARCH}}
	var reports []int64
	_, _, err := fetchTool(toolState{Tool: tl, Latest: "2.0.0"}, func(done, _ int64) {
		reports = append(reports, done)
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) < 2 {
		t.Fatalf("至少要报开始和结束两帧，实际 %v", reports)
	}
	if last := reports[len(reports)-1]; last != int64(len(payload)) {
		t.Errorf("最后一帧应当是文件大小 %d，实际 %d", len(payload), last)
	}
	for i := 1; i < len(reports); i++ {
		if reports[i] < reports[i-1] {
			t.Errorf("进度倒退了: %v", reports)
			break
		}
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
	if tag, err := latestTag(tool{Repo: "o/r"}); err != nil || tag != "v9.9.9" {
		t.Fatalf("redirect: tag=%q err=%v", tag, err)
	}

	latestPageURL = func(string) string { return srv.URL + "/notfound" }
	releaseAPIURL = func(string) string { return srv.URL + "/api/releases/latest" }
	if tag, err := latestTag(tool{Repo: "o/r"}); err != nil || tag != "v8.8.8" {
		t.Fatalf("api fallback: tag=%q err=%v", tag, err)
	}
}

// isolateConfig 把配置目录指到临时目录，并清掉进程里的记录缓存，
// 免得测试把 state.json 写进真实配置
func isolateConfig(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	old := userConfigDir
	userConfigDir = func() (string, error) { return dir, nil }
	resetHistory()
	t.Cleanup(func() {
		userConfigDir = old
		resetHistory()
	})
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
	isolateConfig(t)
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

	got, err := installTool(toolState{Tool: tl, Path: target, Installed: "0.0.1", Latest: "9.9.9"}, nil)
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
	isolateConfig(t)
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
	if _, err := installTool(toolState{Tool: tl, Path: target, Latest: "9.9.8"}, nil); err == nil {
		t.Fatal("校验不匹配却成功了")
	}
	content, _ := os.ReadFile(target)
	if string(content) != "old" {
		t.Errorf("校验失败时不该动原文件: %q", content)
	}
}

func TestInstallToolUnsupportedPlatform(t *testing.T) {
	isolateConfig(t)
	tl := tool{Key: "fake", Repo: "o/r", Binary: "fake", Platforms: []string{"plan9/mips"}}
	if _, err := installTool(toolState{Tool: tl, Latest: "1.0.0"}, nil); err == nil {
		t.Error("不支持的平台应当报错")
	}
}

func TestUpgradeAllCmd(t *testing.T) {
	isolateConfig(t)
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

var ansiEscape = regexp.MustCompile("\x1b\\[[0-9;]*m")

func plainText(s string) string { return ansiEscape.ReplaceAllString(s, "") }

// 版本那一格：状态只用一个图标表示，已是最新时不再重复写「最新 x.y.z」
func TestVersionCell(t *testing.T) {
	m := newTUIModel()
	m.localReady = true
	m.pending = map[string]bool{}
	m.staged = map[string]string{}

	cases := []struct {
		name string
		st   toolState
		want string
	}{
		{"已是最新", toolState{Path: "/tmp/tool-a", Installed: "0.4.2", Latest: "0.4.2"}, "✓ 0.4.2"},
		{"可更新", toolState{Path: "/tmp/tool-a", Installed: "0.4.2", Latest: "0.4.3"}, "↑ 0.4.2 → 0.4.3"},
		{"未安装", toolState{Latest: "0.1.0"}, "○ 未安装"},
		{"只查到已装版本", toolState{Path: "/tmp/tool-a", Installed: "0.1.0"}, "✓ 0.1.0"},
		// PATH 上别人的那份（brew 的 glab）跟已装的显示一样：✓ + 它自己的版本号，
		// 哪怕 brew 那边落后了也不写成 ↑——hmck 替它升不了
		{"PATH 上的那份", toolState{Foreign: []string{"/opt/homebrew/bin/glab"}, ForeignVer: "1.130.0", Latest: "1.133.0"}, "✓ 1.130.0"},
		{"PATH 上那份但读不到版本", toolState{Foreign: []string{"/opt/homebrew/bin/glab"}}, "· PATH 上的那份"},
	}
	for _, c := range cases {
		got := plainText(m.versionCell(c.st))
		if got != c.want {
			t.Errorf("%s: versionCell = %q, want %q", c.name, got, c.want)
		}
	}
}

// 最近一版已下载时换成 ↓，提示按 u 安装
func TestVersionCellStaged(t *testing.T) {
	m := newTUIModel()
	m.localReady = true
	m.pending = map[string]bool{}
	m.staged = map[string]string{"tool-a": "0.4.3"}
	got := plainText(m.versionCell(toolState{Tool: tool{Key: "tool-a"}, Path: "/tmp/tool-a", Installed: "0.4.2", Latest: "0.4.3"}))
	if !strings.HasPrefix(got, "↓ ") {
		t.Errorf("已下载应显示 ↓，got %q", got)
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

func TestKeysBlockedBeforeLocalScan(t *testing.T) {
	m := newTUIModel()
	m.state = list
	m.localReady = false
	m.states = []toolState{{Tool: tool{Key: "tool-a"}}}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if got := updated.(tuiModel); got.state != list {
		t.Errorf("本地还没扫完就响应了 enter: state=%v", got.state)
	}
}

// 结果页按任意键要回得到列表（之前会一直停在结果页），q / esc 直接退出
func TestAnyKeyLeavesResult(t *testing.T) {
	m := newTUIModel()
	m.state = result
	m.resultLines = []resultLine{{text: "hmck 0.6.0 → 0.7.0 已更新", ok: true}}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	got := updated.(tuiModel)
	if got.state != list {
		t.Fatalf("按 x 之后还在 state=%v", got.state)
	}
	if cmd != nil {
		t.Errorf("回列表不需要重扫，cmd 应为 nil")
	}

	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if updated.(tuiModel).state != result {
		t.Error("q 应当直接退出")
	}
	if cmd == nil {
		t.Fatal("q 没有返回退出命令")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("q 返回的不是 tea.Quit")
	}
}

// 列表页左边是 hmck 的招牌：docs/logos/hmck.svg 渲染成的象限方块
func TestListViewShowsLogo(t *testing.T) {
	m := newTUIModel()
	m.width, m.height = 100, 40
	m.localReady = true
	m.pending = map[string]bool{}
	m.states = []toolState{{
		Tool: tool{Key: "tool-a", Desc: "示例工具"}, Path: "/tmp/tool-a",
		Installed: "0.1.0", Latest: "0.1.0",
	}}

	got := plainText(m.viewList())
	logo := toolLogo(tool{Key: "hmck"})
	if len(logo) != logoRowsN {
		t.Fatalf("招牌应有 %d 行，got %d", logoRowsN, len(logo))
	}
	for _, row := range logo {
		if !strings.Contains(got, plainText(row)) {
			t.Errorf("列表页缺招牌那一行 %q:\n%s", plainText(row), got)
		}
	}
	if first := strings.SplitN(got, "\n", 2)[0]; !strings.Contains(first, plainText(logo[0])) || !strings.Contains(first, "hmck") {
		t.Errorf("第一行应是招牌 + 标题，got %q", first)
	}
}

// 「处理中」界面在下载时得画出进度：百分比、已下载 / 总大小，还有按宽度收放的进度条
func TestWorkingViewShowsProgressBar(t *testing.T) {
	m := newTUIModel()
	m.width = 80
	m.state = working
	m.workText = "正在下载 kube-token"
	m.progBytes, m.progDone = 1<<20, 1<<19 // 1 MB 里下了 512 KB

	got := plainText(m.viewWorking())
	if !strings.Contains(got, " 50%") {
		t.Fatalf("没有百分比:\n%s", got)
	}
	if !strings.Contains(got, "512 KB / 1.0 MB") {
		t.Errorf("没有大小:\n%s", got)
	}
	// 宽度 80 时条长 30 格（80-38），一半就是 15 个实心
	if !strings.Contains(got, strings.Repeat(barFill, 15)+strings.Repeat(barEmpty, 15)) {
		t.Errorf("进度条没画到一半:\n%s", got)
	}
	if !strings.Contains(got, "正在下载 kube-token") {
		t.Errorf("丢了正在做什么:\n%s", got)
	}
}

// 对方没给 Content-Length 时画不出比例，只报已下载多少；一键更新时按「第几个 + 这个下到哪」合成一条
func TestProgressBarUnknownLengthAndBatch(t *testing.T) {
	m := newTUIModel()
	m.width = 80
	m.state = working
	m.progDone = 3 << 20 // 已下 3 MB，不知道总大小

	got := plainText(m.viewWorking())
	if strings.Contains(got, "%") || !strings.Contains(got, "已下载 3.0 MB") {
		t.Errorf("长度未知时应当只报已下载:\n%s", got)
	}

	// 四个工具更新到第二个，当下这个文件下一半：整体 37.5%，四舍五入 38%
	m.progIndex, m.progTotal = 2, 4
	m.progDone, m.progBytes = 512, 1024
	got = plainText(m.viewWorking())
	if !strings.Contains(got, " 38%") {
		t.Errorf("一键更新应当按整体进度算:\n%s", got)
	}
}

// 下载进度消息只在当前这轮、还在处理时生效：过期的要丢掉，批量序号也要跟着走
func TestProgMsgUpdatesBar(t *testing.T) {
	m := newTUIModel()
	m.state = working
	m.updGen = 3
	m.updCh = make(chan tea.Msg, 4)

	updated, cmd := m.Update(progMsg{gen: 3, done: 100, bytes: 400})
	got := updated.(tuiModel)
	if got.progDone != 100 || got.progBytes != 400 {
		t.Fatalf("没记下进度: %d/%d", got.progDone, got.progBytes)
	}
	if cmd == nil {
		t.Error("应当继续等下一条进度")
	}

	updated, _ = got.Update(progMsg{gen: 2, done: 300, bytes: 400})
	if after := updated.(tuiModel); after.progDone != 100 {
		t.Errorf("过期 gen 的进度不该生效: %d", after.progDone)
	}

	updated, _ = got.Update(progMsg{gen: 3, index: 2, total: 5, done: 10, bytes: 20})
	if after := updated.(tuiModel); after.progIndex != 2 || after.progTotal != 5 {
		t.Errorf("批量序号没更新: %d/%d", after.progIndex, after.progTotal)
	}
}

func TestLatestResultsArriveOneByOne(t *testing.T) {
	isolateConfig(t) // 本机的自动安装开关不能影响这里
	m := newTUIModel()
	m.state = list
	m.localReady = true
	m.chkGen = 7
	m.states = []toolState{
		{Tool: tool{Key: "tool-a"}, Path: "/x/tool-a", Installed: "0.1.0"},
		{Tool: tool{Key: "tool-b"}, Path: "/x/tool-b", Installed: "0.2.0"},
	}
	m.pending = map[string]bool{"tool-a": true, "tool-b": true}

	updated, _ := m.Update(latestDoneMsg{gen: 7, key: "tool-a", ver: "0.3.0"})
	got := updated.(tuiModel)
	if got.states[0].Latest != "0.3.0" || got.pending["tool-a"] {
		t.Fatalf("tool-a 没更新: %+v pending=%v", got.states[0], got.pending)
	}
	if !got.states[0].Behind() {
		t.Error("tool-a 应当显示可更新")
	}
	if got.states[1].Latest != "" || !got.pending["tool-b"] {
		t.Error("tool-b 不应该被 tool-a 的结果影响")
	}
	if !got.animating() {
		t.Error("还有工具在查，应该继续转圈")
	}

	updated, _ = got.Update(latestDoneMsg{gen: 7, key: "tool-b", ver: "0.2.0"})
	got = updated.(tuiModel)
	if len(got.pending) != 0 {
		t.Errorf("还在等: %v", got.pending)
	}

	updated, _ = got.Update(latestClosedMsg{gen: 7})
	got = updated.(tuiModel)
	if got.checkedAt.IsZero() {
		t.Error("查完后应该记下检查时间")
	}
	if got.animating() {
		t.Error("都查完了不该再转圈")
	}

	// 上一轮迟到的结果不能串进来
	updated, _ = got.Update(latestDoneMsg{gen: 6, key: "tool-a", ver: "9.9.9"})
	if updated.(tuiModel).states[0].Latest != "0.3.0" {
		t.Error("旧一轮的结果被采纳了")
	}
}

func TestInspectLocalDoesNotAskGitHub(t *testing.T) {
	tl := tool{
		Key:       "nope-tool",
		Binary:    "nope-tool",
		Repo:      "example/nope",
		Platforms: []string{runtime.GOOS + "/" + runtime.GOARCH},
	}
	st := inspectLocal(tl)
	if st.Latest != "" {
		t.Errorf("inspectLocal 不该联网查最新版: %q", st.Latest)
	}
	if st.CheckErr != "" {
		t.Errorf("inspectLocal 不该有网络错误: %s", st.CheckErr)
	}
	if st.Installed != "" || st.Path != "" {
		t.Errorf("没装的东西不该有版本/路径: %+v", st)
	}
}

func TestDefaultBinDirIsUnified(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	got, err := defaultBinDir()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".local", "bin"); got != want {
		t.Errorf("defaultBinDir() = %q, want %q", got, want)
	}
}

func TestMoveBinary(t *testing.T) {
	base := t.TempDir()
	legacy := filepath.Join(base, "legacy")
	binDir := filepath.Join(base, "bin")
	for _, d := range []string{legacy, binDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// 旧的搬到统一目录，旧文件消失
	src := filepath.Join(legacy, "tool-a")
	dst := filepath.Join(binDir, "tool-a")
	if err := os.WriteFile(src, []byte("a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := moveBinary(src, dst, filepath.Join(base, "other")); err != nil {
		t.Fatalf("搬旧文件失败: %v", err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Error("旧位置的文件应该已经搬走")
	}
	if _, err := os.Stat(dst); err != nil {
		t.Errorf("统一目录里应该有文件: %v", err)
	}

	// 正在运行的自己：只能复制，旧文件留着
	run := filepath.Join(legacy, "hmck")
	runDst := filepath.Join(binDir, "hmck")
	if err := os.WriteFile(run, []byte("hmck"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := moveBinary(run, runDst, run); err != nil {
		t.Fatalf("复制运行中的自己失败: %v", err)
	}
	if _, err := os.Stat(run); err != nil {
		t.Error("运行中的旧文件删不掉，应该保留")
	}
	if got, err := os.ReadFile(runDst); err != nil || string(got) != "hmck" {
		t.Errorf("统一目录里的副本不对: %q %v", got, err)
	}

	// 统一目录里的自己正在运行：旧副本直接清掉
	other := filepath.Join(legacy, "tool-b")
	if err := os.WriteFile(other, []byte("b"), 0o755); err != nil {
		t.Fatal(err)
	}
	otherDst := filepath.Join(binDir, "tool-b")
	if err := os.WriteFile(otherDst, []byte("b-new"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := moveBinary(other, otherDst, otherDst); err != nil {
		t.Fatalf("清理旧副本失败: %v", err)
	}
	if _, err := os.Stat(other); !os.IsNotExist(err) {
		t.Error("统一目录里的在运行时，旧副本应该清掉")
	}
	if got, _ := os.ReadFile(otherDst); string(got) != "b-new" {
		t.Error("正在运行的统一目录文件不该被覆盖")
	}
}

func TestMigrateInstallLocations(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 上会改真实用户 PATH，留给安装脚本验证")
	}
	home := t.TempDir()
	legacy := t.TempDir()

	oldHome := userHomeDir
	userHomeDir = func() (string, error) { return home, nil }
	defer func() { userHomeDir = oldHome }()
	oldLegacy := legacyDirsForTest
	legacyDirsForTest = []string{legacy}
	defer func() { legacyDirsForTest = oldLegacy }()
	t.Setenv("HMCK_NO_MIGRATE", "")

	names := []string{"kube-token", "npm-token", "hmck"}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(legacy, name), []byte("bin-"+name), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	migrateInstallLocations()

	binDir := filepath.Join(home, ".local", "bin")
	for _, name := range names {
		got, err := os.ReadFile(filepath.Join(binDir, name))
		if err != nil {
			t.Fatalf("%s 没搬到统一目录: %v", name, err)
		}
		if string(got) != "bin-"+name {
			t.Errorf("%s 内容不对: %q", name, got)
		}
		if _, err := os.Stat(filepath.Join(legacy, name)); !os.IsNotExist(err) {
			t.Errorf("%s 的旧副本应该已经清掉", name)
		}
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Error("搬空后的旧目录应该删掉")
	}
}

func TestMigrateSkipsWhenDisabled(t *testing.T) {
	legacy := t.TempDir()
	if err := os.WriteFile(filepath.Join(legacy, "hmck"), []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	oldHome := userHomeDir
	userHomeDir = func() (string, error) { return home, nil }
	defer func() { userHomeDir = oldHome }()
	oldLegacy := legacyDirsForTest
	legacyDirsForTest = []string{legacy}
	defer func() { legacyDirsForTest = oldLegacy }()
	t.Setenv("HMCK_NO_MIGRATE", "1")

	migrateInstallLocations()

	if _, err := os.Stat(filepath.Join(home, ".local", "bin", "hmck")); !os.IsNotExist(err) {
		t.Error("HMCK_NO_MIGRATE 下不该搬任何东西")
	}
	if _, err := os.Stat(filepath.Join(legacy, "hmck")); err != nil {
		t.Error("旧文件不该被删")
	}
}

func TestNewerFile(t *testing.T) {
	dir := t.TempDir()
	older := filepath.Join(dir, "older")
	newer := filepath.Join(dir, "newer")
	for _, p := range []string{older, newer} {
		if err := os.WriteFile(p, []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	oldTime := time.Now().Add(-time.Hour)
	if err := os.Chtimes(older, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if newerFile(older, newer) {
		t.Error("旧文件不该被当成更新的")
	}
	if !newerFile(newer, older) {
		t.Error("新文件应该被当成更新的")
	}
	if newerFile(older, filepath.Join(dir, "missing")) != true {
		t.Error("目标不存在时应当复制")
	}
}

// kubectl 在 dl.k8s.io 发版：最新版读 stable.txt，资产按 平台/架构 分目录，
// 校验值是资产旁边同名的 .sha256（里面只有一行哈希）
func TestKubernetesLatestTag(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/stable.txt" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		fmt.Fprintln(w, "v1.37.2")
	}))
	defer srv.Close()

	old := kubernetesStableURL
	kubernetesStableURL = func() string { return srv.URL + "/stable.txt" }
	defer func() { kubernetesStableURL = old }()

	tag, err := latestTag(tool{Key: "kubectl", Host: "kubernetes", Repo: "kubernetes/kubernetes"})
	if err != nil || tag != "v1.37.2" {
		t.Fatalf("latestTag = %q, %v", tag, err)
	}

	// stable.txt 里不是版本号时要说清楚，别把整段文字当版本
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "<html>oops</html>")
	}))
	defer bad.Close()
	kubernetesStableURL = func() string { return bad.URL }
	if _, err := latestTag(tool{Host: "kubernetes"}); err == nil {
		t.Error("stable.txt 内容不对时应当报错")
	}
}

func TestKubernetesAssetURL(t *testing.T) {
	tl := tool{Key: "kubectl", Host: "kubernetes", Binary: "kubectl", Asset: "kubectl{ext}"}
	asset := assetName(tl, runtime.GOOS, runtime.GOARCH, "v1.37.2")
	want := "https://dl.k8s.io/release/v1.37.2/bin/" + runtime.GOOS + "/" + runtime.GOARCH + "/" + asset
	if got := releaseAssetURLFor(tl, "v1.37.2", asset); got != want {
		t.Errorf("资产地址 = %q，想要 %q", got, want)
	}
	if runtime.GOOS == "windows" && asset != "kubectl.exe" {
		t.Errorf("Windows 的资产名 = %q", asset)
	}
}

// 校验文件不一定叫 checksums.txt：argo-cd 用 cli_checksums.txt，
// 目录里写了 checksums_file 就按这个名字去取
func TestVerifyCustomChecksumsFile(t *testing.T) {
	payload := bytes.Repeat([]byte("ARGOCD"), 20_000)
	sum := sha256.Sum256(payload)
	asset := "argocd-darwin-arm64"
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path)
		if !strings.HasSuffix(r.URL.Path, "/cli_checksums.txt") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), asset)
	}))
	defer srv.Close()

	old := releaseAssetURL
	releaseAssetURL = func(repo, tag, name string) string { return srv.URL + "/download/" + tag + "/" + name }
	defer func() { releaseAssetURL = old }()

	file := filepath.Join(t.TempDir(), "argocd")
	if err := os.WriteFile(file, payload, 0o755); err != nil {
		t.Fatal(err)
	}
	tl := tool{
		Key: "argocd", Repo: "argoproj/argo-cd", Binary: "argocd",
		Asset: "argocd-{goos}-{goarch}{ext}", Checksums: true, ChecksumsFile: "cli_checksums.txt",
	}
	if err := verifyChecksum(tl, "v3.6.2", asset, file); err != nil {
		t.Fatalf("校验应当通过: %v", err)
	}
	for _, p := range asked {
		if strings.HasSuffix(p, "/checksums.txt") {
			t.Errorf("不该去取默认的 checksums.txt: %v", asked)
		}
	}
	if err := os.WriteFile(file, []byte("tampered"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := verifyChecksum(tl, "v3.6.2", asset, file); err == nil {
		t.Error("内容和哈希对不上时应当失败")
	}
}

func TestVerifyKubernetesChecksum(t *testing.T) {
	payload := bytes.Repeat([]byte("KUBECTL"), 20_000)
	sum := sha256.Sum256(payload)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/kubectl.sha256") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		fmt.Fprintln(w, hex.EncodeToString(sum[:]))
	}))
	defer srv.Close()

	old := kubernetesAssetURL
	kubernetesAssetURL = func(tag, goos, goarch, asset string) string {
		return srv.URL + "/" + tag + "/" + asset
	}
	defer func() { kubernetesAssetURL = old }()

	file := filepath.Join(t.TempDir(), "kubectl")
	if err := os.WriteFile(file, payload, 0o755); err != nil {
		t.Fatal(err)
	}
	tl := tool{Key: "kubectl", Host: "kubernetes", Binary: "kubectl", Asset: "kubectl{ext}", Checksums: true}
	if err := verifyChecksum(tl, "v1.37.2", "kubectl", file); err != nil {
		t.Errorf("校验应当通过: %v", err)
	}
	if err := os.WriteFile(file, []byte("tampered"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := verifyChecksum(tl, "v1.37.2", "kubectl", file); err == nil {
		t.Error("内容和哈希对不上时应当失败")
	}
}
