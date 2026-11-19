package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

const sampleChangelog = `# 变更记录

这段开头的说明不属于任何版本。

## [Unreleased]

## [0.8.0] - 2026-11-18

### 新增

- **加粗**：写着 ` + "`代码`" + ` 的条目
  折行接着写
- [文档](https://example.com/docs) 和 ![](https://example.com/a.png)

### 修复

- 修好了一个问题

## 0.7.0 (2026-11-17)

- 没有小节的条目
`

func TestParseChangelog(t *testing.T) {
	entries := parseChangelog(sampleChangelog)
	if len(entries) != 2 {
		t.Fatalf("解析出 %d 个版本，想要 2 个: %+v", len(entries), entries)
	}

	head := entries[0]
	if head.Version != "0.8.0" || head.Date != "2026-11-18" {
		t.Errorf("第一个版本 = %q %q", head.Version, head.Date)
	}
	if len(head.Sections) != 2 || head.Sections[0].Title != "新增" || head.Sections[1].Title != "修复" {
		t.Fatalf("小节 = %+v", head.Sections)
	}
	items := head.Sections[0].Items
	if len(items) != 2 {
		t.Fatalf("新增小节条目 = %+v", items)
	}
	if items[0] != "加粗：写着 代码 的条目 折行接着写" {
		t.Errorf("Markdown 没清干净或折行没接上: %q", items[0])
	}
	if items[1] != "文档 和" {
		t.Errorf("链接/图片没处理好: %q", items[1])
	}

	tail := entries[1]
	if tail.Version != "0.7.0" || tail.Date != "2026-11-17" {
		t.Errorf("第二个版本 = %q %q", tail.Version, tail.Date)
	}
	if got := tail.summary(); got != "没有小节的条目" {
		t.Errorf("summary = %q", got)
	}
}

// 仓库没有 CHANGELOG.md 时退到 release notes
func TestFetchChangelogFallsBackToReleases(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/CHANGELOG.md"):
			w.WriteHeader(http.StatusNotFound)
		case strings.HasSuffix(r.URL.Path, "/releases"):
			fmt.Fprint(w, `[{"tag_name":"v1.2.0","published_at":"2026-01-02T03:04:05Z",`+
				`"body":"### 新增\n\n- 第一件事\n  - 嵌套的也算一条"}]`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	oldRaw, oldRel := changelogRawURL, releasesListURL
	changelogRawURL = func(repo string) string { return srv.URL + "/" + repo + "/CHANGELOG.md" }
	releasesListURL = func(repo string) string { return srv.URL + "/repos/" + repo + "/releases" }
	t.Cleanup(func() { changelogRawURL, releasesListURL = oldRaw, oldRel })

	entries, err := fetchChangelog(tool{Key: "tool", Repo: "someone/tool"}, 3)
	if err != nil || len(entries) != 1 {
		t.Fatalf("fetchChangelog = %+v, %v", entries, err)
	}
	if entries[0].Version != "1.2.0" || entries[0].Date != "2026-01-02" {
		t.Errorf("release 版本 = %+v", entries[0])
	}
	if got := entries[0].summary(); got != "第一件事" {
		t.Errorf("release summary = %q", got)
	}
}

// release 说明里的标题级别不固定，`## Changelog` 这种整段包起来的不能当成条目
func TestSectionsFromMarkdownSkipsWrappedHeading(t *testing.T) {
	secs := sectionsFromMarkdown("## Changelog\n\n### Features\n\n- 加了一个功能\n- 修了一个 bug\n")
	entries := []changeEntry{{Version: "1.131.0", Sections: secs}}
	if got := entries[0].summary(); got != "加了一个功能" {
		t.Fatalf("summary = %q，标题被当成了条目: %+v", got, secs)
	}
	if secs[0].Title != "Features" || len(secs[0].Items) != 2 {
		t.Errorf("小节 = %+v", secs)
	}

	// 井号后面没有空格的是 issue 引用，不是标题
	secs = sectionsFromMarkdown("#123 修掉一个崩溃\n")
	if got := (changeEntry{Sections: secs}).summary(); got != "#123 修掉一个崩溃" {
		t.Errorf("issue 引用被当成标题: %+v", secs)
	}
}

// CHANGELOG.md 能读到就不该再问 API
func TestFetchChangelogPrefersRepoFile(t *testing.T) {
	askedAPI := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/releases") {
			askedAPI = true
			w.WriteHeader(http.StatusNotFound)
			return
		}
		fmt.Fprint(w, "## [2.0.0] - 2026-02-02\n\n### 新增\n\n- 仓库里的说明\n")
	}))
	defer srv.Close()

	oldRaw, oldRel := changelogRawURL, releasesListURL
	changelogRawURL = func(repo string) string { return srv.URL + "/" + repo + "/CHANGELOG.md" }
	releasesListURL = func(repo string) string { return srv.URL + "/repos/" + repo + "/releases" }
	t.Cleanup(func() { changelogRawURL, releasesListURL = oldRaw, oldRel })

	entries, err := fetchChangelog(tool{Key: "tool", Repo: "someone/tool"}, 5)
	if err != nil || len(entries) != 1 || entries[0].Version != "2.0.0" {
		t.Fatalf("fetchChangelog = %+v, %v", entries, err)
	}
	if askedAPI {
		t.Error("有 CHANGELOG.md 还去问了 release API")
	}
}

