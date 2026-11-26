package main

import (
	"bytes"
	"io"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/vt"
)

// fakePTY 站在子进程那一头：记住我们发过去的字节，不发也不画。
// 真进程留给人工试，单测要的是确定。
type fakePTY struct {
	in     bytes.Buffer
	cols   int
	rows   int
	killed bool
}

func (f *fakePTY) Read(p []byte) (int, error)  { return 0, io.EOF }
func (f *fakePTY) Write(p []byte) (int, error) { return f.in.Write(p) }
func (f *fakePTY) Resize(cols, rows int) error {
	f.cols, f.rows = cols, rows
	return nil
}
func (f *fakePTY) Kill() error { f.killed = true; return nil }
func (f *fakePTY) Wait() error { return nil }

func newTestPane(key string, w, h int) (*pane, *fakePTY) {
	fc := &fakePTY{}
	p := &pane{
		tool:    tool{Key: key, TUI: true},
		term:    vt.NewSafeEmulator(w, h),
		child:   fc,
		ch:      make(chan tea.Msg, 1),
		done:    make(chan struct{}),
		width:   w,
		height:  h,
		focused: true,
	}
	return p, fc
}

// paneTestModel 是「列表里选中一个自己的工具、右边开着它的半屏」
func paneTestModel(t *testing.T) (tuiModel, *fakePTY) {
	t.Helper()
	if !paneSupported {
		t.Skip("这个平台没有伪终端，半屏本来就不出现")
	}
	isolateConfig(t)
	m := newTUIModel()
	m.width, m.height = 100, 30
	m.localReady = true
	m.pending = map[string]bool{}
	m.states = []toolState{
		{Tool: tool{Key: "kube-token", Kind: kindOwn, TUI: true, Desc: "更新 kubeconfig 里的 token"},
			Path: "/opt/homebrew/bin/kube-token", Installed: "0.8.0", Latest: "0.8.0"},
		{Tool: tool{Key: "glab", Kind: kindOfficial, Desc: "GitLab 官方 CLI"},
			Foreign: []string{"/opt/homebrew/bin/glab"}, ForeignVer: "1.130.0"},
	}
	m.cursor = 0
	l, ok := m.paneLayout()
	if !ok {
		t.Fatalf("100×30 应该摆得下两栏")
	}
	p, fc := newTestPane("kube-token", l.paneW, l.rows-1)
	m.pane = p
	return m, fc
}

