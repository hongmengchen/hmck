package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func catalogToolKeys(tools []tool) []string {
	keys := make([]string, len(tools))
	for i, t := range tools {
		keys[i] = t.Key
	}
	return keys
}

func hasCatalogTool(tools []tool, key string) bool {
	for _, t := range tools {
		if t.Key == key {
			return true
		}
	}
	return false
}

// 目录里写内置工具的 key 也改不了它：从哪装、怎么校验一律按程序里的定义
func TestParseCatalogBuiltinWins(t *testing.T) {
	data := []byte(`{"version":1,"tools":[
		{"key":"glab","repo":"evil/glab","platforms":["linux/amd64"]},
		{"key":"my-tool","desc":"演示","repo":"me/my-tool","platforms":["darwin/arm64","linux/amd64"],"checksums":true}
	]}`)
	tools, skipped, err := parseCatalog(data)
	if err != nil || skipped != 0 || len(tools) != 2 {
		t.Fatalf("parseCatalog = %v, %d, %v", catalogToolKeys(tools), skipped, err)
	}
	if tools[0].Repo != "gitlab-org/cli" || !tools[0].OwnDir {
		t.Errorf("内置 glab 被目录改掉了: %+v", tools[0])
	}
	if tools[1].Key != "my-tool" || tools[1].Binary != "my-tool" || tools[1].Name != "my-tool" {
		t.Errorf("自定义条目没补默认值: %+v", tools[1])
	}
	if tools[1].Custom {
		t.Error("目录条目不该标成本机配置")
	}
}

// 写坏、重复、host 不认识的条目整条跳过，好的照收
func TestParseCatalogSkipsBadEntries(t *testing.T) {
	data := []byte(`{"version":1,"tools":[
		{"key":"ok","repo":"me/ok","platforms":["linux/amd64"]},
		{"key":"bad key","repo":"me/x"},
		{"key":"norepo","repo":"nope"},
		{"key":"ok","repo":"me/ok2"},
		{"key":"other","repo":"me/h","host":"bitbucket","platforms":["linux/amd64"]}
	]}`)
	tools, skipped, err := parseCatalog(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].Key != "ok" || skipped != 4 {
		t.Errorf("parseCatalog = %v, skipped %d", catalogToolKeys(tools), skipped)
	}
}

func TestParseCatalogRejectsBrokenDocs(t *testing.T) {
	for name, data := range map[string]string{
		"空":     ``,
		"坏JSON": `{"version":1,`,
		"版本":    `{"version":2,"tools":[]}`,
		"没工具":   `{"version":1,"tools":[]}`,
		"全坏":    `{"version":1,"tools":[{"key":"x"}]}`,
	} {
		if _, _, err := parseCatalog([]byte(data)); err == nil {
			t.Errorf("%s 的目录应该报错", name)
		}
	}
}

// 线上目录读不到就退回编进程序的那份，hmck 自己也得在里面
func TestLoadCatalogFallsBackToEmbedded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()
	old := catalogURL
	catalogURL = srv.URL
	t.Cleanup(func() { catalogURL = old })
	t.Setenv("HMCK_CATALOG_URL", "")

	res := loadCatalog()
	if res.source != "bundled" || res.err == "" {
		t.Fatalf("source = %q, err = %q，应退回内置副本并说明原因", res.source, res.err)
	}
	if !hasCatalogTool(res.tools, "hmck") || !hasCatalogTool(res.tools, "kubectl") {
		t.Errorf("内置副本不完整: %v", catalogToolKeys(res.tools))
	}
}

