package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	lAccent = lipgloss.AdaptiveColor{Light: "#6D28D9", Dark: "#A78BFA"}
	lDim    = lipgloss.AdaptiveColor{Light: "#6B7280", Dark: "#7C8794"}
	lText   = lipgloss.AdaptiveColor{Light: "#111827", Dark: "#E5E7EB"}
	lGood   = lipgloss.AdaptiveColor{Light: "#047857", Dark: "#34D399"}
	lBad    = lipgloss.AdaptiveColor{Light: "#B91C1C", Dark: "#F87171"}
	lWarn   = lipgloss.AdaptiveColor{Light: "#B45309", Dark: "#FBBF24"}
	lLine   = lipgloss.AdaptiveColor{Light: "#E5E7EB", Dark: "#333A45"}
)

var (
	sTitle   = lipgloss.NewStyle().Bold(true).Foreground(lAccent)
	sDim     = lipgloss.NewStyle().Foreground(lDim)
	sText    = lipgloss.NewStyle().Foreground(lText)
	sGood    = lipgloss.NewStyle().Foreground(lGood).Bold(true)
	sBad     = lipgloss.NewStyle().Foreground(lBad)
	sWarn    = lipgloss.NewStyle().Foreground(lWarn)
	sAccent  = lipgloss.NewStyle().Foreground(lAccent)
	sKey     = lipgloss.NewStyle().Foreground(lAccent).Bold(true)
	sCardOn  = lipgloss.NewStyle().Border(lipgloss.NormalBorder(), false, false, false, true).BorderForeground(lAccent).PaddingLeft(1)
	sCardOff = lipgloss.NewStyle().Border(lipgloss.NormalBorder(), false, false, false, true).BorderForeground(lLine).PaddingLeft(1)
)

type tuiState int

const (
	loading tuiState = iota
	list
	working
	result
)

type tuiModel struct {
	states []toolState
	cursor int
	state  tuiState

	width, height int
	spin          spinner.Model

	workText  string
	resultMsg string
	resultOK  bool
}

type inspectDoneMsg struct{ states []toolState }

type installDoneMsg struct {
	key     string
	version string
	err     error
}

type execDoneMsg struct{ err error }

func newTUIModel() tuiModel {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = sAccent
	return tuiModel{state: loading, spin: sp}
}

func (m tuiModel) Init() tea.Cmd {
	return tea.Batch(m.spin.Tick, inspectCmd())
}

func inspectCmd() tea.Cmd {
	return func() tea.Msg { return inspectDoneMsg{states: inspectAll()} }
}

func installCmd(st toolState) tea.Cmd {
	return func() tea.Msg {
		v, err := installTool(st)
		return installDoneMsg{key: st.Tool.Key, version: v, err: err}
	}
}

func launchCmd(st toolState) tea.Cmd {
	cmd := exec.Command(st.Path)
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		return execDoneMsg{err: err}
	})
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height

	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		return m.handleKey(msg)

	case spinner.TickMsg:
		if m.state != loading && m.state != working {
			return m, nil
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case inspectDoneMsg:
		m.states = msg.states
		m.state = list
		m.clampCursor()

	case installDoneMsg:
		m.state = result
		if msg.err != nil {
			m.resultOK = false
			m.resultMsg = fmt.Sprintf("%s 升级失败: %v", msg.key, msg.err)
		} else {
			m.resultOK = true
			m.resultMsg = fmt.Sprintf("%s 已升级到 %s", msg.key, msg.version)
		}
		// 本地版本立即刷新，不用再联网
		for i := range m.states {
			if m.states[i].Tool.Key != msg.key {
				continue
			}
			if msg.version != "" {
				m.states[i].Installed = msg.version
				m.states[i].Latest = msg.version
			}
			if p, ok := findTool(m.states[i].Tool); ok {
				m.states[i].Path = p
			}
		}

	case execDoneMsg:
		m.state = list
		if msg.err != nil {
			m.resultOK = false
			m.resultMsg = "工具退出时出错: " + msg.err.Error()
			m.state = result
		}
	}
	return m, nil
}

func (m tuiModel) handleKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.state {
	case loading, working:
		return m, nil

	case list:
		switch k.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.states)-1 {
				m.cursor++
			}
		case "enter":
			if len(m.states) == 0 {
				return m, nil
			}
			st := m.states[m.cursor]
			if st.Missing() || st.Path == "" {
				return m.startInstall(st)
			}
			return m, launchCmd(st)
		case "u":
			if len(m.states) > 0 {
				return m.startInstall(m.states[m.cursor])
			}
		case "r":
			m.state = loading
			return m, tea.Batch(m.spin.Tick, inspectCmd())
		case "q", "esc":
			return m, tea.Quit
		}

	case result:
		m.state = loading
		return m, tea.Batch(m.spin.Tick, inspectCmd())
	}
	return m, nil
}

