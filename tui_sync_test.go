package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// listTestModel 造一个不联网的列表页：工具状态都是现填的
func listTestModel(n int) tuiModel {
	m := newTUIModel()
	m.width, m.height = 110, 30
	m.localReady = true
	m.pending = map[string]bool{}
	m.states = make([]toolState, n)
	keys := []string{"glab", "kubectl", "argocd", "kube-token", "npm-token", "argo-token", "hmck"}
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("tool-%d", i)
		if i < len(keys) {
			key = keys[i]
		}
		kind := kindOwn
		switch {
		case i < 3:
			kind = kindOfficial
		case i >= len(keys):
			kind = kindAdded
		}
		m.states[i] = toolState{
			Tool: tool{Key: key, Kind: kind, Desc: "一个命令行小工具，做点有用的事", Repo: "alice/" + key},
			Path: "/usr/local/bin/" + key, Installed: "1.2.3", Latest: "1.2.4",
		}
	}
	return m
}

// 终端不高时列表自动退成紧凑版：标题、同步状态和按键提示都还在屏幕上
func TestListViewFitsShortTerminal(t *testing.T) {
	isolateConfig(t)
	m := listTestModel(8)
	m.syncLine = "同步 alice/hmck-config · 工具退出后自动推 · y 管理"
	m.cursor = 3

	body := m.viewList()
	if lines := strings.Count(body, "\n") + 1; lines+2 > m.height {
		t.Fatalf("列表画了 %d 行，终端只有 %d 行", lines+2, m.height)
	}
	got := plainText(body)
	for _, want := range []string{"我的命令行小工具", "同步 alice/hmck-config", "y 同步", "kube-token"} {
		if !strings.Contains(got, want) {
			t.Errorf("紧凑列表里缺少 %q:\n%s", want, got)
		}
	}
	// 紧凑版一个工具一行：招牌和分隔线都收起来
	if strings.Count(got, "\n") >= strings.Count(plainText(m.viewListBody(false, 0)), "\n") {
		t.Errorf("紧凑版并不比展开版短")
	}
	if strings.Contains(got, "─") {
		t.Errorf("紧凑版不应再画分隔线:\n%s", got)
	}
}

// 工具很多、窗口很矮时只画光标附近那几行，但光标所在行必须可见
func TestListViewWindowKeepsCursorVisible(t *testing.T) {
	isolateConfig(t)
	m := listTestModel(20)
	m.height = 20
	m.cursor = 15

	body := m.viewList()
	if lines := strings.Count(body, "\n") + 1; lines+2 > m.height {
		t.Fatalf("列表画了 %d 行，终端只有 %d 行", lines+2, m.height)
	}
	got := plainText(body)
	if !strings.Contains(got, "tool-15") {
		t.Errorf("光标所在的 tool-15 不在窗口里:\n%s", got)
	}
	if !strings.Contains(got, "上面还有") && !strings.Contains(got, "下面还有") {
		t.Errorf("截断时应该提示还有多少行:\n%s", got)
	}
}

