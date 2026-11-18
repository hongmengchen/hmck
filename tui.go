package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/hongmengchen/tui/theme"
)

var (
	pal    = theme.Slate()
	styles = theme.New(pal)

	// 卡片里各列自己的样式：名称、版本、路径、分隔线
	styleNameOff = lipgloss.NewStyle().Foreground(pal.Text).Bold(true)
	styleNameOn  = lipgloss.NewStyle().Foreground(pal.Accent).Bold(true)
	styleVer     = lipgloss.NewStyle().Foreground(pal.Text)
	styleVerOld  = lipgloss.NewStyle().Foreground(pal.Dim)
	styleVerNew  = lipgloss.NewStyle().Foreground(pal.Warn).Bold(true)
	stylePath    = lipgloss.NewStyle().Foreground(pal.Dim)
	styleRule    = lipgloss.NewStyle().Foreground(pal.Line)

	// 选中的卡片用一条实心竖条，和未选中的细线一眼能分开
	styleCardOn = lipgloss.NewStyle().
			Border(lipgloss.Border{Left: "▌"}, false, false, false, true).
			BorderForeground(pal.Accent).
			PaddingLeft(1)
)

type tuiState int

const (
	list tuiState = iota
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

	cfg    appConfig         // 自动下载 / 自动安装开关
	cfgErr string            // 开关保存失败时的提示
	staged map[string]string // 已预下载的版本：tool key -> version

	// 本地扫完就上图，远端最新版在后台逐个回来
	localReady bool
	pending    map[string]bool
	chkCh      chan tea.Msg
	chkGen     int

	// 一键更新/预下载的进度通道；updGen 防止上一次的残留消息串进来
	updCh       chan tea.Msg
	updGen      int
	batchAction string // “更新”或“预下载”
}

type localDoneMsg struct {
	states []toolState
	err    error
}

type latestDoneMsg struct {
	gen int
	key string
	ver string
	err error
}

type latestClosedMsg struct{ gen int }

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

type downloadResult struct {
	key, ver string
	err      error
}

type downloadDoneMsg struct {
	gen     int
	results []downloadResult
}

func newTUIModel() tuiModel {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = styles.Accent
	cfg, err := loadConfig()
	m := tuiModel{
		state: list, spin: sp, cfg: cfg,
		staged:  map[string]string{},
		pending: map[string]bool{},
	}
	if err != nil {
		m.cfgErr = err.Error()
	}

	// 工具清单只读本机配置，先摆出来；版本和路径随后补上
	tools, terr := allTools()
	if terr != nil {
		m.configErr = terr.Error()
	}
	m.states = make([]toolState, len(tools))
	for i, t := range tools {
		m.states[i] = toolState{Tool: t}
	}
	return m
}

func (m tuiModel) Init() tea.Cmd {
	return tea.Batch(m.spin.Tick, localInspectCmd(m.toolList()))
}

func (m tuiModel) toolList() []tool {
	tools := make([]tool, len(m.states))
	for i, st := range m.states {
		tools[i] = st.Tool
	}
	return tools
}

// localInspectCmd 只扫本机，几乎立刻返回，首屏不用等网络
func localInspectCmd(tools []tool) tea.Cmd {
	return func() tea.Msg {
		states, err := inspectLocalAll(tools)
		return localDoneMsg{states: states, err: err}
	}
}

// latestCheckCmd 并发问 GitHub，每查到一个就发一条，卡片逐个刷新
func latestCheckCmd(tools []tool, ch chan tea.Msg, gen int) tea.Cmd {
	return func() tea.Msg {
		var wg sync.WaitGroup
		sem := make(chan struct{}, 4)
		for _, t := range tools {
			wg.Add(1)
			go func(t tool) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				tag, err := latestTag(t.Repo)
				msg := latestDoneMsg{gen: gen, key: t.Key}
				if err != nil {
					msg.err = err
				} else {
					msg.ver = strings.TrimPrefix(tag, "v")
				}
				select {
				case ch <- msg:
				default:
				}
			}(t)
		}
		wg.Wait()
		close(ch)
		return nil // 结束信号由 waitLatest 在通道关闭时发出
	}
}