// GitLab 的 release 用 released_at / description，仓库文件在 /-/raw/ 下
func TestFetchChangelogGitLab(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/-/raw/"):
			w.WriteHeader(http.StatusNotFound)
		case strings.Contains(r.URL.Path, "/releases"):
			fmt.Fprint(w, `[{"tag_name":"v1.131.0","released_at":"2026-03-04T05:06:07Z",`+
				`"description":"### 变更\n\n- 在 GitLab 上发的新版"}]`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	oldRaw, oldRel := gitlabChangelogRawURL, gitlabReleasesListURL
	gitlabChangelogRawURL = func(repo string) string { return srv.URL + "/" + repo + "/-/raw/main/CHANGELOG.md" }
	gitlabReleasesListURL = func(repo string) string { return srv.URL + "/projects/" + repo + "/releases" }
	t.Cleanup(func() { gitlabChangelogRawURL, gitlabReleasesListURL = oldRaw, oldRel })

	entries, err := fetchChangelog(tool{Key: "glab", Repo: "group/tool", Host: "gitlab"}, 3)
	if err != nil || len(entries) != 1 {
		t.Fatalf("fetchChangelog = %+v, %v", entries, err)
	}
	if entries[0].Version != "1.131.0" || entries[0].Date != "2026-03-04" {
		t.Errorf("GitLab release 版本 = %+v", entries[0])
	}
	if got := entries[0].summary(); got != "在 GitLab 上发的新版" {
		t.Errorf("GitLab release summary = %q", got)
	}
}

func TestFormatChangelogText(t *testing.T) {
	entries := []changeEntry{
		{Version: "0.8.0", Date: "2026-11-18", Sections: []changeSection{{Title: "新增", Items: []string{"加了东西"}}}},
		{Version: "0.7.0", Date: "2026-11-17", Sections: []changeSection{{Items: []string{"改了东西"}}}},
	}
	got := formatChangelogText("tool", entries, "0.7.0")
	for _, want := range []string{
		"tool 更新日志（当前 0.7.0）",
		"v0.8.0  2026-11-18  最新",
		"v0.7.0  2026-11-17  当前",
		"  - 加了东西",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("缺少 %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "当前 · 最新") {
		t.Errorf("当前版本不是最新时不该标「当前 · 最新」:\n%s", got)
	}
}

// 列表里每个工具左边都有自己的招牌
func TestListViewShowsToolLogos(t *testing.T) {
	isolateConfig(t)
	m := newTUIModel()
	m.width, m.height = 110, 40
	m.localReady = true
	m.pending = map[string]bool{}
	m.chg = map[string]changelogState{}
	m.states = []toolState{
		{Tool: tool{Key: "kube-token", Repo: "hongmengchen/kube-token", Desc: "换 kube token"}, Path: "/tmp/kube-token", Installed: "0.8.0", Latest: "0.8.0"},
		{Tool: tool{Key: "npm-token", Repo: "hongmengchen/npm-token", Desc: "换 npm token"}, Path: "/tmp/npm-token", Installed: "0.5.0", Latest: "0.5.0"},
		{Tool: tool{Key: "hmck", Repo: "hongmengchen/hmck", Desc: "管理工具", Self: true}, Path: "/tmp/hmck", Installed: "0.8.0", Latest: "0.8.0"},
		{Tool: tool{Key: "my-tool", Repo: "me/my-tool", Desc: "自定义"}, Path: "/tmp/my-tool", Installed: "1.0.0", Latest: "1.0.0"},
	}

	got := plainText(m.viewList())
	for _, want := range []string{"│ K │", "│ N │", "│ ↑ │", "│ M │"} {
		if !strings.Contains(got, want) {
			t.Errorf("列表里缺少招牌 %q:\n%s", want, got)
		}
	}
	if got := plainText(strings.Join(toolLogo(tool{Key: "中文工具"}), "")); !strings.Contains(got, "│ * │") {
		t.Errorf("自定义工具的非 ASCII 首字母不该塞进方框: %q", got)
	}
}

// 卡片第三行预览最近一版改了什么，这些数据启动时预取
func TestCardShowsChangelogPreview(t *testing.T) {
	m := newTUIModel()
	m.width, m.height = 110, 40
	m.localReady = true
	m.pending = map[string]bool{}
	m.states = []toolState{{Tool: tool{Key: "kube-token", Repo: "hongmengchen/kube-token", Desc: "换 token"},
		Path: "/tmp/kube-token", Installed: "0.8.0", Latest: "0.8.0"}}

	updated, _ := m.Update(changelogMsg{key: "kube-token", prefetch: true, entries: []changeEntry{
		{Version: "0.8.0", Date: "2026-11-18", Sections: []changeSection{{Title: "新增", Items: []string{"Vault 登录缓存"}}}},
	}})
	m = updated.(tuiModel)
	if got := plainText(m.renderCard(m.states[0], false, m.cardCols())); !strings.Contains(got, "v0.8.0  Vault 登录缓存") {
		t.Errorf("卡片没显示更新日志预览:\n%s", got)
	}
}

// 按 g 打开所选工具的更新日志；缓存命中时不再请求网络
func TestNotesPageShowsCachedChangelog(t *testing.T) {
	m := newTUIModel()
	m.width, m.height = 100, 30
	m.localReady = true
	m.pending = map[string]bool{}
	m.states = []toolState{{Tool: tool{Key: "kube-token", Repo: "hongmengchen/kube-token"},
		Path: "/tmp/kube-token", Installed: "0.7.0", Latest: "0.8.0"}}
	m.chg = map[string]changelogState{"kube-token": {entries: []changeEntry{
		{Version: "0.8.0", Date: "2026-11-18", Sections: []changeSection{{Title: "新增", Items: []string{"Vault 登录缓存"}}}},
		{Version: "0.7.0", Date: "2026-11-17", Sections: []changeSection{{Title: "新增", Items: []string{"自己的标志"}}}},
	}}}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("g")})
	got := updated.(tuiModel)
	if got.state != notes {
		t.Fatalf("按 g 之后 state = %v", got.state)
	}
	if cmd != nil {
		t.Error("缓存里已经有日志，不该再发请求")
	}
	view := plainText(got.viewNotes())
	for _, want := range []string{"kube-token  更新日志", "│ K │", "当前 0.7.0 · 最新 0.8.0", "v0.8.0", "Vault 登录缓存"} {
		if !strings.Contains(view, want) {
			t.Errorf("日志页缺少 %q:\n%s", want, view)
		}
	}

	// esc 回列表
	updated, _ = got.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if updated.(tuiModel).state != list {
		t.Error("esc 应当回列表")
	}
}

// 日志比一屏长时能滚动，且不会滚过头
func TestNotesScroll(t *testing.T) {
	m := newTUIModel()
	m.width, m.height = 90, 12
	m.state = notes
	m.note = notesView{
		tool: tool{Key: "kube-token"},
		entries: []changeEntry{{Version: "0.8.0", Sections: []changeSection{{Title: "新增", Items: []string{
			"第一条", "第二条", "第三条", "第四条", "第五条", "第六条",
		}}}}},
	}
	maxScroll := m.notesMaxScroll()
	if maxScroll <= 0 {
		t.Fatalf("这页应该需要滚动，maxScroll = %d", maxScroll)
	}

	for i := 0; i < maxScroll+5; i++ {
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = updated.(tuiModel)
	}
	if m.note.scroll != maxScroll {
		t.Errorf("滚到底 scroll = %d, want %d", m.note.scroll, maxScroll)
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyHome})
	if got := updated.(tuiModel); got.note.scroll != 0 {
		t.Errorf("home 之后 scroll = %d", got.note.scroll)
	}
}