// 首次推送在页面里输两遍密码；两次不一样要拦住，一样才去开仓库
func TestSyncPageFirstPushFromTUI(t *testing.T) {
	fake, _, _ := setupSyncTest(t)
	m := newTUIModel()
	m.width, m.height = 100, 40

	// y 打开同步页，enter 选「开始同步（首次推送）」
	updated, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m = updated.(tuiModel)
	if m.state != syncPage {
		t.Fatalf("y 没有打开同步页，state=%v", m.state)
	}
	updated, cmd := m.handleSyncKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(tuiModel)
	if m.sync.mode != syncPass || !m.sync.confirm {
		t.Fatalf("首次推送应进入输密码（两遍），得到 mode=%v confirm=%v", m.sync.mode, m.sync.confirm)
	}

	m = typeRunes(m, "correct horse")
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(tuiModel)
	if m.sync.passFirst != "correct horse" || m.sync.pass != "" {
		t.Fatalf("第一遍密码没记住: %+v", m.sync)
	}

	m = typeRunes(m, "wrong horse")
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(tuiModel)
	if !strings.Contains(m.sync.err, "不一样") {
		t.Fatalf("两次密码不一致应该报错，得到 err=%q", m.sync.err)
	}

	m = typeRunes(m, "correct horse")
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(tuiModel)
	if m.sync.err != "" || m.sync.passFirst != "correct horse" {
		t.Fatalf("不一致之后应该重新开始、第一遍已记下: err=%q first=%q", m.sync.err, m.sync.passFirst)
	}
	// 再输一遍同样的，确认后开始推送
	m = typeRunes(m, "correct horse")
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(tuiModel)
	if cmd == nil || !m.sync.busy {
		t.Fatalf("密码确认后应该开始推送: cmd=%v busy=%v err=%q", cmd, m.sync.busy, m.sync.err)
	}

	updated, _ = m.Update(cmd())
	m = updated.(tuiModel)
	if m.sync.err != "" {
		t.Fatalf("TUI 推送失败: %v", m.sync.err)
	}
	if !strings.Contains(m.sync.msg, "已推送") {
		t.Errorf("推送结果不对: %q", m.sync.msg)
	}
	if len(fake.files["alice/"+syncRepoName+"/"+syncRemoteFile]) == 0 {
		t.Fatal("TUI 推送之后仓库里没有同步文件")
	}
	if !strings.Contains(m.syncLine, "同步 alice/"+syncRepoName) {
		t.Errorf("列表上的同步状态没更新: %q", m.syncLine)
	}
}

// 已经记住密钥时不再问密码，直接执行
func TestSyncPageUsesCachedKey(t *testing.T) {
	_, _, _ = setupSyncTest(t)
	writeSyncTestState(t, "pw")
	m := newTUIModel()
	m.width, m.height = 100, 40
	m.state = syncPage
	m.sync = syncView{}

	updated, cmd := m.handleSyncKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(tuiModel)
	if m.sync.mode != syncIdle {
		t.Fatalf("记住密钥后不该再问密码，mode=%v", m.sync.mode)
	}
	if cmd == nil || !m.sync.busy {
		t.Fatalf("应该直接开始推送: cmd=%v busy=%v", cmd, m.sync.busy)
	}
}

// 同步页里加路径：走和 CLI 同一个 addSyncPath，写完状态里能看到
func TestSyncPageAddPath(t *testing.T) {
	_, _, _ = setupSyncTest(t)
	writeSyncTestState(t, "pw")
	m := newTUIModel()
	m.state = syncPage
	m.sync = syncView{}

	m.sync.cursor = syncActionIndex(m, "addpath")
	if m.sync.cursor < 0 {
		t.Fatal("同步页里没有「添加要同步的文件」")
	}
	updated, _ := m.handleSyncKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(tuiModel)
	if m.sync.mode != syncPath {
		t.Fatalf("没进路径输入，mode=%v", m.sync.mode)
	}
	m = typeRunes(m, "~/.foo/bar")
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(tuiModel)
	if m.sync.err != "" {
		t.Fatalf("加路径失败: %v", m.sync.err)
	}
	if m.sync.mode != syncIdle || !strings.Contains(m.sync.msg, "home:.foo/bar") {
		t.Fatalf("加路径后的提示不对: mode=%v msg=%q", m.sync.mode, m.sync.msg)
	}
	st, err := loadSyncState()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.ExtraPaths) != 1 || st.ExtraPaths[0] != "home:.foo/bar" {
		t.Errorf("同步清单 = %v", st.ExtraPaths)
	}
}

// gh 没装时页面最上面给「安装 GitHub CLI」，选中就在这个页面里跑安装器
func TestSyncPageMissingGHSuggestsInstall(t *testing.T) {
	isolateConfig(t)
	old := ghInstaller
	ghInstaller = func() ghInstallPlan {
		return ghInstallPlan{"安装 GitHub CLI", "用 winget 装，装完回来自动接着登录", exec.Command("hmck-test-gh-installer")}
	}
	defer func() { ghInstaller = old }()

	m := newTUIModel()
	m.width, m.height = 100, 40
	m.state = syncPage
	m.sync = syncView{ghChecked: true}

	items := m.syncItems()
	if len(items) == 0 || items[0].action != "ghsetup" {
		t.Fatalf("gh 没装时菜单第一项应该是安装，得到 %+v", items)
	}
	got := plainText(m.viewSync())
	for _, want := range []string{"安装 GitHub CLI", "没装", "r 重新检测"} {
		if !strings.Contains(got, want) {
			t.Errorf("同步页里缺少 %q:\n%s", want, got)
		}
	}

	updated, cmd := m.handleSyncKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(tuiModel)
	if cmd == nil || !m.sync.busy {
		t.Fatalf("选中安装后应该去跑安装器: cmd=%v busy=%v err=%q", cmd, m.sync.busy, m.sync.err)
	}
}