func waitLatest(ch chan tea.Msg, gen int) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return latestClosedMsg{gen: gen}
		}
		return msg
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

// downloadAllCmd 只下载并校验，不替换；结果进 staged，等用户按 u/U 再装
func downloadAllCmd(targets []toolState, ch chan tea.Msg, gen int) tea.Cmd {
	return func() tea.Msg {
		defer close(ch)
		results := make([]downloadResult, 0, len(targets))
		for i, st := range targets {
			select {
			case ch <- batchProgressMsg{gen: gen, index: i + 1, total: len(targets), key: st.Tool.Key}:
			default:
			}
			_, ver, err := fetchTool(st)
			results = append(results, downloadResult{key: st.Tool.Key, ver: ver, err: err})
		}
		return downloadDoneMsg{gen: gen, results: results}
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
		if !m.animating() {
			return m, nil
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case localDoneMsg:
		m.states = msg.states
		if msg.err != nil {
			m.configErr = msg.err.Error()
		}
		m.localReady = true
		m.checkedAt = time.Time{}
		m.clampCursor()
		return m.startLatestCheck()

	case latestDoneMsg:
		if msg.gen != m.chkGen {
			return m, nil
		}
		if msg.key != "" {
			for i := range m.states {
				if m.states[i].Tool.Key != msg.key {
					continue
				}
				if msg.err != nil {
					m.states[i].CheckErr = msg.err.Error()
				} else {
					m.states[i].Latest = msg.ver
					m.states[i].CheckErr = ""
				}
			}
			delete(m.pending, msg.key)
		}
		if m.chkCh == nil {
			return m, nil
		}
		return m, waitLatest(m.chkCh, msg.gen)

	case latestClosedMsg:
		if msg.gen != m.chkGen {
			return m, nil
		}
		m.pending = map[string]bool{}
		m.checkedAt = time.Now()
		return m.afterInspect()

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
		action := m.batchAction
		if action == "" {
			action = "更新"
		}
		m.workText = fmt.Sprintf("正在%s %d/%d：%s…", action, msg.index, msg.total, msg.key)
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
				delete(m.staged, r.key)
			}
		}

	case downloadDoneMsg:
		if msg.gen != m.updGen {
			return m, nil
		}
		m.state = result
		m.resultLines = nil
		for _, r := range msg.results {
			if r.err != nil {
				m.resultLines = append(m.resultLines, resultLine{text: fmt.Sprintf("%s 下载失败: %v", r.key, r.err), ok: false})
				continue
			}
			m.staged[r.key] = r.ver
			m.resultLines = append(m.resultLines, resultLine{text: fmt.Sprintf("%s %s 已下载，按 U 安装", r.key, r.ver), ok: true})
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
			m.states[i].UpdatedAt = time.Now()
			m.states[i].CheckErr = ""
		}
		if !m.states[i].Tool.Self {
			if p, ok := findTool(m.states[i].Tool); ok {
				m.states[i].Path = p
			}
		}
	}
}