func TestLoadCatalogOnline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"version":1,"tools":[
			{"key":"webhook","desc":"演示","repo":"me/webhook","platforms":["linux/amd64"]}
		]}`)
	}))
	defer srv.Close()
	old := catalogURL
	catalogURL = srv.URL
	t.Cleanup(func() { catalogURL = old })
	t.Setenv("HMCK_CATALOG_URL", "")

	res := loadCatalog()
	if res.source != "online" || res.err != "" {
		t.Fatalf("source = %q, err = %q", res.source, res.err)
	}
	if len(res.tools) != 1 || res.tools[0].Key != "webhook" {
		t.Errorf("tools = %v", catalogToolKeys(res.tools))
	}
}

func TestLoadCatalogEnvOverride(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"version":1,"tools":[
			{"key":"webhook","repo":"me/webhook","platforms":["linux/amd64"]}
		]}`)
	}))
	defer srv.Close()
	t.Setenv("HMCK_CATALOG_URL", srv.URL)

	res := loadCatalog()
	if res.source != "online" || !hasCatalogTool(res.tools, "webhook") {
		t.Errorf("HMCK_CATALOG_URL 没生效: %+v", res)
	}
}

func catalogModel(t *testing.T) tuiModel {
	t.Helper()
	isolateConfig(t)
	m := newTUIModel()
	m.width, m.height = 100, 30
	m.localReady = true
	m.pending = map[string]bool{}
	m.cat.loaded = true
	m.cat.source = "online"
	m.cat.rows = []tool{
		{Key: "kube-token", Repo: "hongmengchen/kube-token", Desc: "换 token", Platforms: []string{runtime.GOOS + "/" + runtime.GOARCH}},
		{Key: "webhook", Repo: "me/webhook", Desc: "演示工具", Platforms: []string{runtime.GOOS + "/" + runtime.GOARCH}},
		{Key: "arm-only", Repo: "me/arm-only", Desc: "别的平台", Platforms: []string{"plan9/386"}},
	}
	return m
}

// 目录页把线上工具和本机状态摆在一起：已加入的、没加入的、本平台没包的分得清
func TestCatalogPageListsTools(t *testing.T) {
	m := catalogModel(t)
	m.cat.cursor = 1 // 详情行只跟选中的那个走
	view := plainText(m.viewCatalog())
	for _, want := range []string{"工具目录", "已加入", "在线目录", "kube-token", "webhook", "演示工具", "未添加", "本平台没有", "me/webhook"} {
		if !strings.Contains(view, want) {
			t.Errorf("目录页缺少 %q:\n%s", want, view)
		}
	}
}

// 目录长了就只画放得下的几行，光标走到哪窗口跟到哪
func TestCatalogScrollsWithCursor(t *testing.T) {
	m := catalogModel(t)
	m.height = 20 // 一屏 6 行
	rows := make([]tool, 12)
	for i := range rows {
		rows[i] = tool{Key: fmt.Sprintf("t%02d", i), Repo: "me/x", Desc: "演示", Platforms: m.cat.rows[0].Platforms}
	}
	m.cat.rows = rows
	m.states = nil // 只留目录里这 12 个，数好算
	m.cat.cursor = 10

	view := plainText(m.viewCatalog())
	if strings.Contains(view, "t00") {
		t.Errorf("光标在第 11 行时不该画第一行:\n%s", view)
	}
	for _, want := range []string{"t10", "上面还有 5 个", "下面还有 1 个"} {
		if !strings.Contains(view, want) {
			t.Errorf("目录页缺少 %q:\n%s", want, view)
		}
	}
}

// 本机自己加过、目录里没有的也列出来，别让人以为丢了
func TestCatalogRowsIncludeLocalCustom(t *testing.T) {
	m := catalogModel(t)
	m.states = append(m.states, toolState{Tool: tool{Key: "mine", Repo: "me/mine", Custom: true}})
	rows := m.catalogRows()
	if !hasCatalogTool(rows, "mine") {
		t.Errorf("本机自定义工具没出现在目录里: %v", catalogToolKeys(rows))
	}
	m.cat.cursor = len(rows) - 1
	if !strings.Contains(plainText(m.viewCatalog()), "本机添加") {
		t.Error("选中本机添加的工具时该注明来源")
	}
}