// 常见按键要还原成终端里的那串字节，方向键不能丢
func TestPaneKeyBytes(t *testing.T) {
	cases := []struct {
		msg  tea.KeyMsg
		want string
	}{
		{tea.KeyMsg{Type: tea.KeyUp}, "\x1b[A"},
		{tea.KeyMsg{Type: tea.KeyDown}, "\x1b[B"},
		{tea.KeyMsg{Type: tea.KeyEnter}, "\r"},
		{tea.KeyMsg{Type: tea.KeyTab}, "\t"},
		{tea.KeyMsg{Type: tea.KeyEsc}, "\x1b"},
		{tea.KeyMsg{Type: tea.KeyBackspace}, "\x7f"},
		{tea.KeyMsg{Type: tea.KeyCtrlC}, "\x03"},
		{tea.KeyMsg{Type: tea.KeySpace}, " "},
		{tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")}, "k"},
	}
	for _, c := range cases {
		if got := string(paneKeyBytes(c.msg)); got != c.want {
			t.Errorf("%s → %q，想要 %q", c.msg.String(), got, c.want)
		}
	}
}

// 半屏开着的时候，键盘先给子进程：列表光标不动，字节原样发过去；
// tab 把键盘换回来，之后列表照常能选
func TestPaneFocusRoutesKeys(t *testing.T) {
	m, fc := paneTestModel(t)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	got := updated.(tuiModel)
	if got.cursor != 0 {
		t.Errorf("专注在面板时 j 不该动列表光标: cursor=%d", got.cursor)
	}
	if fc.in.String() != "j" {
		t.Errorf("j 应当原样发给子进程，got %q", fc.in.String())
	}

	updated, _ = got.Update(tea.KeyMsg{Type: tea.KeyTab})
	got = updated.(tuiModel)
	if got.pane == nil || got.pane.focused {
		t.Fatalf("tab 应该把键盘交回列表")
	}

	updated, _ = got.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if updated.(tuiModel).cursor != 1 {
		t.Errorf("交回列表后 j 应当移到下一个工具")
	}
	if fc.in.String() != "j" {
		t.Errorf("交回列表后按键不该再发给子进程，got %q", fc.in.String())
	}
}

// 不专注时 esc 收起面板，子进程一起结束；列表还在
func TestPaneEscClosesAndKills(t *testing.T) {
	m, fc := paneTestModel(t)
	m.pane.focused = false

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	got := updated.(tuiModel)
	if got.pane != nil {
		t.Fatal("esc 应该收起右侧面板")
	}
	if !fc.killed {
		t.Error("收起面板要顺手结束子进程")
	}
	if got.state != list || cmd != nil {
		t.Errorf("收起后应当留在列表: state=%v cmd=%v", got.state, cmd)
	}
}

// 子进程退出了：面板留着给人看最后一眼，p 收起，tab 也不再进去
func TestPaneExitKeepsScreen(t *testing.T) {
	m, _ := paneTestModel(t)
	m.pane.term.Write([]byte("done!"))
	updated, _ := m.Update(paneExitMsg{p: m.pane, err: nil})
	got := updated.(tuiModel)
	if got.pane == nil || !got.pane.exited {
		t.Fatal("退出后面板应当还留着")
	}
	if got.pane.focused {
		t.Error("子进程退出后不该还占着键盘")
	}
	hint := plainText(got.listHints())
	if !strings.Contains(hint, "p 收起") || strings.Contains(hint, "tab 到右侧") {
		t.Errorf("退出后的提示不对:\n%s", hint)
	}
	if !strings.Contains(plainText(got.View()), "done!") {
		t.Errorf("退出前的画面应当还在:\n%s", plainText(got.View()))
	}
}

// 并排渲染：左边列表、右边子进程的屏幕、底部提示横跨整屏
func TestPaneSplitView(t *testing.T) {
	m, _ := paneTestModel(t)
	m.pane.term.Write([]byte("kube-token 的界面"))
	view := plainText(m.View())

	for _, want := range []string{"kube-token", "glab", "kube-token 的界面", "tab 回到列表", "│"} {
		if !strings.Contains(view, want) {
			t.Errorf("并排画面里缺 %q:\n%s", want, view)
		}
	}
	if got := strings.Count(view, "\n") + 1; got > m.height {
		t.Errorf("并排画面画了 %d 行，终端只有 %d 行", got, m.height)
	}
}

// 只有「自己的工具、而且本机上有那一份」才摆 p；官方 CLI 和太窄的终端都不摆
func TestPaneHintOnlyWhenUsable(t *testing.T) {
	isolateConfig(t)
	m := newTUIModel()
	m.width, m.height = 100, 30
	m.localReady = true
	m.pending = map[string]bool{}
	m.states = []toolState{{
		Tool: tool{Key: "kube-token", Kind: kindOwn, TUI: true},
		Path: "/opt/homebrew/bin/kube-token", Installed: "0.8.0",
	}}

	hint := plainText(m.listHints())
	if paneSupported {
		if !strings.Contains(hint, "p 右侧打开") {
			t.Errorf("自己的工具应当摆 p:\n%s", hint)
		}
	} else if strings.Contains(hint, "右侧打开") {
		t.Errorf("没有伪终端的平台上不该摆 p:\n%s", hint)
	}

	m.states[0] = toolState{Tool: tool{Key: "glab", Kind: kindOfficial},
		Foreign: []string{"/opt/homebrew/bin/glab"}}
	if hint := plainText(m.listHints()); strings.Contains(hint, "右侧打开") {
		t.Errorf("官方 CLI 不该摆 p:\n%s", hint)
	}

	m.states[0] = toolState{Tool: tool{Key: "kube-token", Kind: kindOwn, TUI: true},
		Path: "/opt/homebrew/bin/kube-token", Installed: "0.8.0"}
	m.width = 70
	if hint := plainText(m.listHints()); strings.Contains(hint, "右侧打开") {
		t.Errorf("终端太窄时不该摆一个按了没用的 p:\n%s", hint)
	}
}

// 面板开着时按其他键去了别的页面，面板要收掉，不能在后台偷偷跑
func TestPaneClosesWhenLeavingList(t *testing.T) {
	m, fc := paneTestModel(t)
	m.pane.focused = false

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	got := updated.(tuiModel)
	if got.state == list {
		t.Fatal("i 应当进信息页")
	}
	if got.pane != nil || !fc.killed {
		t.Error("离开列表时右侧面板应当收起并结束子进程")
	}
}