func (m tuiModel) handleKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.state {
	case working:
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
			if !m.localReady || len(m.states) == 0 {
				return m, nil
			}
			st := m.states[m.cursor]
			if st.Tool.Self {
				// 选到自己时不启动自己，只走升级/已是最新
				return m.startInstall(st)
			}
			if st.Missing() || st.Path == "" {
				return m.startInstall(st)
			}
			return m, launchCmd(st)
		case "u":
			if m.localReady && len(m.states) > 0 {
				return m.startInstall(m.states[m.cursor])
			}
		case "U":
			if !m.localReady {
				return m, nil
			}
			return m.startUpgradeAll()
		case "a":
			m.cfg.AutoDownload = !m.cfg.AutoDownload
			if !m.cfg.AutoDownload {
				m.cfg.AutoInstall = false
			}
			m.saveCfg()
			if m.cfg.AutoDownload {
				return m.startAutoDownload()
			}
		case "A":
			m.cfg.AutoInstall = !m.cfg.AutoInstall
			if m.cfg.AutoInstall {
				m.cfg.AutoDownload = true
				m.saveCfg()
				if m.hasBehind() {
					return m.startUpgradeAll()
				}
				return m, nil
			}
			m.saveCfg()
		case "s":
			m.state = working
			m.workText = "正在安装 agent skill…"
			return m, tea.Batch(m.spin.Tick, skillCmd())
		case "r":
			return m.refresh()
		case "q", "esc":
			return m, tea.Quit
		}

	case result:
		// 结果页的版本状态在结果消息里已经更新过，回列表直接切屏即可；
		// 再重扫一遍会把刚升级的 hmck 自己读回运行中的旧版本，也会让页面一直停在结果页
		if k.String() == "q" || k.String() == "esc" {
			return m, tea.Quit
		}
		m.state = list
		return m, nil
	}
	return m, nil
}

// refresh 原地重扫：先保留当前列表，版本和最新版再一点点补
func (m tuiModel) refresh() (tea.Model, tea.Cmd) {
	m.localReady = false
	m.checkedAt = time.Time{}
	m.chkGen++ // 丢掉上一轮还没回来的结果
	m.pending = map[string]bool{}
	return m, tea.Batch(m.spin.Tick, localInspectCmd(m.toolList()))
}

// startLatestCheck 把本地已就绪的工具并发问一遍 GitHub
func (m tuiModel) startLatestCheck() (tea.Model, tea.Cmd) {
	var targets []tool
	m.chkGen++
	m.pending = map[string]bool{}
	for _, st := range m.states {
		if st.CheckErr != "" {
			continue // 平台不支持，没什么可查的
		}
		targets = append(targets, st.Tool)
		m.pending[st.Tool.Key] = true
	}
	if len(targets) == 0 {
		m.checkedAt = time.Now()
		return m, nil
	}

	m.chkCh = make(chan tea.Msg, len(targets))
	ch, gen := m.chkCh, m.chkGen
	return m, tea.Batch(latestCheckCmd(targets, ch, gen), waitLatest(ch, gen))
}

func (m tuiModel) animating() bool {
	if m.state == working {
		return true
	}
	return m.state == list && (!m.localReady || len(m.pending) > 0)
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
	m.batchAction = "更新"
	m.workText = fmt.Sprintf("正在更新 %d 个工具…", len(targets))
	ch, gen := m.updCh, m.updGen
	return m, tea.Batch(m.spin.Tick, upgradeAllCmd(targets, ch, gen), waitUpd(ch, gen))
}

// afterInspect 检查完按开关决定：直接装，或先把新版下载到缓存
func (m tuiModel) afterInspect() (tea.Model, tea.Cmd) {
	if m.cfg.AutoInstall && m.hasBehind() {
		return m.startUpgradeAll()
	}
	if m.cfg.AutoDownload && m.hasBehind() {
		return m.startAutoDownload()
	}
	return m, nil
}

// startAutoDownload 把可更新工具的新版下载到缓存，不替换
func (m tuiModel) startAutoDownload() (tea.Model, tea.Cmd) {
	var targets []toolState
	for _, st := range m.states {
		if !st.Behind() {
			continue
		}
		if ver, ok := m.staged[st.Tool.Key]; ok && ver == st.Latest {
			continue // 这个版本已经下好了
		}
		targets = append(targets, st)
	}
	if len(targets) == 0 {
		return m, nil
	}

	m.state = working
	m.updGen++
	m.updCh = make(chan tea.Msg, 16)
	m.batchAction = "预下载"
	m.workText = fmt.Sprintf("正在下载 %d 个工具的新版…", len(targets))
	ch, gen := m.updCh, m.updGen
	return m, tea.Batch(m.spin.Tick, downloadAllCmd(targets, ch, gen), waitUpd(ch, gen))
}