// 目录里挑一个没加入的：先写配置再开装
func TestCatalogEnterAddsAndInstalls(t *testing.T) {
	m := catalogModel(t)
	m.state = catalog
	m.cat.cursor = 1

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(tuiModel)
	if cmd == nil || m.state != working || !strings.Contains(m.workText, "webhook") {
		t.Fatalf("enter 之后 state = %v, workText = %q, cmd = %v", m.state, m.workText, cmd != nil)
	}

	updated, _ = m.Update(catalogAddMsg{t: tool{Key: "webhook", Repo: "me/webhook", Custom: true}})
	m = updated.(tuiModel)
	if last := m.states[len(m.states)-1]; last.Tool.Key != "webhook" {
		t.Fatalf("加入结果没进列表: %+v", m.states)
	}
	if m.state != working {
		t.Errorf("加入后应该直接开装，state = %v", m.state)
	}
}

// 已经加入过的，enter 就是普通的安装/升级
func TestCatalogEnterManaged(t *testing.T) {
	m := catalogModel(t)
	m.states = append(m.states, toolState{Tool: tool{Key: "webhook", Repo: "me/webhook", Platforms: m.cat.rows[1].Platforms}})
	m.state = catalog
	m.cat.cursor = 1

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if got := updated.(tuiModel); got.state != working || !strings.Contains(got.workText, "安装 webhook") {
		t.Errorf("state = %v, workText = %q", got.state, got.workText)
	}
}

// 本平台没发布的不去下载，直接说清楚
func TestCatalogEnterUnsupportedPlatform(t *testing.T) {
	m := catalogModel(t)
	m.state = catalog
	m.cat.cursor = 2

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := updated.(tuiModel)
	if got.state != result || len(got.resultLines) != 1 || !strings.Contains(got.resultLines[0].text, runtime.GOOS) {
		t.Errorf("state = %v, lines = %+v", got.state, got.resultLines)
	}
}

// r 重新读目录；旧代次的结果回来也不许盖掉新的
func TestCatalogRefreshKeepsNewest(t *testing.T) {
	m := catalogModel(t)
	m.state = catalog
	oldGen := m.catGen

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	m = updated.(tuiModel)
	if cmd == nil || !m.cat.loading || m.catGen == oldGen {
		t.Fatalf("r 没有重新开始读: loading = %v, gen %d -> %d", m.cat.loading, oldGen, m.catGen)
	}
	gen := m.catGen

	updated, _ = m.Update(catalogMsg{gen: gen - 1, res: catalogResult{tools: []tool{{Key: "stale"}}, source: "online"}})
	m = updated.(tuiModel)
	if hasCatalogTool(m.cat.rows, "stale") {
		t.Error("旧代次的目录结果把新结果盖掉了")
	}
	if !m.cat.loading {
		t.Error("旧结果不该结束新的读取")
	}

	updated, _ = m.Update(catalogMsg{gen: gen, res: catalogResult{tools: []tool{{Key: "fresh"}}, source: "online"}})
	m = updated.(tuiModel)
	if !hasCatalogTool(m.cat.rows, "fresh") || m.cat.loading {
		t.Errorf("新结果没落地: %v, loading %v", catalogToolKeys(m.cat.rows), m.cat.loading)
	}
}

func TestCatalogAddErrorStaysOnPage(t *testing.T) {
	m := catalogModel(t)
	m.state = catalog
	updated, _ := m.Update(catalogAddMsg{err: errString("写不进配置文件")})
	m = updated.(tuiModel)
	if m.state != catalog || !strings.Contains(m.cat.err, "写不进配置文件") {
		t.Errorf("state = %v, err = %q", m.state, m.cat.err)
	}
}

// esc 回列表
func TestCatalogEscReturnsToList(t *testing.T) {
	m := catalogModel(t)
	m.state = catalog
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if got := updated.(tuiModel); got.state != list {
		t.Errorf("esc 之后 state = %v", got.state)
	}
}
