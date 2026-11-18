package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	lAccent = lipgloss.AdaptiveColor{Light: "#334155", Dark: "#CBD5E1"}
	lDim    = lipgloss.AdaptiveColor{Light: "#64748B", Dark: "#94A3B8"}
	lText   = lipgloss.AdaptiveColor{Light: "#0F172A", Dark: "#E2E8F0"}
	lGood   = lipgloss.AdaptiveColor{Light: "#047857", Dark: "#34D399"}
	lBad    = lipgloss.AdaptiveColor{Light: "#B91C1C", Dark: "#F87171"}
	lWarn   = lipgloss.AdaptiveColor{Light: "#B45309", Dark: "#FBBF24"}
	lLine   = lipgloss.AdaptiveColor{Light: "#E2E8F0", Dark: "#334155"}
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

type resultLine struct {
	text string
	ok   bool
}

type tuiModel struct {
	states []toolState
	cursor int
	state  tuiState

	width, height int
	spin          spinner.Model
	checkedAt     time.Time

	workText    string
	resultLines []resultLine
	configErr   string

	// 一键更新的进度通道；updGen 防止上一次的残留消息串进来
	updCh  chan tea.Msg
	updGen int
}

type inspectDoneMsg struct {
	states []toolState
	err    error
}

type skillDoneMsg struct{ lines []resultLine }

type installDoneMsg struct {
	key, from, version string
	err                error
}

type execDoneMsg struct{ err error }

type installResult struct {
	key, from, to string
	err           error
}

type batchProgressMsg struct {
	gen, index, total int
	key               string
}

type batchDoneMsg struct {
	gen     int
	results []installResult
}

type batchClosedMsg struct{ gen int }

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
	return func() tea.Msg {
		states, err := inspectAll()
		return inspectDoneMsg{states: states, err: err}
	}
}

// skillCmd 装 hmck 自己的 skill，并刷新已安装工具的 skill
func skillCmd() tea.Cmd {
	return func() tea.Msg {
		var lines []resultLine
		path, changed, fresh, err := installSkill("")
		if err != nil {
			lines = append(lines, resultLine{text: "安装 hmck skill 失败: " + err.Error(), ok: false})
			return skillDoneMsg{lines: lines}
		}
		switch {
		case fresh:
			lines = append(lines, resultLine{text: "hmck skill 已安装: " + shortenHome(path), ok: true})
		case changed:
			lines = append(lines, resultLine{text: "hmck skill 已更新: " + shortenHome(path), ok: true})
		default:
			lines = append(lines, resultLine{text: "hmck skill 已是最新: " + shortenHome(path), ok: true})
		}
		done, failed := refreshToolSkills(false)
		if len(done) > 0 {
			lines = append(lines, resultLine{text: "已刷新 " + strings.Join(done, "、") + " 的 skill", ok: true})
		}
		if len(failed) > 0 {
			lines = append(lines, resultLine{text: "没刷新成功（版本太旧？）: " + strings.Join(failed, "、"), ok: false})
		}
		return skillDoneMsg{lines: lines}
	}
}

func installCmd(st toolState) tea.Cmd {
	return func() tea.Msg {
		v, err := installTool(st)
		return installDoneMsg{key: st.Tool.Key, from: st.Installed, version: v, err: err}
	}
}

// upgradeAllCmd 依次升级所有可升级的工具，边跑边把进度发到通道里
func upgradeAllCmd(targets []toolState, ch chan tea.Msg, gen int) tea.Cmd {
	return func() tea.Msg {
		defer close(ch)
		results := make([]installResult, 0, len(targets))
		for i, st := range targets {
			select {
			case ch <- batchProgressMsg{gen: gen, index: i + 1, total: len(targets), key: st.Tool.Key}:
			default:
			}
			v, err := installTool(st)
			results = append(results, installResult{key: st.Tool.Key, from: st.Installed, to: v, err: err})
		}
		return batchDoneMsg{gen: gen, results: results}
	}
}

func waitUpd(ch chan tea.Msg, gen int) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return batchClosedMsg{gen: gen}
		}
		return msg
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
		if msg.err != nil {
			m.configErr = msg.err.Error()
		} else {
			m.configErr = ""
		}
		m.checkedAt = time.Now()
		m.state = list
		m.clampCursor()

	case skillDoneMsg:
		m.state = result
		m.resultLines = msg.lines
		if len(m.resultLines) == 0 {
			m.resultLines = []resultLine{{text: "没有需要处理的 skill", ok: true}}
		}

	case installDoneMsg:
		m.state = result
		m.resultLines = []resultLine{m.installResultLine(msg.key, msg.from, msg.version, msg.err)}
		if msg.err == nil {
			m.markUpdated(msg.key, msg.version)
		}

	case batchProgressMsg:
		if msg.gen != m.updGen || m.state != working {
			return m, nil
		}
		m.workText = fmt.Sprintf("正在更新 %d/%d：%s…", msg.index, msg.total, msg.key)
		// 继续等下一帧，否则后面的进度消息全堵在通道里
		return m, waitUpd(m.updCh, msg.gen)

	case batchClosedMsg:
		return m, nil

	case batchDoneMsg:
		if msg.gen != m.updGen {
			return m, nil
		}
		m.state = result
		m.resultLines = nil
		for _, r := range msg.results {
			m.resultLines = append(m.resultLines, m.installResultLine(r.key, r.from, r.to, r.err))
			if r.err == nil {
				m.markUpdated(r.key, r.to)
			}
		}

	case execDoneMsg:
		if msg.err != nil {
			m.state = result
			m.resultLines = []resultLine{{text: "工具退出时出错: " + msg.err.Error(), ok: false}}
			return m, nil
		}
		m.state = list
	}
	return m, nil
}