func (m tuiModel) hasBehind() bool {
	for _, st := range m.states {
		if st.Behind() {
			return true
		}
	}
	return false
}

func (m *tuiModel) saveCfg() {
	if _, err := saveConfig(m.cfg); err != nil {
		m.cfgErr = err.Error()
		return
	}
	m.cfgErr = ""
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
	case list:
		body = m.viewList()
	case working:
		body = m.viewWorking()
	case result:
		body = m.viewResult()
	}
	return "\n" + body + "\n"
}

// logoLines 是终端里的招牌，和 docs/logo.svg 同一套几何：一个方框加向上的箭头
var logoLines = []string{
	"╭───╮",
	"│ ↑ │",
	"╰───╯",
}

// withLogo 把 logo 摆在左边，右边逐行接上文字
func (m tuiModel) withLogo(lines ...string) string {
	var b strings.Builder
	for i, l := range logoLines {
		b.WriteString(styles.Accent.Render(l))
		if i < len(lines) && lines[i] != "" {
			b.WriteString("  " + lines[i])
		}
		b.WriteString("\n")
	}
	return b.String()
}

func (m tuiModel) viewList() string {
	var b strings.Builder
	w := m.contentWidth()
	cols := m.cardCols()

	behind, missing := 0, 0
	for _, st := range m.states {
		if st.Behind() {
			behind++
		}
		if st.Missing() {
			missing++
		}
	}
	summary := fmt.Sprintf("%d 个工具", len(m.states))
	if behind == 0 && missing == 0 && !m.checkedAt.IsZero() {
		summary += " · 全部已是最新"
	} else {
		if behind > 0 {
			summary += fmt.Sprintf(" · %d 个可更新", behind)
		}
		if missing > 0 {
			summary += fmt.Sprintf(" · %d 个未安装", missing)
		}
	}
	if !m.checkedAt.IsZero() {
		summary += " · 检查于 " + m.checkedAt.Format("15:04:05")
	}
	b.WriteString(m.withLogo(
		styles.Title.Render("hmck")+styles.Dim.Render("  我的命令行小工具"),
		styles.Dim.Render(summary),
		m.viewSwitches(),
	))
	if m.configErr != "" {
		b.WriteString(styles.Warn.Render("  自定义工具配置有问题: " + theme.Truncate(m.configErr, w)))
		b.WriteString("\n")
	}
	if m.cfgErr != "" {
		b.WriteString(styles.Warn.Render("  开关没保存: " + theme.Truncate(m.cfgErr, w)))
		b.WriteString("\n")
	}
	b.WriteString("\n")

	for i, st := range m.states {
		b.WriteString(m.renderCard(st, i == m.cursor, cols))
		b.WriteString("\n")
		if i < len(m.states)-1 {
			// 工具之间用一条淡线隔开，卡片各占一块，扫一眼就能分开
			b.WriteString("  " + styleRule.Render(strings.Repeat("─", w)))
			b.WriteString("\n")
		}
	}
	b.WriteString("\n")
	b.WriteString(m.viewHints("↑↓", "选择", "enter", "启动/安装", "u", "升级", "U", "全部更新", "s", "装 skill", "r", "重新检查", "q", "退出"))
	return b.String()
}