func (m tuiModel) startInstall(st toolState) (tea.Model, tea.Cmd) {
	if st.Installed != "" && st.Latest != "" && !isNewer(st.Latest, st.Installed) {
		m.state = result
		m.resultOK = true
		m.resultMsg = st.Tool.Key + " " + st.Installed + " 已是最新"
		return m, nil
	}
	m.state = working
	m.workText = "正在下载并替换 " + st.Tool.Key + "…"
	return m, tea.Batch(m.spin.Tick, installCmd(st))
}

func (m *tuiModel) clampCursor() {
	if m.cursor >= len(m.states) {
		m.cursor = len(m.states) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

// ---------- 渲染 ----------

func (m tuiModel) View() string {
	var body string
	switch m.state {
	case loading:
		body = m.viewLoading()
	case list:
		body = m.viewList()
	case working:
		body = m.viewWorking()
	case result:
		body = m.viewResult()
	}
	return "\n" + body + "\n"
}

func (m tuiModel) viewLoading() string {
	return sTitle.Render("hmck") + sDim.Render("  检查工具版本") + "\n\n" +
		"  " + m.spin.View() + "  " + sDim.Render("正在问 GitHub 最新版本…")
}

func (m tuiModel) viewList() string {
	var b strings.Builder
	b.WriteString(sTitle.Render("hmck") + sDim.Render("  我的命令行小工具"))
	b.WriteString("\n")

	behind, missing := 0, 0
	for _, st := range m.states {
		if st.Behind() {
			behind++
		}
		if st.Missing() {
			missing++
		}
	}
	summary := fmt.Sprintf("  %d 个工具", len(m.states))
	if behind > 0 {
		summary += fmt.Sprintf(" · %d 个可升级", behind)
	}
	if missing > 0 {
		summary += fmt.Sprintf(" · %d 个未安装", missing)
	}
	b.WriteString(sDim.Render(summary))
	b.WriteString("\n\n")

	for i, st := range m.states {
		b.WriteString(m.renderCard(st, i == m.cursor))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(hints("↑↓", "选择", "enter", "启动/安装", "u", "升级", "r", "刷新", "q", "退出"))
	return b.String()
}

func (m tuiModel) renderCard(st toolState, selected bool) string {
	w := m.contentWidth()

	ver := st.Installed
	if ver == "" {
		ver = "未安装"
	}
	label := ver
	if st.Behind() {
		label = ver + " → " + st.Latest
	} else if st.Missing() && st.Latest != "" {
		label = "未安装 · 最新 " + st.Latest
	}
	status, style := statusStyle(st)

	head := sText.Render(st.Tool.Key) + "   " + sText.Render(label) + "   " + style.Render(status)

	desc := truncate(st.Tool.Desc, w)
	line2 := sDim.Render(desc)

	var line3 string
	switch {
	case st.Path != "":
		line3 = sDim.Render(truncate(shortenHome(st.Path), w))
	case st.CheckErr != "":
		line3 = sWarn.Render(truncate(st.CheckErr, w))
	default:
		line3 = sDim.Render("按 enter 安装到 " + shortenHome(m.installHint()))
	}

	body := head + "\n" + line2 + "\n" + line3
	if selected {
		return sCardOn.Render(body)
	}
	return sCardOff.Render(body)
}

func (m tuiModel) installHint() string {
	if dir, err := installDir(); err == nil {
		return dir
	}
	return "PATH"
}

func (m tuiModel) viewWorking() string {
	return sTitle.Render("hmck") + sDim.Render("  处理中") + "\n\n" +
		"  " + m.spin.View() + "  " + sDim.Render(m.workText)
}

func (m tuiModel) viewResult() string {
	head := sBad.Render("✗ 操作失败")
	if m.resultOK {
		head = sGood.Render("✓ 完成")
	}
	return sTitle.Render("hmck") + "  " + head + "\n\n" +
		"  " + sText.Render(m.resultMsg) + "\n\n" +
		hints("任意键", "返回")
}

func (m tuiModel) contentWidth() int {
	w := m.width - 4
	if w < 40 {
		w = 40
	}
	if w > 110 {
		w = 110
	}
	return w
}

func hints(pairs ...string) string {
	var parts []string
	for i := 0; i+1 < len(pairs); i += 2 {
		parts = append(parts, sKey.Render(pairs[i])+sDim.Render(" "+pairs[i+1]))
	}
	return "  " + strings.Join(parts, sDim.Render("  ·  "))
}

func statusStyle(st toolState) (string, lipgloss.Style) {
	label, kind := st.Status()
	switch kind {
	case "ok":
		return "✓ " + label, sGood
	case "behind":
		return "↑ 可升级", sWarn
	case "missing":
		return "按 enter 安装", sWarn
	default:
		return "! " + label, sWarn
	}
}

func truncate(s string, w int) string {
	if w <= 0 || lipgloss.Width(s) <= w {
		return s
	}
	r := []rune(s)
	for len(r) > 0 && lipgloss.Width(string(r)) > w-1 {
		r = r[:len(r)-1]
	}
	return string(r) + "…"
}

func runTUI() int {
	p := tea.NewProgram(newTUIModel(), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "TUI 出错: "+err.Error())
		return 2
	}
	return 0
}
