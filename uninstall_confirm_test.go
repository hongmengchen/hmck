package main

import (
	"bytes"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// 确认页上敲错名字不能删；敲对了才进卸载
func TestTUIConfirmNeedsTypedName(t *testing.T) {
	sb := newUninstallSandbox(t)
	tl := tool{Key: "kube-token", Binary: "kube-token"}
	writeFile(t, filepath.Join(sb.bin, binaryName(tl, runtime.GOOS)), "bin")

	m := newTUIModel()
	m.state = confirmUninstall
	m.confirm = uninstallView{tool: toolState{Tool: tl}}

	// 敲了一半就回车：还在确认页，并说明名字不对
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("kube")})
	m = updated.(tuiModel)
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(tuiModel)
	if m.state != confirmUninstall || cmd != nil {
		t.Fatalf("名字没敲对就不该卸载: state=%v cmd=%v", m.state, cmd)
	}
	if !strings.Contains(m.confirm.err, "名字不对") {
		t.Errorf("该提示名字不对，得到 %q", m.confirm.err)
	}

	// 敲完再回车：才真去卸载
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("-token")})
	m = updated.(tuiModel)
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(tuiModel)
	if m.state != working || cmd == nil {
		t.Fatalf("名字敲对了应当卸载: state=%v cmd=%v", m.state, cmd)
	}
}

// esc 随时能退回去，而且清空已经敲的字
func TestTUIConfirmEscCancels(t *testing.T) {
	m := newTUIModel()
	m.state = confirmUninstall
	m.confirm = uninstallView{tool: toolState{Tool: tool{Key: "glab"}}, typed: "gl"}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(tuiModel)
	if m.state != list || cmd != nil {
		t.Fatalf("esc 应当回到列表: state=%v cmd=%v", m.state, cmd)
	}
	if m.confirm.typed != "" || m.confirm.tool.Tool.Key != "" {
		t.Errorf("确认页状态该清空: %+v", m.confirm)
	}
}

// q 是工具名里会出现的字母，确认页里要能敲进去，不能当退出
func TestTUIConfirmTypesQ(t *testing.T) {
	m := newTUIModel()
	m.state = confirmUninstall
	m.confirm = uninstallView{tool: toolState{Tool: tool{Key: "q-tool"}}}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	m = updated.(tuiModel)
	if cmd != nil {
		t.Fatalf("敲 q 不该退出: cmd=%v", cmd)
	}
	if m.confirm.typed != "q" {
		t.Errorf("typed = %q", m.confirm.typed)
	}
}

// 确认页要看得到「输入什么」和已经敲进去的字
func TestTUIConfirmShowsPrompt(t *testing.T) {
	m := newTUIModel()
	m.width, m.height = 120, 40
	m.state = confirmUninstall
	m.confirm = uninstallView{tool: toolState{Tool: tool{Key: "kube-token"}}, typed: "kube-tok"}

	got := plainText(m.viewConfirm())
	for _, want := range []string{"输入 kube-token 再按 enter 删除", "kube-tok"} {
		if !strings.Contains(got, want) {
			t.Errorf("确认页缺 %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "按 y") {
		t.Errorf("确认页不该再提按 y:\n%s", got)
	}
}

// 命令行先摊清单，再要人敲工具名；没有终端（脚本 / agent）时直接拒绝
func TestConfirmUninstallCLI(t *testing.T) {
	sb := newUninstallSandbox(t)
	tl := tool{Key: "kube-token", Binary: "kube-token"}
	writeFile(t, filepath.Join(sb.bin, binaryName(tl, runtime.GOOS)), "bin")
	st := inspectLocal(tl)

	var out bytes.Buffer
	if code := confirmUninstallCLI(strings.NewReader("kube-token\n"), &out, st, false); code != 2 {
		t.Errorf("非交互时不加 --yes 应当拒绝并返回 2，得到 %d", code)
	}
	if !strings.Contains(out.String(), "--yes") {
		t.Errorf("该告诉人加 --yes:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "删除") || !strings.Contains(out.String(), "kube-token") {
		t.Errorf("该先摊开要删的清单:\n%s", out.String())
	}

	out.Reset()
	if code := confirmUninstallCLI(strings.NewReader("yes\n"), &out, st, true); code != 1 {
		t.Errorf("敲错名字应当取消并返回 1，得到 %d", code)
	}
	if !strings.Contains(out.String(), "已取消") {
		t.Errorf("该说明已取消:\n%s", out.String())
	}

	out.Reset()
	if code := confirmUninstallCLI(strings.NewReader("  Kube-Token \n"), &out, st, true); code != 0 {
		t.Errorf("敲对名字（忽略大小写和空格）应当放行，得到 %d\n%s", code, out.String())
	}

	out.Reset()
	if code := confirmUninstallCLI(strings.NewReader(""), &out, st, true); code != 1 {
		t.Errorf("直接回车应当取消，得到 %d", code)
	}
}
