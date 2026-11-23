package main

import (
	"fmt"
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
	m.sync = syncView{cursor: 5} // 「添加要同步的文件」

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