// viewHints 和 theme.Hints 一样，但窄终端会自动折成两行，不会把最后一个键提示切掉
func (m tuiModel) viewHints(pairs ...string) string {
	const indent, sepW = 2, 5 // "  ·  "
	w := m.contentWidth()
	var lines, line []string
	lineW := indent
	for i := 0; i+1 < len(pairs); i += 2 {
		part := styles.Key.Render(pairs[i]) + styles.Dim.Render(" "+pairs[i+1])
		pw := lipgloss.Width(pairs[i]) + 1 + lipgloss.Width(pairs[i+1])
		if len(line) > 0 && lineW+sepW+pw > w {
			lines = append(lines, "  "+strings.Join(line, styles.Dim.Render("  ·  ")))
			line, lineW = nil, indent
		}
		if len(line) > 0 {
			lineW += sepW
		}
		line = append(line, part)
		lineW += pw
	}
	if len(line) > 0 {
		lines = append(lines, "  "+strings.Join(line, styles.Dim.Render("  ·  ")))
	}
	return strings.Join(lines, "\n")
}

func (m tuiModel) viewSwitches() string {
	state := func(on bool) string {
		if on {
			return styles.Good.Render("[开]")
		}
		return styles.Dim.Render("[关]")
	}
	return styles.Key.Render("a") + styles.Dim.Render(" 自动下载最新版 ") + state(m.cfg.AutoDownload) +
		"    " + styles.Key.Render("A") + styles.Dim.Render(" 自动安装最新版 ") + state(m.cfg.AutoInstall)
}

// cardCols 是所有卡片共用的列宽：名称列和版本列左对齐，后面的路径才好排
type cardCols struct {
	name int
	ver  int
}

func (m tuiModel) cardCols() cardCols {
	c := cardCols{name: 6, ver: 8}
	for _, st := range m.states {
		if n := lipgloss.Width(st.Tool.Key); n > c.name {
			c.name = n
		}
		if n := lipgloss.Width(m.versionCell(st)); n > c.ver {
			c.ver = n
		}
	}
	if c.name > 20 {
		c.name = 20
	}
	if c.ver > 24 {
		c.ver = 24
	}
	return c
}

// statusIcon 用一个符号说清状态，颜色再补一层；
// 不再写「已是最新」这类和版本号重复的话，版本号本身就能说明问题
func (m tuiModel) statusIcon(st toolState) (string, lipgloss.Style) {
	switch {
	case st.Installed == "" && st.CheckErr != "":
		return "!", styles.Bad // 这台机器上装不了
	case st.Installed == "":
		return "○", styles.Warn
	case st.CheckErr != "":
		return "!", styles.Warn // 版本有了，最新版没查到
	case st.Latest != "" && m.staged[st.Tool.Key] == st.Latest:
		return "↓", styles.Good // 已下载，按 u 就能装
	case st.Behind():
		return "↑", styles.Warn
	default:
		return "✓", styles.Good
	}
}

// versionCell 是版本那一格：状态图标 + 版本号（可更新时是 旧 → 新）
func (m tuiModel) versionCell(st toolState) string {
	if !m.localReady {
		return styles.Dim.Render(m.spin.View()) + " " + styleVerOld.Render("读取中…")
	}
	icon, iconStyle := m.statusIcon(st)
	verStyle := styleVer
	if m.pending[st.Tool.Key] {
		// 最新版还没回来，先转圈；已装版本照常显示
		icon, iconStyle, verStyle = m.spin.View(), styles.Dim, styleVerOld
	}
	switch {
	case st.Installed == "":
		return iconStyle.Render(icon) + " " + styleVerOld.Render("未安装")
	case st.Latest == "":
		return iconStyle.Render(icon) + " " + verStyle.Render(st.Installed)
	case isNewer(st.Latest, st.Installed):
		return iconStyle.Render(icon) + " " + styleVerOld.Render(st.Installed) +
			styleVerOld.Render(" → ") + styleVerNew.Render(st.Latest)
	default:
		return iconStyle.Render(icon) + " " + verStyle.Render(st.Installed)
	}
}