// gh 装了没登录：菜单先给「登录 GitHub CLI」，推送这类动作先拦住
func TestSyncPageGHCheckUpdatesMenu(t *testing.T) {
	isolateConfig(t)
	m := newTUIModel()
	m.width, m.height = 100, 40
	m.state = syncPage

	updated, _ := m.Update(ghCheckMsg{found: true, loggedIn: false, version: "2.63.2"})
	m = updated.(tuiModel)
	if !m.sync.ghChecked || m.sync.ghLogin {
		t.Fatalf("检测结果没记住: %+v", m.sync)
	}
	items := m.syncItems()
	if items[0].action != "ghlogin" {
		t.Fatalf("没登录时第一项应该是登录，得到 %+v", items)
	}
	if got := plainText(m.viewSync()); !strings.Contains(got, "2.63.2") || !strings.Contains(got, "没登录") {
		t.Errorf("状态行没写清 gh 版本和登录状态:\n%s", got)
	}

	// 往下挪到「开始同步」，没登录时不该进密码输入
	m.sync.cursor = 1
	updated, _ = m.handleSyncKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(tuiModel)
	if m.sync.mode == syncPass || !strings.Contains(m.sync.err, "登录") {
		t.Fatalf("没登录时推送应该先被拦住: mode=%v err=%q", m.sync.mode, m.sync.err)
	}

	updated, _ = m.Update(ghCheckMsg{found: true, loggedIn: true, version: "2.63.2"})
	m = updated.(tuiModel)
	if m.syncItems()[0].action == "ghlogin" {
		t.Error("登录后不该再显示登录项")
	}
	if got := plainText(m.viewSync()); !strings.Contains(got, "已登录") {
		t.Errorf("登录后状态行没说已登录:\n%s", got)
	}
}

// 安装器退出后重新检测：装上了就提示去登录，没装上就把退出码说出来
func TestSyncPageInstallResult(t *testing.T) {
	m := newTUIModel()
	m.state = syncPage
	m.sync = syncView{busy: true, ghRetry: "install", setupErr: errors.New("exit status 1")}

	updated, cmd := m.Update(ghCheckMsg{found: true, loggedIn: false, version: "2.63.2"})
	m = updated.(tuiModel)
	if cmd != nil {
		t.Errorf("检测完不该再排别的命令: %v", cmd)
	}
	if !strings.Contains(m.sync.msg, "登录 GitHub CLI") || m.sync.err != "" {
		t.Fatalf("装上之后应该提示去登录: msg=%q err=%q", m.sync.msg, m.sync.err)
	}

	m.sync = syncView{busy: true, ghRetry: "install", setupErr: errors.New("exit status 1")}
	updated, _ = m.Update(ghCheckMsg{found: false})
	m = updated.(tuiModel)
	if !strings.Contains(m.sync.err, "安装没成功") || !strings.Contains(m.sync.err, "exit status 1") {
		t.Fatalf("安装失败应该带上原因: %q", m.sync.err)
	}
}

// syncActionIndex 在同步页菜单里找一个动作的下标；找不到返回 -1
func syncActionIndex(m tuiModel, action string) int {
	for i, it := range m.syncItems() {
		if it.action == action {
			return i
		}
	}
	return -1
}

