package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func typeRunes(m tuiModel, s string) tuiModel {
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)})
	return updated.(tuiModel)
}

func pressKey(m tuiModel, key tea.KeyType) tuiModel {
	updated, _ := m.Update(tea.KeyMsg{Type: key})
	return updated.(tuiModel)
}

// 列表里按 n 先到工具目录，再按 a 打开按仓库添加的表单
func TestAddToolFormOpens(t *testing.T) {
	isolateConfig(t)
	m := newTUIModel()
	m.width, m.height = 100, 30
	m.localReady = true
	m.pending = map[string]bool{}

	m = typeRunes(m, "n")
	if m.state != catalog {
		t.Fatalf("按 n 之后 state = %v，想要工具目录", m.state)
	}
	m = typeRunes(m, "a")
	if m.state != addTool {
		t.Fatalf("目录里按 a 之后 state = %v，想要新增表单", m.state)
	}
	view := plainText(m.viewAdd())
	for _, want := range []string{"新增工具", "仓库", "owner/name", "添加并安装"} {
		if !strings.Contains(view, want) {
			t.Errorf("新增表单缺少 %q:\n%s", want, view)
		}
	}

	m = pressKey(m, tea.KeyEsc)
	if m.state != list {
		t.Errorf("esc 之后 state = %v，想要回列表", m.state)
	}
}

// 填完仓库按 enter：先落到「读取发布信息」，再由结果消息接着装
func TestAddToolSubmit(t *testing.T) {
	isolateConfig(t)
	m := newTUIModel()
	m.width, m.height = 100, 30
	m.localReady = true
	m.pending = map[string]bool{}
	m.state = addTool

	m = typeRunes(m, "https://github.com/me/webhook")
	m = pressKey(m, tea.KeyTab)
	if m.add.field != 1 {
		t.Fatalf("tab 之后停在 %d 号格子", m.add.field)
	}
	m = pressKey(m, tea.KeyEnter)
	if m.state != working {
		t.Fatalf("enter 之后 state = %v，想要 working", m.state)
	}
	if !strings.Contains(m.workText, "me/webhook") {
		t.Errorf("读取中的提示里没有仓库名: %q", m.workText)
	}

	// 读到了发布信息：工具进列表、光标跟过去、顺手开装
	updated, _ := m.Update(addToolMsg{t: tool{Key: "webhook", Repo: "me/webhook"}})
	m = updated.(tuiModel)
	if last := m.states[len(m.states)-1]; last.Tool.Key != "webhook" {
		t.Fatalf("结果没进列表: %+v", m.states)
	}
	if m.cursor != len(m.states)-1 {
		t.Errorf("光标应该停在新增的那个上，got %d", m.cursor)
	}
	if m.state != working || !strings.Contains(m.workText, "webhook") {
		t.Errorf("新增完应该直接开装，state = %v, workText = %q", m.state, m.workText)
	}
}

// 仓库写错了就留在表单上，填过的内容不丢
func TestAddToolRejectsBadRepo(t *testing.T) {
	isolateConfig(t)
	m := newTUIModel()
	m.width, m.height = 100, 30
	m.state = addTool

	m = typeRunes(m, "webhook")
	m = pressKey(m, tea.KeyEnter)
	if m.state != addTool {
		t.Fatalf("仓库不合法还走了：state = %v", m.state)
	}
	if m.add.repo != "webhook" || m.add.err == "" {
		t.Errorf("应该留着输入并报错，repo = %q, err = %q", m.add.repo, m.add.err)
	}
	if !strings.Contains(plainText(m.viewAdd()), "owner/name") {
		t.Error("错误提示里该说清楚仓库怎么写")
	}
}

// 内置工具和已经有的一律不收
func TestAddToolRejectsDuplicate(t *testing.T) {
	isolateConfig(t)
	m := newTUIModel()
	m.width, m.height = 100, 30
	m.localReady = true
	m.states = []toolState{{Tool: tool{Key: "webhook", Repo: "me/webhook"}}}

	m.state = addTool
	m = typeRunes(m, "me/webhook")
	m = pressKey(m, tea.KeyEnter)
	if m.state != addTool || !strings.Contains(m.add.err, "已经在列表里") {
		t.Errorf("重复的 key 没挡住: state = %v, err = %q", m.state, m.add.err)
	}

	m.add = addView{repo: "someone/hmck", key: "hmck"}
	m = pressKey(m, tea.KeyEnter)
	if m.state != addTool || !strings.Contains(m.add.err, "内置工具") {
		t.Errorf("内置 key 没挡住: state = %v, err = %q", m.state, m.add.err)
	}
}

// 读发布失败时回到表单带着错误，可以改一改再试
func TestAddToolMsgErrorReturnsToForm(t *testing.T) {
	isolateConfig(t)
	m := newTUIModel()
	m.width, m.height = 100, 30
	m.state = working
	m.add = addView{repo: "me/webhook", key: "webhook"}

	updated, _ := m.Update(addToolMsg{err: errString("写不进配置文件")})
	m = updated.(tuiModel)
	if m.state != addTool {
		t.Fatalf("失败后 state = %v，想要回表单", m.state)
	}
	if m.add.repo != "me/webhook" || !strings.Contains(m.add.err, "写不进配置文件") {
		t.Errorf("表单内容或错误丢了: %+v", m.add)
	}
}

type errString string

func (e errString) Error() string { return string(e) }