// renderCard 一行主信息（名称 · 版本 · 路径），一行说明；
// 路径贴右边，版本那几列不会因为路径长短而跑位
func (m tuiModel) renderCard(st toolState, selected bool, cols cardCols) string {
	w := m.contentWidth()
	nameStyle := styleNameOff
	if selected {
		nameStyle = styleNameOn
	}

	head := nameStyle.Render(theme.Pad(st.Tool.Key, cols.name)) + "  " +
		theme.Pad(m.versionCell(st), cols.ver)

	// 第一行右边标出上一次版本更新时间，窄屏放不下就不显示
	if st.Installed != "" && !st.UpdatedAt.IsZero() {
		t := updatedText(st.UpdatedAt, time.Now())
		if gap := w - lipgloss.Width(head) - lipgloss.Width(t); gap >= 2 {
			head += strings.Repeat(" ", gap) + stylePath.Render(t)
		}
	}

	desc := st.Tool.Desc
	if st.Tool.Custom {
		desc = "自定义 · " + desc
	}
	tail, tailStyle, tailShort := "", styles.Dim, ""
	switch {
	case !m.localReady:
		desc, tail = "正在读取本地安装位置…", ""
	case st.Installed == "" && st.CheckErr != "":
		tail, tailStyle = "  "+st.CheckErr, styles.Warn
	case st.Installed == "":
		tail, tailStyle = "  ·  按 enter 安装到 "+shortenHome(m.installHint()), styles.Dim
		tailShort = "  ·  按 enter 安装"
	case st.CheckErr != "":
		tail, tailStyle = "  ·  "+st.CheckErr, styles.Warn
	case st.Latest != "" && m.staged[st.Tool.Key] == st.Latest:
		tail, tailStyle = "  ·  已下载，按 u 安装", styles.Good
	}

	// 第二行：作用在左，路径贴右边。窄屏时依次舍弃：路径 → 提示 → 截短提示
	const indent = "   "
	room := func(tail, p string) int {
		return w - lipgloss.Width(indent) - lipgloss.Width(tail) - lipgloss.Width(p) - 2
	}
	p := ""
	if st.Path != "" {
		p = shortenHome(st.Path)
	}
	if p != "" && room(tail, p) < 16 {
		p = ""
	}
	if tail != "" && tailShort != "" && room(tail, p) < 24 {
		tail = tailShort // 窄屏上先把提示说短，别把说明挤没
	}
	if tail != "" && room(tail, p) < 16 {
		tail = ""
	}
	descRoom := max(room(tail, p), 8)
	line2 := indent + stylePath.Render(theme.Truncate(desc, descRoom))
	if tail != "" {
		line2 += tailStyle.Render(tail)
	}
	if p != "" {
		line2 += strings.Repeat(" ", max(w-lipgloss.Width(line2)-lipgloss.Width(p), 2)) +
			stylePath.Render(p)
	}

	body := head + "\n" + line2
	if selected {
		return styleCardOn.Render(body)
	}
	return styles.CardOff.Render(body)
}

func (m tuiModel) installHint() string {
	if dir, err := installDir(); err == nil {
		return dir
	}
	return "PATH"
}

func (m tuiModel) viewWorking() string {
	return styles.Title.Render("hmck") + styles.Dim.Render("  处理中") + "\n\n" +
		"  " + m.spin.View() + "  " + styles.Dim.Render(m.workText)
}

func (m tuiModel) viewResult() string {
	allOK := true
	for _, l := range m.resultLines {
		if !l.ok {
			allOK = false
		}
	}
	head := styles.Bad.Render("✗ 操作失败")
	if allOK {
		head = styles.Good.Render("✓ 完成")
	}

	var b strings.Builder
	b.WriteString(styles.Title.Render("hmck") + "  " + head + "\n\n")
	for _, l := range m.resultLines {
		style := styles.Good
		if !l.ok {
			style = styles.Bad
		}
		b.WriteString("  " + style.Render(l.text) + "\n")
	}
	b.WriteString("\n")
	b.WriteString(styles.Hints("任意键", "返回"))
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

func runTUI() int {
	p := tea.NewProgram(newTUIModel(), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "TUI 出错: "+err.Error())
		return 2
	}
	return 0
}