func TestSyncPreviewPageScrollsAndFitsShortTerminal(t *testing.T) {
	m := newTUIModel()
	m.width, m.height = 100, 12
	m.state = syncPage
	file := syncPreviewFile{Name: "home:.config/example.yaml", Status: "变更"}
	for i := 0; i < 20; i++ {
		file.Diff = append(file.Diff, fmt.Sprintf("+ changed-line-%02d", i))
	}
	m.sync = syncView{mode: syncDiff, msg: "预览：会写入 1 个文件", preview: []syncPreviewFile{file}}

	view := plainText(m.viewSync())
	if !strings.Contains(view, "changed-line-00") || strings.Contains(view, "changed-line-03") {
		t.Fatalf("预览页首屏窗口不正确:\n%s", view)
	}
	if got := strings.Count(m.viewSync(), "\n"); got > m.height {
		t.Fatalf("短终端预览页占 %d 行，超过高度 %d", got, m.height)
	}

	updated, _ := m.handleSyncKey(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(tuiModel)
	view = plainText(m.viewSync())
	if !strings.Contains(view, "changed-line-03") || m.sync.previewScroll != 1 {
		t.Fatalf("预览页没有随按键滚动:\n%s", view)
	}
	updated, _ = m.handleSyncKey(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(tuiModel)
	if m.sync.mode != syncIdle || len(m.sync.preview) != 0 {
		t.Fatal("按 esc 应返回同步菜单并清理预览数据")
	}
}

// 同步页要把参与同步的设备列出来，本机那台标「这台」
func TestSyncPageShowsDevices(t *testing.T) {
	_, _, _ = setupSyncTest(t)
	writeSyncTestState(t, "pw")
	st, err := loadSyncState()
	if err != nil {
		t.Fatal(err)
	}
	st.DeviceID = "aaaa1111"
	st.Devices = []syncDevice{
		{ID: "aaaa1111", Name: "mac-mini", OS: "darwin", LastPush: "2026-11-24T01:00:00Z"},
		{ID: "bbbb2222", Name: "win-11", OS: "windows", LastPull: "2026-11-23T02:00:00Z"},
	}
	if _, err := saveSyncState(st); err != nil {
		t.Fatal(err)
	}

	m := newTUIModel()
	m.width, m.height = 100, 40
	m.state = syncPage
	m.sync = syncView{ghChecked: true, ghFound: true, ghLogin: true}
	view := m.viewSync()
	for _, want := range []string{"设备", "2 台", "mac-mini（这台）", "win-11"} {
		if !strings.Contains(view, want) {
			t.Errorf("同步页里没有 %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "（这台）win-11") {
		t.Errorf("别的设备不该标「这台」:\n%s", view)
	}
}

// 页面矮的时候设备压成一行，名字还得看得到，别把菜单挤出屏幕
func TestSyncPageDevicesCompactOnShortTerminal(t *testing.T) {
	_, _, _ = setupSyncTest(t)
	writeSyncTestState(t, "pw")
	st, err := loadSyncState()
	if err != nil {
		t.Fatal(err)
	}
	st.DeviceID = "aaaa1111"
	st.Devices = []syncDevice{
		{ID: "aaaa1111", Name: "mac-mini", LastPush: "2026-11-24T01:00:00Z"},
		{ID: "bbbb2222", Name: "win-11", LastPush: "2026-11-23T02:00:00Z"},
		{ID: "cccc3333", Name: "ci-box", LastPush: "2026-11-22T02:00:00Z"},
	}
	if _, err := saveSyncState(st); err != nil {
		t.Fatal(err)
	}

	m := newTUIModel()
	m.width, m.height = 100, 26
	m.state = syncPage
	m.sync = syncView{ghChecked: true, ghFound: true, ghLogin: true}
	view := m.viewSync()
	if !strings.Contains(view, "win-11") {
		t.Errorf("矮屏上设备名也该在:\n%s", view)
	}
	if got := strings.Count(view, "\n"); m.height > 0 && got > m.height {
		t.Errorf("同步页 %d 行，超过终端高度 %d", got, m.height)
	}
}

// 选「刷新设备列表」：从远端解密只更新设备缓存，不动 LastSHA
func TestSyncPageRefreshDevices(t *testing.T) {
	_, home, _ := setupSyncTest(t)
	if err := os.MkdirAll(filepath.Join(home, ".argocd"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".argocd", "token"), []byte("t"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeSyncTestState(t, "pw")
	if _, err := syncPush(syncOptions{}); err != nil {
		t.Fatalf("push: %v", err)
	}

	// 本机把设备缓存丢了（换过 sync.json、或者从旧版本升上来）
	st, err := loadSyncState()
	if err != nil {
		t.Fatal(err)
	}
	st.Devices = nil
	if _, err := saveSyncState(st); err != nil {
		t.Fatal(err)
	}

	m := newTUIModel()
	m.width, m.height = 100, 40
	m.state = syncPage
	m.sync = syncView{ghChecked: true, ghFound: true, ghLogin: true, cursor: syncActionIndex(m, "devices")}
	if m.sync.cursor < 0 {
		t.Fatal("同步页里没有「刷新设备列表」")
	}
	updated, cmd := m.handleSyncKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(tuiModel)
	if cmd == nil || !m.sync.busy {
		t.Fatalf("选刷新后应该开始跑: cmd=%v busy=%v", cmd, m.sync.busy)
	}
	updated, _ = m.Update(cmd())
	m = updated.(tuiModel)
	if m.sync.err != "" {
		t.Fatalf("刷新失败: %v", m.sync.err)
	}
	if !strings.Contains(m.sync.msg, "设备列表已更新：1 台") {
		t.Errorf("刷新结果不对: %q", m.sync.msg)
	}
	after, err := loadSyncState()
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Devices) != 1 || after.Devices[0].ID != st.DeviceID {
		t.Fatalf("设备缓存没补回来: %+v", after.Devices)
	}
	if after.LastSHA != st.LastSHA {
		t.Errorf("刷新不该动 LastSHA: %q -> %q", st.LastSHA, after.LastSHA)
	}
}

func TestSyncToolSettingsAvailableAndPersistLocally(t *testing.T) {
	fake, _, _ := setupSyncTest(t)
	m := newTUIModel()
	m.width, m.height = 100, 30
	m.state = syncPage
	m.sync = syncView{}

	// 未连接仓库时也能进入逐工具设置。
	idx := syncActionIndex(m, "tools")
	if idx < 0 {
		t.Fatal("首次同步菜单缺少逐工具设置")
	}
	updated, _ := m.syncChoose(m.syncItems()[idx])
	m = updated.(tuiModel)
	if m.sync.mode != syncToolsMode {
		t.Fatalf("没有进入逐工具设置页: mode=%v", m.sync.mode)
	}
	view := plainText(m.viewSync())
	for _, want := range []string{"逐工具同步设置", "argo-token", "[开]", "远端数据保留"} {
		if !strings.Contains(view, want) {
			t.Errorf("逐工具设置页缺少 %q:\n%s", want, view)
		}
	}
	updated, _ = m.handleSyncKey(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(tuiModel)
	if m.sync.mode != syncIdle {
		t.Fatal("esc 应返回同步菜单")
	}

	// 连上仓库后菜单仍保留同一入口。
	writeSyncTestState(t, "fixture-passphrase")
	m.sync = syncView{ghChecked: true, ghFound: true, ghLogin: true}
	idx = syncActionIndex(m, "tools")
	if idx < 0 {
		t.Fatal("已连接同步菜单缺少逐工具设置")
	}
	updated, _ = m.syncChoose(m.syncItems()[idx])
	m = updated.(tuiModel)

	tools, err := syncableTools()
	if err != nil {
		t.Fatal(err)
	}
	argoIndex := -1
	for i, tool := range tools {
		if tool.Key == "argo-token" {
			argoIndex = i
			break
		}
	}
	if argoIndex < 0 {
		t.Fatal("测试所需的内置同步工具不在列表里")
	}
	m.sync.toolCursor = argoIndex
	putsBefore := fake.puts
	updated, _ = m.handleSyncKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(tuiModel)
	st, err := loadSyncState()
	if err != nil {
		t.Fatal(err)
	}
	if syncToolEnabled(st, "argo-token") {
		t.Fatal("TUI 关闭工具后没有写入本机禁用设置")
	}
	if !strings.Contains(plainText(m.viewSync()), "[关]") || !strings.Contains(m.sync.msg, "远端数据保留") {
		t.Fatal("关闭后的状态和远端保留提示没有显示")
	}
	if fake.puts != putsBefore {
		t.Fatal("本机逐工具偏好不应触发同步推送")
	}

	updated, _ = m.handleSyncKey(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(tuiModel)
	st, err = loadSyncState()
	if err != nil {
		t.Fatal(err)
	}
	if !syncToolEnabled(st, "argo-token") || len(st.DisabledTools) != 0 {
		t.Fatalf("重新开启后本机设置未恢复默认开启: %+v", st.DisabledTools)
	}
}