func (m *tuiModel) installResultLine(key, from, to string, err error) resultLine {
	if err != nil {
		return resultLine{text: fmt.Sprintf("%s 升级失败: %v", key, err), ok: false}
	}
	switch {
	case to == "":
		return resultLine{text: key + " 已更新", ok: true}
	case from == "":
		return resultLine{text: fmt.Sprintf("%s 已安装 %s", key, to), ok: true}
	case isNewer(to, from):
		return resultLine{text: fmt.Sprintf("%s %s → %s 已更新", key, from, to), ok: true}
	default:
		return resultLine{text: fmt.Sprintf("%s %s 已是最新", key, to), ok: true}
	}
}

func (m *tuiModel) markUpdated(key, newVer string) {
	for i := range m.states {
		if m.states[i].Tool.Key != key {
			continue
		}
		if newVer != "" {
			m.states[i].Installed = newVer
			m.states[i].Latest = newVer
			m.states[i].CheckErr = ""
		}
		if p, ok := findTool(m.states[i].Tool); ok {
			m.states[i].Path = p
		}
	}
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
		case "U":
			return m.startUpgradeAll()
		case "s":
			m.state = working
			m.workText = "正在安装 agent skill…"
			return m, tea.Batch(m.spin.Tick, skillCmd())
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
		m.resultLines = []resultLine{{text: st.Tool.Key + " " + st.Installed + " 已是最新", ok: true}}
		return m, nil
	}
	m.state = working
	m.workText = "正在下载并替换 " + st.Tool.Key + "…"
	return m, tea.Batch(m.spin.Tick, installCmd(st))
}

// startUpgradeAll 一键更新：把所有 Behind 的工具按顺序更新一遍
func (m tuiModel) startUpgradeAll() (tea.Model, tea.Cmd) {
	var targets []toolState
	for _, st := range m.states {
		if st.Behind() {
			targets = append(targets, st)
		}
	}
	if len(targets) == 0 {
		m.state = result
		m.resultLines = []resultLine{{text: "所有工具都已是最新", ok: true}}
		return m, nil
	}

	m.state = working
	m.updGen++
	m.updCh = make(chan tea.Msg, 16)
	m.workText = fmt.Sprintf("正在更新 %d 个工具…", len(targets))
	ch, gen := m.updCh, m.updGen
	return m, tea.Batch(m.spin.Tick, upgradeAllCmd(targets, ch, gen), waitUpd(ch, gen))
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
		summary += fmt.Sprintf(" · %d 个可更新", behind)
	}
	if missing > 0 {
		summary += fmt.Sprintf(" · %d 个未安装", missing)
	}
	if !m.checkedAt.IsZero() {
		summary += " · 检查于 " + m.checkedAt.Format("15:04:05")
	}
	b.WriteString(sDim.Render(summary))
	b.WriteString("\n")
	if m.configErr != "" {
		b.WriteString(sWarn.Render("  自定义工具配置有问题: " + truncate(m.configErr, m.contentWidth())))
		b.WriteString("\n")
	}
	b.WriteString("\n")

	for i, st := range m.states {
		b.WriteString(m.renderCard(st, i == m.cursor))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(hints("↑↓", "选择", "enter", "启动/安装", "u", "升级", "U", "一键全部更新", "s", "装 skill", "r", "重新检查", "q", "退出"))
	return b.String()
}

func (m tuiModel) renderCard(st toolState, selected bool) string {
	w := m.contentWidth()
	status, style := statusStyle(st)
	name := st.Tool.Key
	if st.Tool.Custom {
		name += sDim.Render(" · 自定义")
	}
	head := sText.Render(name) + "   " + sText.Render(versionText(st)) + "   " + style.Render(status)

	line2 := sDim.Render(truncate(st.Tool.Desc, w))

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

// versionText 把已装和最新都摆在卡片上：0.4.2 → 0.4.3 / 0.4.2 · 最新 0.4.2
func versionText(st toolState) string {
	switch {
	case st.Installed == "" && st.Latest != "":
		return "未安装 · 最新 " + st.Latest
	case st.Installed == "":
		return "未安装"
	case st.Latest == "":
		return "已装 " + st.Installed
	case isNewer(st.Latest, st.Installed):
		return st.Installed + " → " + st.Latest
	default:
		return st.Installed + " · 最新 " + st.Latest
	}
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
	allOK := true
	for _, l := range m.resultLines {
		if !l.ok {
			allOK = false
		}
	}
	head := sBad.Render("✗ 操作失败")
	if allOK {
		head = sGood.Render("✓ 完成")
	}

	var b strings.Builder
	b.WriteString(sTitle.Render("hmck") + "  " + head + "\n\n")
	for _, l := range m.resultLines {
		style := sGood
		if !l.ok {
			style = sBad
		}
		b.WriteString("  " + style.Render(l.text) + "\n")
	}
	b.WriteString("\n")
	b.WriteString(hints("任意键", "返回"))
	return b.String()
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
		return "✓ 已是最新", sGood
	case "behind":
		return "↑ 可更新", sWarn
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
