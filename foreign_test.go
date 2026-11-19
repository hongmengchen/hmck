package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// brew 装好、已经登录的 glab：hmck 只查看和启动它。
// 按 enter 必须直接启动 PATH 上那份，不能顺手往 hmck 目录里再装一份——
// 多出来的副本排在 PATH 前面，会把用户和 agent 一直在用的那份顶掉
func TestEnterLaunchesForeignCopy(t *testing.T) {
	m := newTUIModel()
	m.state = list
	m.localReady = true
	m.states = []toolState{{
		Tool:    tool{Key: "glab", Binary: "glab", OwnDir: true},
		Foreign: []string{"/opt/homebrew/bin/glab"},
	}}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := updated.(tuiModel)
	if got.state != list {
		t.Fatalf("按 enter 不该离开列表去安装: state=%v workText=%q", got.state, got.workText)
	}
	if cmd == nil {
		t.Fatal("按 enter 应当启动 PATH 上那份 glab")
	}
}

// 本机哪儿都没有这个工具时，按 enter 才是安装
func TestEnterInstallsWhenNothingOnMachine(t *testing.T) {
	isolateConfig(t)
	m := newTUIModel()
	m.state = list
	m.localReady = true
	m.states = []toolState{{Tool: tool{Key: "tool-a", Binary: "tool-a", OwnDir: true}}}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := updated.(tuiModel)
	if got.state != working {
		t.Fatalf("什么都没装时按 enter 应当安装: state=%v", got.state)
	}
	if !strings.Contains(got.workText, "安装") {
		t.Errorf("workText = %q", got.workText)
	}
	if cmd == nil {
		t.Error("安装应当返回命令")
	}
}

// u 键也一样：不归 hmck 管的那份只说清楚用的是哪一份，不另装
func TestUpgradeOneExplainsForeignCopy(t *testing.T) {
	isolateConfig(t)
	m := newTUIModel()
	m.states = []toolState{{
		Tool:       tool{Key: "glab", Binary: "glab", OwnDir: true},
		Foreign:    []string{"/opt/homebrew/bin/glab"},
		ForeignVer: "1.131.0",
	}}

	updated, cmd := m.upgradeOne(m.states[0])
	got := updated.(tuiModel)
	if cmd != nil {
		t.Error("u 不该再下载安装一份")
	}
	if got.state != result {
		t.Fatalf("state = %v", got.state)
	}
	all := ""
	for _, l := range got.resultLines {
		all += l.text + "\n"
	}
	for _, want := range []string{"/opt/homebrew/bin/glab", "1.131.0", "不会另装一份"} {
		if !strings.Contains(all, want) {
			t.Errorf("结果页缺 %q:\n%s", want, all)
		}
	}
}

// 第一次装和替换现有的提示要分开
func TestStartInstallWording(t *testing.T) {
	isolateConfig(t)
	cases := []struct {
		name     string
		instaled string
		want     string
	}{
		{"第一次安装", "", "正在下载并安装"},
		{"替换现有的", "0.9.0", "正在下载并替换"},
	}
	for _, c := range cases {
		m := newTUIModel()
		st := toolState{Tool: tool{Key: "tool-a", Binary: "tool-a"}, Installed: c.instaled, Latest: "1.0.0"}
		updated, _ := m.startInstall(st)
		if got := updated.(tuiModel); !strings.HasPrefix(got.workText, c.want) {
			t.Errorf("%s: workText = %q", c.name, got.workText)
		}
	}
}

// 本机只有 PATH 上那一份时，toolBinary 要用它，别假装没装
func TestToolBinaryUsesForeignCopy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH 分隔符在这里不相关")
	}
	oldExe := runningExe
	runningExe = func() (string, error) { return filepath.Join(t.TempDir(), "hmck"), nil }
	defer func() { runningExe = oldExe }()

	dir := t.TempDir()
	tl := tool{Key: "glab", Binary: "glab", OwnDir: true}
	p := filepath.Join(dir, binaryName(tl, runtime.GOOS))
	if err := os.WriteFile(p, []byte("foreign"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	if got := toolBinary(tl); got != realPath(p) {
		t.Errorf("toolBinary = %q, want %q", got, realPath(p))
	}
}

// 状态和列表数据都要说明「用的是 PATH 上那份」
func TestForeignStateInStatusAndList(t *testing.T) {
	st := toolState{
		Tool:       tool{Key: "glab", Name: "glab", OwnDir: true},
		Foreign:    []string{"/opt/homebrew/bin/glab"},
		ForeignVer: "1.131.0",
	}
	if label, kind := st.Status(); label != "用 PATH 上的 1.131.0" || kind != "warn" {
		t.Errorf("Status = %q/%q", label, kind)
	}
	it := toListItem(st)
	if !it.Foreign || !strings.HasSuffix(it.InUse, "/opt/homebrew/bin/glab") {
		t.Errorf("列表项 = %+v", it)
	}
}

// 卡片上写明 PATH 上已有一份，别让人以为它没装
func TestCardShowsForeignNote(t *testing.T) {
	m := newTUIModel()
	m.width, m.height = 120, 40
	m.localReady = true
	m.pending = map[string]bool{}
	m.states = []toolState{{
		Tool:       tool{Key: "glab", Name: "glab", Desc: "GitLab 官方 CLI", OwnDir: true},
		Foreign:    []string{"/opt/homebrew/bin/glab"},
		ForeignVer: "1.131.0",
	}}

	got := plainText(m.viewList())
	if !strings.Contains(got, "PATH 上已有一份") {
		t.Errorf("卡片上没写 PATH 上已有一份:\n%s", got)
	}
}

// shadowNote 按 PATH 顺序算装完以后终端里会用哪份
func TestShadowNotePathOrder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH 分隔符在这里不相关")
	}
	home := t.TempDir()
	early, late := filepath.Join(home, "early"), filepath.Join(home, "late")
	t.Setenv("PATH", strings.Join([]string{early, late}, string(os.PathListSeparator)))

	note, kind := shadowNote(filepath.Join(early, "glab"), filepath.Join(late, "glab"))
	if !strings.Contains(note, "先用 hmck 装的这份") || kind != "warn" {
		t.Errorf("装在前面的目录: %q/%q", note, kind)
	}
	note, kind = shadowNote(filepath.Join(late, "glab"), filepath.Join(early, "glab"))
	if !strings.Contains(note, "还是原来那份") || kind != "ok" {
		t.Errorf("装在后面的目录: %q/%q", note, kind)
	}
	if note, _ := shadowNote("", "/opt/homebrew/bin/glab"); note != "" {
		t.Errorf("算不出目标位置时不该下结论: %q", note)
	}
}
