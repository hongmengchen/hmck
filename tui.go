package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
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
	// 分栏标题：比卡片淡，但加粗，扫一眼知道下面这一栏是什么
	styleGroup = lipgloss.NewStyle().Foreground(pal.Text).Bold(true)

	// 进度条：填充用主色，底槽用卡片分隔线那种深色，整条看着是实的，不是一串点
	styleBarFill  = lipgloss.NewStyle().Foreground(pal.Accent)
	styleBarEmpty = lipgloss.NewStyle().Foreground(pal.Line)

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
	notes
	infoPage
	confirmUninstall
	addTool
	catalog
	syncPage
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

	syncLine string // 多端同步状态/结果，只在开启过同步时显示

	// 本地扫完就上图，远端最新版在后台逐个回来
	localReady bool
	pending    map[string]bool
	chkCh      chan tea.Msg
	chkGen     int

	// 一键更新/预下载的进度通道；updGen 防止上一次的残留消息串进来
	updCh       chan tea.Msg
	updGen      int
	batchAction string // “更新”或“预下载”

	// 下载进度条的状态：progIndex/progTotal 是一键更新里第几个（单个工具为 0），
	// progDone/progBytes 是当前文件已下载和总字节数，progBytes 为 0 表示长度未知
	progIndex, progTotal int
	progDone, progBytes  int64

	chg  map[string]changelogState // 更新日志缓存：tool key -> 最近的几个版本
	note notesView                 // 正在看的更新日志页
	info infoView                  // 正在看的信息页

	confirm      uninstallView // 正在确认卸载哪个工具
	exitOnResult bool          // 卸载了 hmck 自己：结果页按任意键退出

	add addView // 正在填「新增工具」的表单

	cat    catalogView // 线上工具目录
	catGen int         // 目录也认代次，连按 r 不会让旧结果盖掉新的

	sync syncView // 多端同步页
}

type syncMode int

const (
	syncIdle syncMode = iota // 菜单
	syncPass                 // 在页面里输入同步密码
	syncPath                 // 在页面里输入要同步的路径
)

// syncView 是多端同步页的状态。密码只在当前这次 push/pull 里用，
// 输入完交给后台命令后就清掉，不写盘、不进命令行参数。
type syncView struct {
	mode   syncMode
	cursor int
	busy   bool   // 正在和 GitHub 通信
	msg    string // 上次动作的结果
	err    string // 上次动作失败的原因

	action    string // 等密码回来后要执行的动作
	pass      string // 正在输入的密码
	passFirst string // 需要输两遍时，第一遍记在这里
	confirm   bool   // 这次密码要输两遍

	path     string // 正在输入的同步路径
	offArmed bool   // 忘掉密钥要按两次：第一次只是提醒

	ghChecked bool   // 已经问过本机的 gh 了
	ghFound   bool   // gh 装没装
	ghLogin   bool   // gh 登录没登录
	ghVer     string // gh 版本号
	ghRetry   string // 刚做过 install / login，等重新检测后好接一句提示
	setupErr  error  // 装/登录那一步的退出错误，用来把提示说清楚
}

// catalogView 是工具目录页：线上目录里的工具 + 本机自己加的自定义工具
type catalogView struct {
	rows    []tool // 线上目录那份；本机自定义的拼在 catalogRows 里
	cursor  int
	loaded  bool // 至少试过读一次
	loading bool
	source  string // online / bundled
	err     string // 线上没读到的原因，或操作时的一句提示
	skipped int    // 目录里读不懂、跳过的条数
}

// addView 是新增工具的表单：仓库必填，名字和说明可以不填。
// 作用和三件事的 CLI 版 `hmck add <repo>` 一样，只是添加完顺手装好
type addView struct {
	repo, key, desc string
	field           int // 0 仓库，1 名字，2 说明
	err             string
}

// value 是当前这一格的值；输入、退格都写这里
func (a *addView) value() *string {
	switch a.field {
	case 1:
		return &a.key
	case 2:
		return &a.desc
	}
	return &a.repo
}

// maxAddField 是单格的字符上限：仓库地址再长也到不了这么长，挡一下粘贴进来的大段文字
const maxAddField = 120

func (a *addView) insert(s string) {
	v := a.value()
	if r := []rune(*v); len(r) < maxAddField {
		*v = string(append(r, []rune(s)...))
	}
}

func (a *addView) backspace() {
	if r := []rune(*a.value()); len(r) > 0 {
		*a.value() = string(r[:len(r)-1])
	}
}

// uninstallView 是卸载确认页的状态：带上整个 toolState，
// 页面上才能说清删哪一份、PATH 上还有哪份要留着。
// typed 是已经敲进去的字，敲出的名字和工具名对上了才真删——手滑按一下不该删掉东西
type uninstallView struct {
	tool  toolState
	typed string
	err   string // 名字没敲对时的提醒
}

// nameMatches 判断敲进来的字是不是这个工具名：忽略大小写和首尾空格
func (v uninstallView) nameMatches() bool {
	return strings.EqualFold(strings.TrimSpace(v.typed), v.tool.Tool.Key)
}

// infoView 是信息页的状态：只记看的是哪个工具，内容每次从 states 现算，
// 后台查回来的最新版本、重新扫出来的路径会直接反映到页面上
type infoView struct {
	tool tool
}

// changelogState 是一个工具更新日志的缓存
type changelogState struct {
	entries []changeEntry
	err     string
	at      time.Time
}

// notesView 是更新日志页的状态
type notesView struct {
	tool    tool
	current string // 已装版本
	latest  string // 最新版本
	entries []changeEntry
	err     string
	loading bool
	scroll  int
	gen     int
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

// syncDoneMsg 是工具退出后自动推送的结果；失败只占状态栏一行，不打断使用
type syncDoneMsg struct {
	msg string
	err error
}

// syncActionMsg 是同步页上 push/pull/off 跑完的结果；
// reload 表示远端可能改了配置和工具清单，页面要重新读一遍
type syncActionMsg struct {
	action string
	msg    string
	err    error
	reload bool
}

// ghCheckMsg 是同步页开工前看一眼本机 GitHub CLI 的结果
type ghCheckMsg struct {
	found, loggedIn bool
	version         string
}

// ghSetupMsg 是装 gh / 登录 gh 那一步退出后的结果；how 是 install / login / page
type ghSetupMsg struct {
	how string
	err error
}

type uninstallDoneMsg struct{ res uninstallResult }

type installResult struct {
	key, from, to string
	err           error
}

type batchProgressMsg struct {
	gen, index, total int
	key               string
}

// progMsg 是下载中的字节进度。index/total 是一键更新里进行到第几个（单个工具是 0/0），
// done/bytes 是当前这个文件下了多少、一共多大（对方没给长度时 bytes 为 0）
type progMsg struct {
	gen, index, total int
	done, bytes       int64
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

// changelogMsg 是更新日志取回来的结果；prefetch 为 true 时只填列表卡片上的预览
type changelogMsg struct {
	key      string
	entries  []changeEntry
	err      error
	gen      int
	prefetch bool
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
		chg:     map[string]changelogState{},
	}
	if err != nil {
		m.cfgErr = err.Error()
	}
	m.refreshSyncLine()

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
	cmds := []tea.Cmd{m.spin.Tick, localInspectCmd(m.toolList())}
	// 顺手把每个工具的更新日志抓回来，卡片第三行能预览最近改了什么
	if c := m.prefetchChangelogs(); c != nil {
		cmds = append(cmds, c)
	}
	return tea.Batch(cmds...)
}

// prefetchChangelogs 并发抓所有工具的更新日志，只用来填卡片预览，失败静默
func (m tuiModel) prefetchChangelogs() tea.Cmd {
	var cmds []tea.Cmd
	for _, t := range m.toolList() {
		cmds = append(cmds, changelogCmd(t, changelogVersionLimit, 0, true))
	}
	return tea.Batch(cmds...)
}

// changelogCmd 拉一个工具的更新日志；prefetch 时只用来填列表预览
func changelogCmd(t tool, limit, gen int, prefetch bool) tea.Cmd {
	return func() tea.Msg {
		entries, err := fetchChangelog(t, limit)
		return changelogMsg{key: t.Key, entries: entries, err: err, gen: gen, prefetch: prefetch}
	}
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
				tag, err := latestTag(t)
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

// installCmd 装一个工具。下载的字节进度经 ch 回给 TUI——装一个工具也要有进度条，
// 大文件要下十几秒，光转个圈看不出是在动还是卡住了
func installCmd(st toolState, ch chan tea.Msg, gen int) tea.Cmd {
	return func() tea.Msg {
		defer close(ch)
		v, err := installTool(st, progressReporter(ch, gen, 0, 0))
		return installDoneMsg{key: st.Tool.Key, from: st.Installed, version: v, err: err}
	}
}

// progressReporter 把下载回调接成进度消息。通道满了就丢掉这一帧：
// 下载在另一个 goroutine 里跑，不能因为界面还没来得及读就卡住下载
func progressReporter(ch chan tea.Msg, gen, index, total int) progressFunc {
	return func(done, bytes int64) {
		select {
		case ch <- progMsg{gen: gen, index: index, total: total, done: done, bytes: bytes}:
		default:
		}
	}
}

// addToolMsg 是「新增工具」跑完的结果：读到了发布就顺手把平台和校验方式记准
type addToolMsg struct {
	t   tool
	err error
}

// addToolCmd 和三件事的 `hmck add` 走同一套：先看发布里的资产推平台，
// 读不到就按常见平台记，写进本机配置，随后由调用方接着装
func addToolCmd(t tool) tea.Cmd {
	return func() tea.Msg {
		info, err := fetchReleaseInfo(t.Repo)
		if err != nil {
			t.Platforms = append([]string(nil), defaultPlatforms...)
		} else {
			t.Platforms = inferPlatforms(t.Key, info.Assets)
			if len(t.Platforms) == 0 {
				t.Platforms = append([]string(nil), defaultPlatforms...)
			}
			if hasChecksums(info.Assets) {
				t.Checksums = true
			}
		}
		if _, err := addCustomTool(t, false); err != nil {
			return addToolMsg{err: err}
		}
		t.Custom = true
		if err := normalizeCustom(&t); err != nil {
			return addToolMsg{err: err}
		}
		return addToolMsg{t: t}
	}
}

// catalogMsg 是工具目录读回来的结果；gen 不对就说明用户又按了 r，旧结果丢掉
type catalogMsg struct {
	gen int
	res catalogResult
}

func catalogFetchCmd(gen int) tea.Cmd {
	return func() tea.Msg { return catalogMsg{gen: gen, res: loadCatalog()} }
}

// catalogAddMsg 是从目录里挑中一个还没加入的工具：先写进本机配置，再开装
type catalogAddMsg struct {
	t   tool
	err error
}

// catalogAddCmd 直接用目录里写好的平台和校验方式入册，不再去问一次发布：
// 目录是维护的人挑过的，按它写的装才对得上
func catalogAddCmd(t tool) tea.Cmd {
	return func() tea.Msg {
		t.Custom = true
		if err := normalizeCustom(&t); err != nil {
			return catalogAddMsg{err: err}
		}
		if _, err := addCustomTool(t, false); err != nil {
			return catalogAddMsg{err: err}
		}
		return catalogAddMsg{t: t}
	}
}

func uninstallCmd(t tool, keepSkill bool) tea.Cmd {
	return func() tea.Msg {
		return uninstallDoneMsg{res: uninstallTool(t, keepSkill)}
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
			v, err := installTool(st, progressReporter(ch, gen, i+1, len(targets)))
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
			_, ver, err := fetchTool(st, progressReporter(ch, gen, i+1, len(targets)))
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

// launchCmd 启动一份已经在本机上的工具。path 由调用方决定：优先 hmck 装的那份，
// 没有就用 PATH 上别人装的那份（brew 的 glab），不会顺手装个新的
func launchCmd(t tool, path string) tea.Cmd {
	cmd := exec.Command(path)
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		return execDoneMsg{err: err}
	})
}

// autoSyncCmd 在工具退出后把可能改过的配置推上去；没开同步时几乎立刻返回。
func autoSyncCmd() tea.Cmd {
	return func() tea.Msg {
		msg, err := autoSyncPush()
		return syncDoneMsg{msg: msg, err: err}
	}
}

// ghCheckCmd 看一眼本机的 gh 装没装、登录没登录，结果回到同步页
func ghCheckCmd() tea.Cmd {
	return func() tea.Msg {
		st := inspectGH()
		return ghCheckMsg{found: st.Found, loggedIn: st.LoggedIn, version: st.Version}
	}
}

// syncRunCmd 在后台跑一次同步动作。pass 由同步页输入框直接传进来，
// 不经过终端读密码——alt screen 正开着，term.ReadPassword 会打架。
func syncRunCmd(action, pass string) tea.Cmd {
	return func() tea.Msg {
		switch action {
		case "push", "forcepush":
			msg, err := syncPush(syncOptions{pass: pass, force: action == "forcepush", quiet: true})
			return syncActionMsg{action: action, msg: msg, err: err}
		case "pull":
			msg, err := syncPull(syncOptions{pass: pass})
			return syncActionMsg{action: action, msg: msg, err: err, reload: err == nil}
		case "preview":
			msg, err := syncPull(syncOptions{pass: pass, dryRun: true})
			return syncActionMsg{action: action, msg: msg, err: err}
		case "forcepull":
			msg, err := syncPull(syncOptions{pass: pass, force: true})
			return syncActionMsg{action: action, msg: msg, err: err, reload: err == nil}
		case "off":
			repo, err := disableSync()
			if err != nil {
				return syncActionMsg{action: action, err: err}
			}
			msg := "已忘掉本机的仓库和同步密钥（远端数据没动）"
			if repo != "" {
				msg += "；远端 " + repo + " 里的数据还在"
			}
			return syncActionMsg{action: action, msg: msg}
		}
		return syncActionMsg{action: action, err: fmt.Errorf("未知的同步动作 %q", action)}
	}
}

// refreshSyncLine 用本机的 sync.json 重算列表上那一行同步状态
func (m *tuiModel) refreshSyncLine() {
	st, err := loadSyncState()
	if err != nil || !syncEnabled(st) {
		m.syncLine = ""
		return
	}
	line := "同步 " + st.Repo + " · 工具退出后自动推 · y 管理"
	if st.LastPush != "" {
		line += " · 上次推送 " + st.LastPush
	}
	m.syncLine = line
}

// reloadConfigFromDisk 在 pull 之后重读配置和工具清单：
// 远端可能改了工具列表和开关，页面得跟着变
func (m *tuiModel) reloadConfigFromDisk() tea.Cmd {
	if cfg, err := loadConfig(); err == nil {
		m.cfg = cfg
		m.cfgErr = ""
	} else {
		m.cfgErr = err.Error()
	}
	if tools, err := allTools(); err == nil {
		m.states = make([]toolState, len(tools))
		for i, t := range tools {
			m.states[i] = toolState{Tool: t}
		}
		m.clampCursor()
	}
	m.localReady = false
	m.checkedAt = time.Time{}
	m.chkGen++
	m.pending = map[string]bool{}
	return tea.Batch(m.spin.Tick, localInspectCmd(m.toolList()))
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
		// 换工具了，进度条从 0 重画
		m.progIndex, m.progTotal = msg.index, msg.total
		m.progDone, m.progBytes = 0, 0
		// 继续等下一帧，否则后面的进度消息全堵在通道里
		return m, waitUpd(m.updCh, msg.gen)

	case progMsg:
		if msg.gen != m.updGen || m.state != working {
			return m, nil
		}
		if msg.index > 0 {
			m.progIndex, m.progTotal = msg.index, msg.total
		}
		m.progDone, m.progBytes = msg.done, msg.bytes
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

	case changelogMsg:
		errText := ""
		if msg.err != nil {
			errText = msg.err.Error()
		}
		if len(msg.entries) > 0 {
			m.chg[msg.key] = changelogState{entries: msg.entries, at: time.Now()}
		}
		if msg.prefetch {
			return m, nil
		}
		if m.state != notes || msg.key != m.note.tool.Key || msg.gen != m.note.gen {
			return m, nil // 用户已经翻了别的工具，或又按了一次 r
		}
		m.note.loading = false
		m.note.err = errText
		if msg.err == nil {
			m.note.entries = msg.entries
		}

	case catalogMsg:
		if msg.gen != m.catGen {
			return m, nil
		}
		m.cat.loading = false
		m.cat.loaded = true
		m.cat.rows = msg.res.tools
		m.cat.source = msg.res.source
		m.cat.err = msg.res.err
		m.cat.skipped = msg.res.skipped
		if n := len(m.catalogRows()); m.cat.cursor >= n {
			m.cat.cursor = max(0, n-1)
		}

	case catalogAddMsg:
		if msg.err != nil {
			m.state = catalog
			m.cat.err = msg.err.Error()
			return m, nil
		}
		st := toolState{Tool: msg.t}
		m.states = append(m.states, st)
		m.cursor = len(m.states) - 1
		m.cat.err = ""
		go autoSyncPush()
		// 挑中就是为了装：写进配置后直接开始下载
		return m.startInstall(st)

	case addToolMsg:
		if msg.err != nil {
			// 没写进配置：回到表单，把填过的留着，改一下再来
			m.state = addTool
			m.add.err = msg.err.Error()
			return m, nil
		}
		st := toolState{Tool: msg.t}
		m.states = append(m.states, st)
		m.cursor = len(m.states) - 1
		m.add = addView{}
		go autoSyncPush()
		// 加进来就是为了用：接着装，不用再按一次 enter
		return m.startInstall(st)

	case uninstallDoneMsg:
		m.state = result
		m.resultLines = msg.res.lines()
		m.confirm = uninstallView{}
		if msg.res.Err == "" {
			m.forgetTool(msg.res.Tool.Key)
			delete(m.staged, msg.res.Tool.Key)
			// 把自己删了：这个进程还在跑，但没有下次了，结果看完就退出
			m.exitOnResult = msg.res.Tool.Self
		}

	case execDoneMsg:
		if msg.err != nil {
			m.state = result
			m.resultLines = []resultLine{{text: "工具退出时出错: " + msg.err.Error(), ok: false}}
			return m, nil
		}
		m.state = list
		return m, autoSyncCmd()

	case syncDoneMsg:
		switch {
		case msg.err != nil:
			m.syncLine = "同步失败：" + msg.err.Error()
		case msg.msg != "":
			m.syncLine = "同步 " + msg.msg
		}

	case syncActionMsg:
		m.sync.busy = false
		if msg.err != nil {
			m.sync.msg = ""
			m.sync.err = msg.err.Error()
			m.syncLine = "同步失败：" + msg.err.Error()
		} else {
			m.sync.err = ""
			m.sync.msg = msg.msg
			m.refreshSyncLine()
		}
		if msg.reload {
			return m, m.reloadConfigFromDisk()
		}

	case ghCheckMsg:
		m.sync.ghChecked = true
		m.sync.ghFound, m.sync.ghLogin, m.sync.ghVer = msg.found, msg.loggedIn, msg.version
		m.sync.busy = false
		retry, setupErr := m.sync.ghRetry, m.sync.setupErr
		m.sync.ghRetry, m.sync.setupErr = "", nil
		switch retry {
		case "install":
			switch {
			case msg.found && msg.loggedIn:
				m.sync.err = ""
				m.sync.msg = "gh 装好了，GitHub 也登录着，可以同步了"
			case msg.found:
				m.sync.err = ""
				m.sync.msg = "gh 装好了；下一步选「登录 GitHub CLI」"
			case setupErr != nil:
				m.sync.err = "安装没成功（" + setupErr.Error() + "）；可以再试一次，或打开下载页手动装"
			default:
				m.sync.err = "装完还没找到 gh；把安装目录加进 PATH 后重开 hmck，或按 r 重新检测"
			}
		case "login":
			if msg.loggedIn {
				m.sync.err = ""
				m.sync.msg = "GitHub 登录好了，可以推送/拉取了"
			} else if setupErr != nil {
				m.sync.err = "登录没完成（" + setupErr.Error() + "）；可以再选一次「登录 GitHub CLI」"
			} else {
				m.sync.err = "还没登录成功；可以再选一次「登录 GitHub CLI」"
			}
		}

	case ghSetupMsg:
		switch msg.how {
		case "page":
			m.sync.busy = false
		default:
			// 安装器返回非零不代表没装上，一律重新看一眼再说话
			m.sync.ghRetry, m.sync.setupErr = msg.how, msg.err
			return m, ghCheckCmd()
		}
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

// forgetTool 把刚卸载的工具在列表里改回「没装」。
// 不整表重扫：重扫会把 hmck 自己读回正在运行的旧版本，也会把页面顶回列表
func (m *tuiModel) forgetTool(key string) {
	for i := range m.states {
		if m.states[i].Tool.Key != key {
			continue
		}
		t := m.states[i].Tool
		latest := m.states[i].Latest
		m.states[i] = toolState{Tool: t, Latest: latest}
		if !t.Self {
			// 自己那份删了，PATH 上别人的副本可能还在，卡片上照样说清楚
			m.states[i].Foreign = foreignCopies(t, "")
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
			// 本机上有哪份就启动哪份：hmck 装的那份优先，其次才是 PATH 上
			// 别人装的（brew 的 glab）。启动永远不会顺手装一份新的
			if bin := toolCopy(st); bin != "" {
				return m, launchCmd(st.Tool, bin)
			}
			return m.startInstall(st)
		case "u":
			if m.localReady && len(m.states) > 0 {
				return m.upgradeOne(m.states[m.cursor])
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
		case "g":
			if !m.localReady || len(m.states) == 0 {
				return m, nil
			}
			return m.startNotes()
		case "i":
			if !m.localReady || len(m.states) == 0 {
				return m, nil
			}
			return m.startInfo()
		case "d":
			if !m.localReady || len(m.states) == 0 {
				return m, nil
			}
			return m.startUninstall(m.states[m.cursor])
		case "s":
			m.state = working
			m.workText = "正在安装 agent skill…"
			return m, tea.Batch(m.spin.Tick, skillCmd())
		case "n":
			m.state = catalog
			if m.cat.loaded || m.cat.loading {
				return m, nil
			}
			return m.startCatalog()
		case "y":
			m.state = syncPage
			m.sync = syncView{}
			return m, ghCheckCmd()
		case "r":
			return m.refresh()
		case "q", "esc":
			return m, tea.Quit
		}

	case result:
		// 刚把自己卸载了：这进程还能跑，但没有下次了，看完结果就退出
		if m.exitOnResult {
			return m, tea.Quit
		}
		// 结果页的版本状态在结果消息里已经更新过，回列表直接切屏即可；
		// 再重扫一遍会把刚升级的 hmck 自己读回运行中的旧版本，也会让页面一直停在结果页
		if k.String() == "q" || k.String() == "esc" {
			return m, tea.Quit
		}
		m.state = list
		return m, nil

	case confirmUninstall:
		// 要敲工具名：q 之类的键得能打进输入框，所以退出只认 ctrl+c
		switch {
		case k.String() == "ctrl+c":
			return m, tea.Quit
		case k.String() == "esc":
			m.state = list
			m.confirm = uninstallView{}
			return m, nil
		case k.String() == "enter":
			if !m.confirm.nameMatches() {
				m.confirm.err = "名字不对，没删。照着上面那行敲 " + m.confirm.tool.Tool.Key
				return m, nil
			}
			st := m.confirm.tool
			m.state = working
			m.workText = "正在卸载 " + st.Tool.Key + "…"
			return m, tea.Batch(m.spin.Tick, uninstallCmd(st.Tool, false))
		case k.String() == "backspace":
			if r := []rune(m.confirm.typed); len(r) > 0 {
				m.confirm.typed = string(r[:len(r)-1])
			}
			m.confirm.err = ""
			return m, nil
		case k.Type == tea.KeyRunes:
			// 工具名都不长，限制一下长度；粘贴进来一长串也不至于撑破页面
			if r := []rune(m.confirm.typed); len(r) < 64 {
				m.confirm.typed += string(k.Runes)
			}
			m.confirm.err = ""
			return m, nil
		}
		return m, nil

	case addTool:
		// 和卸载确认页一样，q 之类的键要能打进输入框，退出只认 ctrl+c
		switch {
		case k.String() == "ctrl+c":
			return m, tea.Quit
		case k.String() == "esc":
			m.state = list
			m.add = addView{}
			return m, nil
		case k.String() == "tab", k.String() == "down":
			m.add.field = (m.add.field + 1) % 3
			return m, nil
		case k.String() == "shift+tab", k.String() == "up":
			m.add.field = (m.add.field + 2) % 3
			return m, nil
		case k.String() == "enter":
			return m.submitAdd()
		case k.String() == "backspace":
			m.add.backspace()
			m.add.err = ""
			return m, nil
		case k.Type == tea.KeyRunes:
			m.add.insert(string(k.Runes))
			m.add.err = ""
			return m, nil
		}
		return m, nil

	case catalog:
		switch k.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		case "esc":
			m.state = list
			return m, nil
		case "up", "k":
			if m.cat.cursor > 0 {
				m.cat.cursor--
			}
		case "down", "j":
			if m.cat.cursor < len(m.catalogRows())-1 {
				m.cat.cursor++
			}
		case "home":
			m.cat.cursor = 0
		case "end":
			m.cat.cursor = max(0, len(m.catalogRows())-1)
		case "a":
			// 目录里没有的，走原来的按仓库添加
			m.state = addTool
			m.add = addView{}
		case "r":
			return m.startCatalog()
		case "enter":
			return m.catalogInstall()
		}
		return m, nil

	case notes:
		switch k.String() {
		case "up", "k":
			m.note.scroll--
		case "down", "j":
			m.note.scroll++
		case "pgup", "b":
			m.note.scroll -= m.notesHeight()
		case "pgdown", "f", " ":
			m.note.scroll += m.notesHeight()
		case "home":
			m.note.scroll = 0
		case "end":
			m.note.scroll = m.notesMaxScroll()
		case "r":
			return m.reloadNotes()
		case "enter", "esc":
			m.state = list
			m.note = notesView{}
			return m, nil
		case "q":
			return m, tea.Quit
		}
		m.note.scroll = clampInt(m.note.scroll, 0, m.notesMaxScroll())
		return m, nil

	case infoPage:
		switch k.String() {
		case "enter", "esc":
			m.state = list
			m.info = infoView{}
			return m, nil
		case "g":
			return m.startNotesFor(m.info.tool)
		case "u":
			return m.upgradeOne(m.stateOf(m.info.tool.Key))
		case "d":
			return m.startUninstall(m.stateOf(m.info.tool.Key))
		case "r":
			return m.refresh() // 留在信息页，数据回来后原地刷新
		case "q":
			return m, tea.Quit
		}

	case syncPage:
		return m.handleSyncKey(k)
	}
	return m, nil
}

// syncItem 是同步页菜单里的一项
type syncItem struct {
	action string
	label  string
	desc   string
}

// syncItems 按本机是否已经连过同步给出菜单：没连过只留「开始」和「接上」，
// 免得第一屏就摆一堆还用不上的强制覆盖。gh 没装或没登录时，最上面先放
// 「安装 / 登录 GitHub CLI」——这两步不做，后面的推送拉取都没法开始。
func (m tuiModel) syncItems() []syncItem {
	var head []syncItem
	switch {
	case m.sync.ghChecked && !m.sync.ghFound:
		plan := ghInstaller()
		head = append(head, syncItem{"ghsetup", plan.label, plan.desc})
	case m.sync.ghChecked && m.sync.ghFound && !m.sync.ghLogin:
		head = append(head, syncItem{"ghlogin", "登录 GitHub CLI", "在页面里跑 gh auth login，浏览器里确认一下"})
	}

	st, _ := loadSyncState()
	if !syncEnabled(st) {
		return append(head,
			syncItem{"push", "开始同步（首次推送）", "在 GitHub 建私有仓库，把配置加密后推上去；要设一个同步密码"},
			syncItem{"pull", "从已有仓库拉取", "另一台设备推过之后，在这里输入同一个密码接上"},
		)
	}
	return append(head,
		syncItem{"push", "推送本机配置", "把本机改动加密后推上去"},
		syncItem{"pull", "拉取远端配置", "把远端较新的配置写回本机"},
		syncItem{"preview", "预览远端会改哪些文件", "只解密看清单，不动本机文件"},
		syncItem{"forcepush", "强制推送（覆盖远端）", "远端有别的设备的新版本时也拿本机覆盖"},
		syncItem{"forcepull", "强制拉取（覆盖本机）", "本机有没推上去的改动也覆盖"},
		syncItem{"addpath", "添加要同步的文件", "额外同步 config:… / home:… / ~/… 下的文件"},
		syncItem{"off", "忘掉本机的仓库和密钥", "远端仓库里的数据不动"},
	)
}

// handleSyncKey 管同步页：菜单、密码输入、路径输入三种状态
func (m tuiModel) handleSyncKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.sync.busy {
		return m, nil // 正在和 GitHub 通信：等结果，ctrl+c 仍然能退出
	}
	switch m.sync.mode {
	case syncPass:
		return m.handleSyncPassKey(k)
	case syncPath:
		return m.handleSyncPathKey(k)
	}

	items := m.syncItems()
	if m.sync.cursor >= len(items) {
		m.sync.cursor = len(items) - 1
	}
	if m.sync.cursor < 0 {
		m.sync.cursor = 0
	}
	switch k.String() {
	case "esc":
		m.state = list
		m.sync = syncView{}
	case "q":
		return m, tea.Quit
	case "r":
		// 重新看一眼 gh：手动装完、或者刚才检测失败时用
		m.sync.err, m.sync.msg = "", ""
		return m, ghCheckCmd()
	case "up", "k":
		if m.sync.cursor > 0 {
			m.sync.cursor--
		}
		m.sync.offArmed = false
	case "down", "j":
		if m.sync.cursor < len(items)-1 {
			m.sync.cursor++
		}
		m.sync.offArmed = false
	case "enter":
		return m.syncChoose(items[m.sync.cursor])
	}
	return m, nil
}

func (m tuiModel) syncChoose(it syncItem) (tea.Model, tea.Cmd) {
	switch it.action {
	case "ghsetup":
		plan := ghInstaller()
		m.sync.err, m.sync.msg = "", ""
		m.sync.offArmed = false
		if plan.cmd == nil {
			// 没有能自动装的包管理器：开官方下载页，装完按 r 重新检测
			m.sync.msg = "下载页已经在浏览器里打开；装好以后按 r 重新检测"
			return m, tea.ExecProcess(openURLCommand(ghDownloadURL), func(err error) tea.Msg {
				return ghSetupMsg{how: "page", err: err}
			})
		}
		m.sync.busy = true
		return m, tea.ExecProcess(plan.cmd, func(err error) tea.Msg {
			return ghSetupMsg{how: "install", err: err}
		})
	case "ghlogin":
		path, ok := ghPath()
		if !ok {
			m.sync.err = "没找到 gh；先选「安装 GitHub CLI」"
			return m, nil
		}
		m.sync.err, m.sync.msg = "", ""
		m.sync.offArmed = false
		m.sync.busy = true
		return m, tea.ExecProcess(exec.Command(path, "auth", "login"), func(err error) tea.Msg {
			return ghSetupMsg{how: "login", err: err}
		})
	case "addpath":
		m.sync.mode = syncPath
		m.sync.path = ""
		m.sync.err, m.sync.msg = "", ""
		return m, nil
	case "off":
		// 忘掉密钥是不可逆的（远端数据还在，但密码再也没人记得就解不开了），按两次
		if !m.sync.offArmed {
			m.sync.offArmed = true
			m.sync.err, m.sync.msg = "", "再按一次 enter：只让本机忘掉密钥和仓库，远端数据不动"
			return m, nil
		}
		m.sync.offArmed = false
		m.sync.busy = true
		m.sync.err, m.sync.msg = "", ""
		return m, syncRunCmd("off", "")
	}

	m.sync.offArmed = false
	// 推送/拉取都靠 gh 拿 token：没装或没登录时先去最上面那两项，别让用户白输一遍密码
	if m.sync.ghChecked && !m.sync.ghFound {
		m.sync.msg, m.sync.err = "", "还没装 GitHub CLI；先选「安装 GitHub CLI」"
		return m, nil
	}
	if m.sync.ghChecked && !m.sync.ghLogin {
		m.sync.msg, m.sync.err = "", "GitHub CLI 还没登录；先选「登录 GitHub CLI」"
		return m, nil
	}
	st, _ := loadSyncState()
	if st.Key == "" {
		// 本机没记住密钥：先从密码算出钥匙，再让后台执行
		m.sync.mode = syncPass
		m.sync.action = it.action
		m.sync.pass, m.sync.passFirst = "", ""
		// 第一次推送是设密码，要输两遍；拉取/预览只输一遍
		m.sync.confirm = it.action == "push" || it.action == "forcepush"
		m.sync.err, m.sync.msg = "", ""
		return m, nil
	}
	m.sync.busy = true
	m.sync.err, m.sync.msg = "", ""
	return m, syncRunCmd(it.action, "")
}

// handleSyncPassKey 在页面里收同步密码：掩码显示，输完才交给后台命令。
// 第一次推送输入两遍，确认没打错——密码丢了远端数据就解不开了。
func (m tuiModel) handleSyncPassKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case k.String() == "esc":
		m.sync.mode = syncIdle
		m.sync.pass, m.sync.passFirst = "", ""
		m.sync.confirm = false
		return m, nil
	case k.String() == "enter":
		if strings.TrimSpace(m.sync.pass) == "" {
			m.sync.err = "同步密码不能为空"
			return m, nil
		}
		if m.sync.confirm && m.sync.passFirst == "" {
			m.sync.passFirst = m.sync.pass
			m.sync.pass = ""
			m.sync.err = ""
			return m, nil
		}
		if m.sync.confirm && m.sync.pass != m.sync.passFirst {
			m.sync.passFirst, m.sync.pass = "", ""
			m.sync.err = "两次输入的密码不一样"
			return m, nil
		}
		action, pass := m.sync.action, m.sync.pass
		m.sync.mode = syncIdle
		m.sync.pass, m.sync.passFirst = "", ""
		m.sync.confirm = false
		m.sync.busy = true
		m.sync.err, m.sync.msg = "", ""
		return m, syncRunCmd(action, pass)
	case k.String() == "backspace":
		if r := []rune(m.sync.pass); len(r) > 0 {
			m.sync.pass = string(r[:len(r)-1])
		}
		m.sync.err = ""
		return m, nil
	case k.Type == tea.KeyRunes:
		// 密码不会特别长，挡一下粘贴进来的大段文字
		if r := []rune(m.sync.pass); len(r) < 256 {
			m.sync.pass += string(k.Runes)
		}
		m.sync.err = ""
		return m, nil
	}
	return m, nil
}

func (m tuiModel) handleSyncPathKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case k.String() == "esc":
		m.sync.mode = syncIdle
		m.sync.path = ""
		return m, nil
	case k.String() == "enter":
		name, added, err := addSyncPath(m.sync.path)
		if err != nil {
			m.sync.err = err.Error()
			return m, nil
		}
		m.sync.mode = syncIdle
		m.sync.path = ""
		m.sync.err = ""
		if added {
			m.sync.msg = "已加入同步清单: " + name + "（回到菜单选「推送本机配置」推一次）"
		} else {
			m.sync.msg = name + " 已经在同步清单里了"
		}
		return m, nil
	case k.String() == "backspace":
		if r := []rune(m.sync.path); len(r) > 0 {
			m.sync.path = string(r[:len(r)-1])
		}
		m.sync.err = ""
		return m, nil
	case k.Type == tea.KeyRunes:
		if r := []rune(m.sync.path); len(r) < 512 {
			m.sync.path += string(k.Runes)
		}
		m.sync.err = ""
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
	cmds := []tea.Cmd{m.spin.Tick, localInspectCmd(m.toolList())}
	if c := m.prefetchChangelogs(); c != nil {
		cmds = append(cmds, c) // 更新日志也一起重抓
	}
	return m, tea.Batch(cmds...)
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
	if m.state == notes {
		return m.note.loading
	}
	if m.state == infoPage {
		return !m.localReady || len(m.pending) > 0 // 信息页上「读取中」的圈也得转
	}
	if m.state == catalog {
		return m.cat.loading
	}
	if m.state == syncPage {
		return m.sync.busy
	}
	return m.state == list && (!m.localReady || len(m.pending) > 0)
}

// startCatalog 去读一遍线上目录。读不到会退回程序自带的那份，页面上注明
func (m tuiModel) startCatalog() (tea.Model, tea.Cmd) {
	m.cat.loading = true
	m.cat.err = ""
	m.catGen++
	return m, tea.Batch(m.spin.Tick, catalogFetchCmd(m.catGen))
}

// catalogRows 是目录页上看到的全部工具：线上目录那份在前，
// 本机自己 hmck add 的不在目录里的补在后面，别让人找不到自己加过的
func (m tuiModel) catalogRows() []tool {
	rows := append([]tool(nil), m.cat.rows...)
	seen := make(map[string]bool, len(rows))
	for _, t := range rows {
		seen[strings.ToLower(t.Key)] = true
	}
	for _, st := range m.states {
		if !seen[strings.ToLower(st.Tool.Key)] {
			rows = append(rows, st.Tool)
		}
	}
	return rows
}

func (m tuiModel) stateOfKey(key string) (toolState, bool) {
	for _, st := range m.states {
		if strings.EqualFold(st.Tool.Key, key) {
			return st, true
		}
	}
	return toolState{}, false
}

// catalogInstall 是目录页上的 enter：已经加入的直接装/升级，
// 还没加入的先写进本机配置再装；本平台没包的提前说清楚，不白下一趟
func (m tuiModel) catalogInstall() (tea.Model, tea.Cmd) {
	rows := m.catalogRows()
	if len(rows) == 0 {
		return m, nil
	}
	if m.cat.cursor >= len(rows) {
		m.cat.cursor = len(rows) - 1
	}
	t := rows[m.cat.cursor]
	if !platformSupported(t) {
		m.state = result
		m.resultLines = []resultLine{{
			text: t.Key + " 没有 " + runtime.GOOS + "/" + runtime.GOARCH + " 的发布",
			ok:   false,
		}}
		return m, nil
	}
	if st, ok := m.stateOfKey(t.Key); ok {
		if !m.localReady {
			m.cat.err = "本机还在检查，稍等一下再按 enter"
			return m, nil
		}
		return m.startInstall(st)
	}
	m.state = working
	m.workText = "正在加入 " + t.Key + "…"
	return m, tea.Batch(m.spin.Tick, catalogAddCmd(t))
}

// catalogStatus 说清目录里这一行现在什么情况：装在哪、能不能升、还是没加进来
func (m tuiModel) catalogStatus(t tool) (string, string) {
	if !platformSupported(t) {
		return "本平台没有", "missing"
	}
	st, ok := m.stateOfKey(t.Key)
	if !ok {
		return "未添加", "missing"
	}
	if !m.localReady {
		return "检查中…", "warn"
	}
	if st.Behind() {
		return st.Installed + " → " + st.Latest, "warn"
	}
	if st.Missing() && len(st.Foreign) > 0 {
		if st.ForeignVer != "" {
			return "PATH 上 " + st.ForeignVer, "warn"
		}
		return "PATH 上已有一份", "warn"
	}
	if st.Missing() {
		return "未安装", "missing"
	}
	if st.CheckErr != "" {
		return st.Installed + "（没查到新版）", "warn"
	}
	return st.Installed, "ok"
}

// submitAdd 收起新增表单：和 `hmck add <repo>` 走同一条路——先落进本机配置，
// 再去读发布信息补平台，读到了就直接接着装
func (m tuiModel) submitAdd() (tea.Model, tea.Cmd) {
	a := &m.add
	a.err = ""
	repo, err := parseRepo(a.repo)
	if err != nil {
		a.err = err.Error()
		a.field = 0
		return m, nil
	}
	t := tool{Repo: repo, Desc: a.desc, Custom: true}
	t.Key = strings.TrimSpace(a.key)
	if t.Key == "" {
		t.Key = defaultKeyForRepo(repo)
	}
	if isBuiltin(t.Key) {
		a.err = t.Key + " 是内置工具，换个 key 再加"
		a.field = 1
		return m, nil
	}
	for _, st := range m.states {
		if strings.EqualFold(st.Tool.Key, t.Key) {
			a.err = t.Key + " 已经在列表里了"
			a.field = 1
			return m, nil
		}
	}
	if err := normalizeCustom(&t); err != nil {
		a.err = err.Error()
		return m, nil
	}
	t.Custom = true
	m.state = working
	m.workText = "正在读取 " + repo + " 的发布信息…"
	return m, tea.Batch(m.spin.Tick, addToolCmd(t))
}

func (m tuiModel) startInstall(st toolState) (tea.Model, tea.Cmd) {
	if st.Installed != "" && st.Latest != "" && !isNewer(st.Latest, st.Installed) {
		m.state = result
		m.resultLines = []resultLine{{text: st.Tool.Key + " " + st.Installed + " 已是最新", ok: true}}
		return m, nil
	}
	m.state = working
	if st.Installed == "" {
		m.workText = "正在下载并安装 " + st.Tool.Key + "…"
	} else {
		m.workText = "正在下载并替换 " + st.Tool.Key + "…"
	}
	m.resetProgress()
	ch, gen := m.newProgressCh()
	return m, tea.Batch(m.spin.Tick, installCmd(st, ch, gen), waitUpd(ch, gen))
}

// newProgressCh 开一条新的进度通道。新开一条而不是复用旧的：
// 上一次没读完的残留消息不带着旧的 gen 进来，界面上就不会突然跳一下
func (m *tuiModel) newProgressCh() (chan tea.Msg, int) {
	m.updGen++
	m.updCh = make(chan tea.Msg, 16)
	return m.updCh, m.updGen
}

// resetProgress 清掉上一个操作的进度，换工具时进度条从 0 开始画
func (m *tuiModel) resetProgress() {
	m.progIndex, m.progTotal = 0, 0
	m.progDone, m.progBytes = 0, 0
}

// upgradeOne 是 u 键。hmck 只查看 PATH 上已有的那份的工具（brew 的 glab），
// 按下升级也只说清楚「用的是哪份」，不再往 hmck 目录里另装一份——
// 多出来的副本会排在 PATH 前面，把用户和 agent 一直在用的那份顶掉
func (m tuiModel) upgradeOne(st toolState) (tea.Model, tea.Cmd) {
	if st.Tool.OwnDir && st.Installed == "" && len(st.Foreign) > 0 {
		ver := st.ForeignVer
		if ver != "" {
			ver = "（v" + ver + "）"
		}
		m.state = result
		m.resultLines = []resultLine{
			{text: st.Tool.Key + " 用的是 PATH 上 " + shortenHome(st.Foreign[0]) + ver, ok: true},
			{text: "这一份归 brew / 系统管，hmck 只查看和启动它，不会另装一份", ok: true},
			{text: "要换版本用它自己的包管理器；确实要 hmck 的副本才用 hmck install " + st.Tool.Key, ok: true},
		}
		return m, nil
	}
	return m.startInstall(st)
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
	m.resetProgress()
	m.batchAction = "更新"
	m.workText = fmt.Sprintf("正在更新 %d 个工具…", len(targets))
	ch, gen := m.newProgressCh()
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
	m.resetProgress()
	m.batchAction = "预下载"
	m.workText = fmt.Sprintf("正在下载 %d 个工具的新版…", len(targets))
	ch, gen := m.newProgressCh()
	return m, tea.Batch(m.spin.Tick, downloadAllCmd(targets, ch, gen), waitUpd(ch, gen))
}

// ---------- 更新日志 ----------

func (m tuiModel) startNotes() (tea.Model, tea.Cmd) {
	return m.startNotesFor(m.states[m.cursor].Tool)
}

// startNotesFor 打开指定工具的更新日志；从列表和信息页都能进来
func (m tuiModel) startNotesFor(t tool) (tea.Model, tea.Cmd) {
	st := m.stateOf(t.Key)
	m.state = notes
	m.note = notesView{tool: t, current: st.Installed, latest: st.Latest}
	if c, ok := m.chg[t.Key]; ok && len(c.entries) > 0 {
		m.note.entries = c.entries
		return m, nil
	}
	return m.loadNotes()
}

// startInfo 打开信息页：装在哪、是不是最新、登录成了谁
func (m tuiModel) startInfo() (tea.Model, tea.Cmd) {
	m.state = infoPage
	m.info = infoView{tool: m.states[m.cursor].Tool}
	return m, nil
}

// startUninstall 按 d 之后先进确认页：删哪一份、留哪一份，看完把工具名敲一遍才动手。
// hmck 目录里没有这个工具就直接说清楚，不开一个空确认页
func (m tuiModel) startUninstall(st toolState) (tea.Model, tea.Cmd) {
	if managedCopy(st.Tool) == "" {
		m.state = result
		m.resultLines = []resultLine{{text: "hmck 目录里没有 " + st.Tool.Key + "，没什么可卸载的", ok: false}}
		for _, p := range st.Foreign {
			m.resultLines = append(m.resultLines,
				resultLine{text: "  PATH 上的 " + shortenHome(p) + "（不是 hmck 装的，不归 hmck 删）", ok: true})
		}
		return m, nil
	}
	m.state = confirmUninstall
	m.confirm = uninstallView{tool: st}
	return m, nil
}

// stateOf 按 key 找当前状态；找不到就返回一个只有工具信息的空壳
func (m tuiModel) stateOf(key string) toolState {
	for _, st := range m.states {
		if st.Tool.Key == key {
			return st
		}
	}
	if m.info.tool.Key == key {
		return toolState{Tool: m.info.tool}
	}
	return toolState{}
}

func (m tuiModel) loadNotes() (tea.Model, tea.Cmd) {
	m.note.loading = true
	m.note.err = ""
	m.note.scroll = 0
	m.note.gen++
	return m, tea.Batch(m.spin.Tick, changelogCmd(m.note.tool, changelogVersionLimit, m.note.gen, false))
}

func (m tuiModel) reloadNotes() (tea.Model, tea.Cmd) {
	delete(m.chg, m.note.tool.Key)
	return m.loadNotes()
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
	// 开关也是同步内容：保存后顺手推一下（没开同步时什么都不做）
	go autoSyncPush()
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
	case notes:
		body = m.viewNotes()
	case infoPage:
		body = m.viewInfo()
	case confirmUninstall:
		body = m.viewConfirm()
	case addTool:
		body = m.viewAdd()
	case catalog:
		body = m.viewCatalog()
	case syncPage:
		body = m.viewSync()
	}
	return "\n" + body + "\n"
}

// withLogo 把 hmck 的招牌（docs/logos/hmck.svg 渲染出来的）摆在左边，右边逐行接上文字
func (m tuiModel) withLogo(lines ...string) string {
	logo := toolLogo(tool{Key: "hmck"})
	var b strings.Builder
	for i, l := range logo {
		b.WriteString(l)
		if i < len(lines) && lines[i] != "" {
			b.WriteString("  " + lines[i])
		}
		b.WriteString("\n")
	}
	return b.String()
}

func (m tuiModel) viewList() string {
	body := m.viewListBody(false, 0)
	if m.height <= 0 || strings.Count(body, "\n")+3 <= m.height {
		return body
	}
	// 终端不够高：先换成「一行一个工具」的紧凑版
	compact := m.viewListBody(true, 0)
	if strings.Count(compact, "\n")+3 <= m.height {
		return compact
	}
	// 还是放不下（工具很多）：只画光标附近那几行，标题和提示始终留在屏上
	return m.viewListBody(true, max(m.height-2-m.listFixedLines(), 1))
}

// listFixedLines 是列表区以外固定要占的行：招牌 3 行、同步/错误提示、
// 列表上下各一个空行、按键提示（按键提示窄屏会折行，按实际行数算）
func (m tuiModel) listFixedLines() int {
	n := logoRowsN + 2
	if m.syncLine != "" {
		n++
	}
	if m.configErr != "" {
		n++
	}
	if m.cfgErr != "" {
		n++
	}
	n += strings.Count(m.listHints(), "\n") + 1
	return n
}

func (m tuiModel) listHints() string {
	return m.viewHints("↑↓", "选择", "enter", "启动/安装", "n", "工具目录", "u", "升级", "U", "全部更新",
		"i", "信息", "g", "更新日志", "y", "同步", "d", "卸载", "s", "装 skill", "r", "重新检查", "q", "退出")
}

// listRow 是紧凑列表里的一行：分组标题或某个工具
type listRow struct {
	kind string // 非空 = 分组标题
	idx  int    // 工具下标；标题行是 -1
}

// viewListBody 画列表。compact 时一个工具一行；budget > 0 时只画窗口里那几行，
// 光标所在行和它的分组标题尽量留在窗口里。
func (m tuiModel) viewListBody(compact bool, budget int) string {
	var b strings.Builder
	w := m.contentWidth()
	cols := m.cardCols()

	behind, missing := 0, 0
	for _, st := range m.states {
		if st.Behind() {
			behind++
		}
		// PATH 上已经有一份的（brew 的 glab）不算「未安装」：本机明明能跑
		if st.Missing() && len(st.Foreign) == 0 {
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
	header := []string{
		styles.Title.Render("hmck") + styles.Dim.Render("  我的命令行小工具"),
		styles.Dim.Render(summary),
		m.viewSwitches(),
	}
	b.WriteString(m.withLogo(header...))
	// 同步状态单独占一行：withLogo 只接招牌高度的文字，第四行会被吞掉
	if m.syncLine != "" {
		line := styles.Dim.Render(theme.Truncate(m.syncLine, w-2))
		if strings.HasPrefix(m.syncLine, "同步失败") {
			line = styles.Warn.Render(theme.Truncate(m.syncLine, w-2))
		}
		b.WriteString("  " + line)
		b.WriteString("\n")
	}
	if m.configErr != "" {
		b.WriteString(styles.Warn.Render("  自定义工具配置有问题: " + theme.Truncate(m.configErr, w)))
		b.WriteString("\n")
	}
	if m.cfgErr != "" {
		b.WriteString(styles.Warn.Render("  开关没保存: " + theme.Truncate(m.cfgErr, w)))
		b.WriteString("\n")
	}
	b.WriteString("\n")

	// 分栏：官方 CLI / 自己的工具 / 自己加的；标题带上这一栏有几个
	counts := map[string]int{}
	for _, st := range m.states {
		counts[toolKind(st.Tool)]++
	}
	if compact {
		m.writeCompactList(&b, cols, counts, budget)
	} else {
		lastKind := ""
		for i, st := range m.states {
			kind := toolKind(st.Tool)
			if kind != lastKind {
				if i > 0 {
					b.WriteString("\n")
				}
				b.WriteString("  " + styleGroup.Render(kindTitle(kind)) +
					styles.Dim.Render(fmt.Sprintf(" · %d 个", counts[kind])))
				b.WriteString("\n")
				lastKind = kind
			}
			b.WriteString(m.renderCard(st, i == m.cursor, cols))
			b.WriteString("\n")
			// 同一栏里工具之间用一条淡线隔开；换栏时靠标题和空行分，不再画线
			if i < len(m.states)-1 && toolKind(m.states[i+1].Tool) == kind {
				b.WriteString("  " + styleRule.Render(strings.Repeat("─", w)))
				b.WriteString("\n")
			}
		}
	}
	b.WriteString("\n")
	b.WriteString(m.listHints())
	return b.String()
}

func (m tuiModel) writeCompactList(b *strings.Builder, cols cardCols, counts map[string]int, budget int) {
	var rows []listRow
	lastKind := ""
	for i, st := range m.states {
		kind := toolKind(st.Tool)
		if kind != lastKind {
			rows = append(rows, listRow{kind: kind, idx: -1})
			lastKind = kind
		}
		rows = append(rows, listRow{idx: i})
	}
	cur := 0
	for i, r := range rows {
		if r.idx == m.cursor {
			cur = i
			break
		}
	}

	start, end := 0, len(rows)
	topHidden, bottomHidden := 0, 0
	if budget > 0 && len(rows) > budget {
		n := budget
		start = cur - n/2
		if start < 0 {
			start = 0
		}
		if start+n > len(rows) {
			start = len(rows) - n
		}
		end = start + n
		// 光标上面那行分组标题能一起放进来就带上，免得看不到自己在哪一栏
		g := -1
		for i := cur; i >= 0; i-- {
			if rows[i].idx < 0 {
				g = i
				break
			}
		}
		if g >= 0 && g < start && cur-g+1 <= n {
			start = g
			end = start + n
			if end > len(rows) {
				end = len(rows)
				start = end - n
			}
		}
		// 提示行占掉的位置从窗口两端让出来，光标那行不会被挤掉
		if start > 0 && start < cur {
			start++
			topHidden = start
		}
		if end < len(rows) && end-1 > cur {
			end--
			bottomHidden = len(rows) - end
		}
	}
	if topHidden > 0 {
		b.WriteString("  " + styles.Dim.Render(fmt.Sprintf("↑ 上面还有 %d 行", topHidden)))
		b.WriteString("\n")
	}
	for i := start; i < end; i++ {
		r := rows[i]
		if r.idx < 0 {
			b.WriteString("  " + styleGroup.Render(kindTitle(r.kind)) +
				styles.Dim.Render(fmt.Sprintf(" · %d 个", counts[r.kind])))
			b.WriteString("\n")
			continue
		}
		b.WriteString(m.renderCompactCard(m.states[r.idx], r.idx == m.cursor, cols))
		b.WriteString("\n")
	}
	if bottomHidden > 0 {
		b.WriteString("  " + styles.Dim.Render(fmt.Sprintf("↓ 下面还有 %d 行", bottomHidden)))
		b.WriteString("\n")
	}
}

// renderCompactCard 是紧凑版卡片：一个工具一行，不带招牌、没有分隔线。
// 终端高度不够时用它，标题、分组和按键提示就能留在同一屏里。
func (m tuiModel) renderCompactCard(st toolState, selected bool, cols cardCols) string {
	w := max(m.contentWidth()-2, 24) // 卡片自己占掉左边框和缩进 2 列

	nameStyle := styleNameOff
	if selected {
		nameStyle = styleNameOn
	}
	head := nameStyle.Render(theme.Pad(st.Tool.Key, cols.name)) + "  " +
		theme.Pad(m.versionCell(st), cols.ver)

	desc := st.Tool.Desc
	hint, hintStyle := "", styles.Dim
	switch {
	case !m.localReady:
		desc, hint = "", "  读取中…"
	case st.Installed == "" && st.CheckErr != "":
		hint, hintStyle = "  "+st.CheckErr, styles.Warn
	case st.Installed == "" && len(st.Foreign) > 0:
		hint = "  ·  PATH 上已有一份"
	case st.Installed == "":
		hint = "  ·  按 enter 安装"
	case st.Latest != "" && m.staged[st.Tool.Key] == st.Latest:
		hint, hintStyle = "  ·  按 u 安装", styles.Good
	case st.CheckErr != "":
		hint, hintStyle = "  ·  "+st.CheckErr, styles.Warn
	}

	p := ""
	if st.Path != "" {
		p = shortenHome(st.Path)
	}
	avail := max(w-lipgloss.Width(head)-2, 10) // 名称和版本之后留给说明和路径
	if p != "" && avail-lipgloss.Width(hint)-12 < lipgloss.Width(p) {
		p = "" // 路径不能把说明挤没
	}
	room := avail - lipgloss.Width(hint) - lipgloss.Width(p) - 2
	if room < 12 && hint != "" {
		hint, hintStyle = "", styles.Dim // 放不下提示就只留说明
		room = avail - lipgloss.Width(p) - 2
	}
	room = max(room, 4)

	line := head + "  " + stylePath.Render(theme.Truncate(desc, room))
	if hint != "" {
		line += hintStyle.Render(hint)
	}
	if p != "" {
		line += strings.Repeat(" ", max(w-lipgloss.Width(line)-lipgloss.Width(p), 2)) +
			stylePath.Render(p)
	}
	if selected {
		return styleCardOn.Render(line)
	}
	return styles.CardOff.Render(line)
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
	case st.Installed == "" && len(st.Foreign) > 0:
		// 用的是 PATH 上别人装的那份：版本刚好也是最新给个 ✓，否则按「不归 hmck 管」处理
		if st.ForeignVer != "" && st.Latest != "" && !isNewer(st.Latest, st.ForeignVer) {
			return "✓", styles.Good
		}
		return "·", styles.Dim
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
	case st.Installed == "" && st.ForeignVer != "":
		// hmck 这边没装，但 PATH 上有一份能用的：写它是哪一版，别让人以为这台机器没有
		return iconStyle.Render(icon) + " " + styleVerOld.Render("PATH 上的 "+st.ForeignVer)
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

// renderCard 左边是这个工具的招牌，右边三行：
// 名称 · 版本 · 上次更新时间 / 作用 · 路径 / 仓库 · 最近一版改了什么。
// 路径贴右边，版本那几列不会因为路径长短而跑位
func (m tuiModel) renderCard(st toolState, selected bool, cols cardCols) string {
	logo := toolLogo(st.Tool)
	gutter := lipgloss.Width(logo[0]) + 2
	// 卡片自己占掉左边框和缩进 2 列，招牌再占 gutter 列
	w := m.contentWidth() - 2 - gutter
	if w < 30 {
		w = 30
	}

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
	case st.Installed == "" && len(st.Foreign) > 0:
		// PATH 上已经有一份别人的（brew 装的 glab 之类）：hmck 只查看和启动它，
		// 不接管、不另装一份
		tail, tailStyle = "  ·  PATH 上已有一份，hmck 只查看；按 enter 就用它", styles.Dim
		tailShort = "  ·  PATH 上已有一份"
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

	// 第三行：仓库 + 登录状态（glab 这类）+ 最近一版的说明。
	// 登录状态排在日志前面：登没登录比这版改了什么更要紧
	line3 := stylePath.Render(st.Tool.Repo)
	used := lipgloss.Width(st.Tool.Repo)
	if st.Login != "" {
		if avail := w - used - 5; avail >= 12 {
			badge := theme.Truncate(st.Login, avail)
			line3 += styles.Dim.Render("  ·  ") + kindStyle(st.LoginKind).Render(badge)
			used += 5 + lipgloss.Width(badge)
		}
	}
	if c, ok := m.chg[st.Tool.Key]; ok && len(c.entries) > 0 {
		if s := c.entries[0].summary(); s != "" {
			note := "v" + c.entries[0].Version + "  " + s
			if avail := w - used - 5; avail >= 12 {
				line3 += styles.Dim.Render("  ·  " + theme.Truncate(note, avail))
			}
		}
	}

	lines := []string{head, line2, line3}
	// 招牌比文字行高：文字在招牌中间那几行，上下各留一点，看着不像是贴顶了
	top := max((len(logo)-len(lines))/2, 0)
	var b strings.Builder
	for i, l := range logo {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(l)
		if j := i - top; j >= 0 && j < len(lines) {
			b.WriteString("  ")
			b.WriteString(lines[j])
		}
	}

	body := b.String()
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
	out := styles.Title.Render("hmck") + styles.Dim.Render("  处理中") + "\n\n" +
		"  " + m.spin.View() + "  " + styles.Dim.Render(m.workText)
	if bar := m.progressBar(); bar != "" {
		out += "\n" + bar
	}
	return out
}

// progressBar 画下载进度：只装一个工具时就是它自己下到哪了，
// 一键更新时把「第几个」和「这个下到哪」合成一条总进度，还没开始的算 0。
// 下载真正占时间，下载前（查版本）和解压后（替换文件）都没有比例可算，不画条
func (m tuiModel) progressBar() string {
	if m.progBytes <= 0 && m.progDone <= 0 {
		return ""
	}
	// 对方没给 Content-Length、也不是一键更新：画不出比例，只报已下载多少
	if m.progBytes <= 0 && m.progTotal == 0 {
		return "     " + styles.Dim.Render("已下载 "+humanSize(m.progDone))
	}

	inner := 0.0
	if m.progBytes > 0 {
		inner = float64(m.progDone) / float64(m.progBytes)
		inner = min(inner, 1)
	}
	frac := inner
	if m.progTotal > 1 {
		frac = (float64(m.progIndex-1) + inner) / float64(m.progTotal)
	}

	width := 24
	if m.width > 0 {
		width = clampInt(m.width-38, 12, 30)
	}
	filled := int(frac*float64(width) + 0.5)
	bar := styleBarFill.Render(strings.Repeat(barFill, filled)) +
		styleBarEmpty.Render(strings.Repeat(barEmpty, width-filled))

	// 对齐到上面那行字：两个空格缩进 + 转圈的一个字符 + 两个空格
	out := "     " + bar + "  " + styles.Dim.Render(fmt.Sprintf("%3d%%", int(frac*100+0.5)))
	if m.progBytes > 0 {
		out += "  " + styles.Dim.Render(humanSize(m.progDone)+" / "+humanSize(m.progBytes))
	} else {
		out += "  " + styles.Dim.Render("已下载 "+humanSize(m.progDone))
	}
	return out
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
	if m.exitOnResult {
		b.WriteString(styles.Hints("任意键", "退出"))
	} else {
		b.WriteString(styles.Hints("任意键", "返回"))
	}
	return b.String()
}

// viewNotes 是更新日志页：左边是所选工具的招牌，右边是版本号和状态
func (m tuiModel) viewNotes() string {
	var b strings.Builder
	logo := toolLogo(m.note.tool)

	sub := ""
	switch {
	case m.note.current != "" && m.note.latest != "":
		sub = styles.Dim.Render("当前 " + m.note.current + " · 最新 " + m.note.latest)
	case m.note.current != "":
		sub = styles.Dim.Render("当前 " + m.note.current)
	case m.note.latest != "":
		sub = styles.Dim.Render("最新 " + m.note.latest + " · 还没安装")
	default:
		sub = styles.Dim.Render("还没安装")
	}
	head := []string{
		styles.Title.Render(m.note.tool.Key) + styles.Dim.Render("  更新日志"),
		sub,
		styles.Dim.Render(m.note.tool.Repo),
	}
	for i, l := range logo {
		b.WriteString(l)
		if i < len(head) {
			b.WriteString("  " + head[i])
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")

	lines := m.notesLines(m.contentWidth())
	start := clampInt(m.note.scroll, 0, max(len(lines)-m.notesHeight(), 0))
	end := min(start+m.notesHeight(), len(lines))
	for _, l := range lines[start:end] {
		b.WriteString(l + "\n")
	}
	// 内容不满一屏时补空行，底部提示不会跟着跳
	for i := end - start; i < m.notesHeight(); i++ {
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(m.viewHints("↑↓", "滚动", "pgup/pgdn", "翻页", "r", "刷新", "esc", "返回"))
	return b.String()
}

// viewInfo 是信息页：左边招牌，右边一句话讲清情况，下面一行行明细。
// 内容每次从 states 现算，所以后台查回来的最新版本会直接出现在页面上
func (m tuiModel) viewInfo() string {
	st := m.stateOf(m.info.tool.Key)
	info := toolInfoFor(st)
	w := m.contentWidth()

	head := []string{
		styles.Title.Render(st.Tool.Key) + styles.Dim.Render("  信息"),
		kindStyle(info.Kind).Render(theme.Truncate(info.Headline, w-4)),
		styles.Dim.Render(st.Tool.Repo),
	}
	var b strings.Builder
	for i, l := range toolLogo(st.Tool) {
		b.WriteString(l)
		if i < len(head) {
			b.WriteString("  " + head[i])
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")

	if !m.localReady {
		b.WriteString("  " + m.spin.View() + "  " + styles.Dim.Render("正在读取本地安装位置…") + "\n")
	} else {
		keyW := 2
		for _, f := range info.Fields {
			keyW = max(keyW, lipgloss.Width(f.Key))
		}
		keyW = min(keyW, 10)
		for _, f := range info.Fields {
			for i, v := range strings.Split(f.Value, "\n") {
				key := strings.Repeat(" ", keyW)
				if i == 0 {
					key = styles.Dim.Render(theme.Pad(f.Key, keyW))
				}
				b.WriteString("  " + key + "  " +
					kindStyle(f.Kind).Render(theme.Truncate(v, w-keyW-4)) + "\n")
			}
		}
	}
	if info.Err != "" {
		b.WriteString("  " + styles.Bad.Render(theme.Truncate(info.Err, w-2)) + "\n")
	}

	b.WriteString("\n")
	b.WriteString(m.viewHints("g", "更新日志", "u", "升级/安装", "d", "卸载", "r", "重新检查", "esc", "返回"))
	return b.String()
}

// viewConfirm 是卸载确认页：先摊开删什么、留什么，再让人把工具名敲一遍。
// 删列表用的是和 info 页同一套行格式，颜色一眼就能分出「删」和「留」
func (m tuiModel) viewConfirm() string {
	st := m.confirm.tool
	fields := uninstallPreview(st)
	w := m.contentWidth()

	head := []string{
		styles.Title.Render(st.Tool.Key) + styles.Dim.Render("  卸载"),
		styles.Warn.Render("下面这些会被删掉"),
		styles.Dim.Render(st.Tool.Repo),
	}
	var b strings.Builder
	for i, l := range toolLogo(st.Tool) {
		b.WriteString(l)
		if i < len(head) {
			b.WriteString("  " + head[i])
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")

	keyW := 2
	for _, f := range fields {
		keyW = max(keyW, lipgloss.Width(f.Key))
	}
	for _, f := range fields {
		b.WriteString("  " + styles.Dim.Render(theme.Pad(f.Key, keyW)) + "  " +
			kindStyle(f.Kind).Render(theme.Truncate(f.Value, w-keyW-4)) + "\n")
	}
	if st.Tool.Self {
		b.WriteString("\n")
		b.WriteString("  " + styles.Bad.Render("这是 hmck 自己：删了 hmck 就没了，要装回来得重新下载一次。") + "\n")
	}

	b.WriteString("\n")
	b.WriteString("  " + styles.Dim.Render("输入 ") + styles.Key.Render(st.Tool.Key) +
		styles.Dim.Render(" 再按 enter 删除，esc 取消") + "\n")
	cursor := styles.Accent.Render("> ") + m.confirm.typed + styles.Accent.Render("█")
	if m.confirm.nameMatches() {
		cursor = styles.Good.Render("> ") + styles.Good.Render(m.confirm.typed) + styles.Good.Render("█")
	}
	b.WriteString(cursor + "\n")
	if m.confirm.err != "" {
		b.WriteString("  " + styles.Bad.Render(m.confirm.err) + "\n")
	}

	b.WriteString("\n")
	b.WriteString(m.viewHints("enter", "确认卸载", "esc", "算了", "ctrl+c", "退出"))
	return b.String()
}

// viewCatalog 是工具目录页：一行一个工具，右边是它在本机的状态。
// 选中那一行下面补出仓库和平台，够判断要不要装
func (m tuiModel) viewCatalog() string {
	w := m.contentWidth()
	rows := m.catalogRows()
	added := 0
	for _, t := range rows {
		if _, ok := m.stateOfKey(t.Key); ok {
			added++
		}
	}
	source := "在线目录"
	if m.cat.source == "bundled" {
		source = "随程序内置的一份"
	}

	var b strings.Builder
	b.WriteString(m.withLogo(
		styles.Title.Render("工具目录")+styles.Dim.Render("  线上维护，挑着装"),
		styles.Dim.Render(fmt.Sprintf("%d 个工具 · 已加入 %d 个 · %s", len(rows), added, source)),
		styles.Dim.Render("enter 安装/加入 · a 按仓库添加 · r 刷新目录"),
	))
	b.WriteString("\n")

	if m.cat.loading && !m.cat.loaded {
		b.WriteString("  " + m.spin.View() + "  " + styles.Dim.Render("正在读取在线目录…") + "\n\n")
		b.WriteString(m.viewHints("esc", "返回", "ctrl+c", "退出"))
		return b.String()
	}
	if m.cat.err != "" && len(rows) > 0 {
		b.WriteString("  " + styles.Warn.Render(theme.Truncate(m.cat.err, max(w-2, 8))) + "\n")
	}
	if m.cat.skipped > 0 {
		b.WriteString("  " + styles.Dim.Render(fmt.Sprintf("目录里有 %d 条读不懂，已跳过", m.cat.skipped)) + "\n")
	}
	if len(rows) == 0 {
		b.WriteString("  " + styles.Warn.Render(theme.Truncate("目录读不到："+m.cat.err, max(w-2, 8))) + "\n\n")
		b.WriteString(m.viewHints("a", "按仓库添加", "r", "重试", "esc", "返回"))
		return b.String()
	}
	b.WriteString("\n")

	keyW, statusW := lipgloss.Width("KEY"), lipgloss.Width("状态")
	for _, t := range rows {
		keyW = max(keyW, lipgloss.Width(t.Key))
		status, _ := m.catalogStatus(t)
		statusW = max(statusW, lipgloss.Width(status))
	}
	keyW = min(keyW, 20)
	statusW = min(statusW, max(w/3, 8))
	descW := max(w-2-2-keyW-2-statusW-2, 10)

	// 目录以后会越来越长，只画放得下的那几行，光标走到哪就跟到哪
	perPage := max(m.height-14, 5)
	start := 0
	if m.cat.cursor >= perPage {
		start = m.cat.cursor - perPage + 1
	}
	end := min(len(rows), start+perPage)
	if start > 0 {
		b.WriteString("  " + styles.Dim.Render(fmt.Sprintf("↑ 上面还有 %d 个", start)) + "\n")
	}

	for i := start; i < end; i++ {
		t := rows[i]
		status, kind := m.catalogStatus(t)
		marker := "  "
		descStyle := styles.Dim
		if i == m.cat.cursor {
			marker = styles.Accent.Render("▌ ")
			descStyle = lipgloss.NewStyle()
		}
		b.WriteString("  " + marker +
			styles.Key.Render(theme.Pad(t.Key, keyW)) + "  " +
			descStyle.Render(theme.Truncate(t.Desc, descW)) + "  " +
			kindStyle(kind).Render(theme.Pad(theme.Truncate(status, statusW), statusW)) + "\n")
		if i == m.cat.cursor {
			detail := t.Repo
			if host := t.releaseHost(); host != "github" {
				detail = host + " · " + detail
			}
			if p := strings.Join(t.Platforms, ", "); p != "" {
				detail += "  ·  " + p
			}
			if t.Custom {
				detail += "  ·  本机添加"
			}
			b.WriteString("      " + styles.Dim.Render(theme.Truncate(detail, max(w-6, 10))) + "\n")
		}
	}

	if end < len(rows) {
		b.WriteString("  " + styles.Dim.Render(fmt.Sprintf("↓ 下面还有 %d 个", len(rows)-end)) + "\n")
	}
	b.WriteString("\n")
	b.WriteString(m.viewHints("↑↓", "选择", "enter", "安装/加入", "a", "按仓库添加", "r", "刷新目录", "esc", "返回"))
	return b.String()
}

// viewAdd 是新增工具页：填一个仓库就够，key 和说明可以留空。
// 资产名得是 <key>_<os>_<arch>，不然推不出该下哪个平台的包
func (m tuiModel) viewAdd() string {
	w := m.contentWidth()
	var b strings.Builder
	b.WriteString(m.withLogo(
		styles.Title.Render("新增工具")+styles.Dim.Render("  加一个让 hmck 管的"),
		styles.Dim.Render("填 GitHub 仓库就能装：owner/name 或直接贴链接"),
		styles.Dim.Render("key 不填就按仓库名取，资产名写成 <key>_<os>_<arch>"),
	))
	b.WriteString("\n")

	fields := []struct {
		label string
		hint  string
		value string
	}{
		{"仓库", "owner/name，也可以直接贴 GitHub 链接", m.add.repo},
		{"key", "不填就按仓库名取", m.add.key},
		{"说明", "列表里那句一句话说明，可以留空", m.add.desc},
	}
	for i, f := range fields {
		active := i == m.add.field
		labelStyle := styles.Dim
		if active {
			labelStyle = styles.Accent
		}
		label := "  " + labelStyle.Render(theme.Pad(f.label, 4)) + "  "
		v := theme.Truncate(f.value, max(w-12, 8))
		switch {
		case active:
			b.WriteString(label + styles.Accent.Render("> ") + v + styles.Accent.Render("█"))
		case v == "":
			b.WriteString(label + styles.Dim.Render(theme.Truncate(f.hint, max(w-12, 8))))
		default:
			b.WriteString(label + v)
		}
		b.WriteString("\n")
	}
	if m.add.err != "" {
		b.WriteString("\n  " + styles.Bad.Render(theme.Truncate(m.add.err, max(w-2, 8))) + "\n")
	}

	b.WriteString("\n")
	b.WriteString(m.viewHints("tab", "换一格", "enter", "添加并安装", "esc", "取消", "ctrl+c", "退出"))
	return b.String()
}

// viewSync 是多端同步页：上面是本机状态，下面是能做的事。
// 密码在页面里掩码输入，只用于这一次 push/pull，不落到任何文件里。
func (m tuiModel) viewSync() string {
	w := m.contentWidth()
	st, stErr := loadSyncState()
	items := m.syncItems()
	if m.sync.cursor >= len(items) {
		m.sync.cursor = max(len(items)-1, 0)
	}

	var b strings.Builder
	b.WriteString(m.withLogo(
		styles.Title.Render("多端同步")+styles.Dim.Render("  GitHub 私有仓库，加密存放"),
		styles.Dim.Render(theme.Truncate(m.syncHeadline(st, stErr), w-2)),
		styles.Dim.Render("密码只在输入时用一次，不上传、不落盘明文"),
	))
	b.WriteString("\n")

	repo := st.Repo
	if repo == "" {
		repo = "(还没连；首次推送时自动建成 <用户名>/" + syncRepoName + ")"
	}
	keyLine := "没记住（推送/拉取时在页面里输）"
	if st.Key != "" {
		keyLine = "已记住（本机 sync.json，0600）"
	}
	type field struct{ k, v string }
	fields := []field{
		{"GitHub", m.ghFieldLine()},
		{"仓库", repo},
		{"密钥", keyLine},
	}
	if names, err := syncNames(st); err == nil {
		fields = append(fields, field{"文件", fmt.Sprintf("%d 个（含各工具自带的配置）", len(names))})
	}
	if len(st.ExtraPaths) > 0 {
		fields = append(fields, field{"额外", strings.Join(st.ExtraPaths, "、")})
	}
	if st.LastPush != "" {
		fields = append(fields, field{"上次推送", st.LastPush})
	}
	if st.LastPull != "" {
		fields = append(fields, field{"上次拉取", st.LastPull})
	}
	keyW := 4
	for _, f := range fields {
		keyW = max(keyW, lipgloss.Width(f.k))
	}
	for _, f := range fields {
		b.WriteString("  " + styles.Dim.Render(theme.Pad(f.k, keyW)) + "  " +
			theme.Truncate(f.v, max(w-keyW-4, 10)) + "\n")
	}
	if st.LastError != "" {
		b.WriteString("  " + styles.Warn.Render(theme.Truncate("上次失败 "+st.LastError, max(w-2, 10))) + "\n")
	}
	b.WriteString("\n")

	labelW := 0
	for _, it := range items {
		labelW = max(labelW, lipgloss.Width(it.label))
	}
	labelW = min(labelW, max(w/2, 10))
	descW := max(w-2-2-labelW-2, 10)
	for i, it := range items {
		marker, labelStyle := "  ", styles.Dim
		if i == m.sync.cursor {
			marker, labelStyle = styles.Accent.Render("▌ "), styles.Accent
		}
		b.WriteString("  " + marker +
			labelStyle.Render(theme.Pad(theme.Truncate(it.label, labelW), labelW)) + "  " +
			styles.Dim.Render(theme.Truncate(it.desc, descW)) + "\n")
	}

	switch {
	case m.sync.busy:
		busy := "正在和 GitHub 同步…"
		if m.sync.ghRetry != "" {
			busy = "正在检查 GitHub CLI…"
		}
		b.WriteString("\n  " + m.spin.View() + "  " + styles.Dim.Render(busy) + "\n")
	case m.sync.mode == syncPass:
		b.WriteString("\n")
		prompt := "同步密码"
		if m.sync.confirm && m.sync.passFirst != "" {
			prompt = "再输入一次"
		}
		masked := strings.Repeat("•", len([]rune(m.sync.pass)))
		b.WriteString("  " + styles.Accent.Render(prompt) + "  " + styles.Accent.Render("> "))
		if masked == "" {
			b.WriteString(styles.Dim.Render("输入时不显示"))
		} else {
			b.WriteString(theme.Truncate(masked, max(w-18, 8)))
		}
		b.WriteString(styles.Accent.Render("█") + "\n")
	case m.sync.mode == syncPath:
		b.WriteString("\n  " + styles.Accent.Render("路径") + "  " + styles.Accent.Render("> ") +
			theme.Truncate(m.sync.path, max(w-14, 8)) + styles.Accent.Render("█") + "\n")
		b.WriteString("  " + styles.Dim.Render("如 ~/.config/foo/config.yaml、home:.ssh/config、config:foo/config.yaml") + "\n")
	case m.sync.msg != "":
		b.WriteString("\n  " + styles.Good.Render(theme.Truncate(m.sync.msg, max(w-2, 10))) + "\n")
	}
	if m.sync.err != "" {
		b.WriteString("\n  " + styles.Bad.Render(theme.Truncate(m.sync.err, max(w-2, 10))) + "\n")
	}

	b.WriteString("\n")
	switch m.sync.mode {
	case syncPass:
		b.WriteString(m.viewHints("enter", "确认", "esc", "取消", "ctrl+c", "退出"))
	case syncPath:
		b.WriteString(m.viewHints("enter", "加入同步", "esc", "取消", "ctrl+c", "退出"))
	default:
		hints := []string{"↑↓", "选择", "enter", "执行"}
		if m.sync.ghChecked && (!m.sync.ghFound || !m.sync.ghLogin) {
			hints = append(hints, "r", "重新检测")
		}
		hints = append(hints, "esc", "返回", "ctrl+c", "退出")
		b.WriteString(m.viewHints(hints...))
	}
	return b.String()
}

// ghFieldLine 是同步页上 GitHub CLI 那行：检测中 / 没装 / 没登录 / 已登录
func (m tuiModel) ghFieldLine() string {
	ver := strings.TrimSpace(m.sync.ghVer)
	switch {
	case !m.sync.ghChecked:
		return "检测中…"
	case !m.sync.ghFound:
		return "没装（在下面菜单里可以直接装）"
	case !m.sync.ghLogin:
		if ver != "" {
			return "gh " + ver + " · 没登录"
		}
		return "gh 没登录"
	}
	if ver != "" {
		return "gh " + ver + " · 已登录"
	}
	return "gh 已登录"
}

// syncHeadline 是同步页最上面那一句：连没连（推送/拉取时间在下面几行里）
func (m tuiModel) syncHeadline(st syncState, err error) string {
	if err != nil {
		return "读本机同步状态失败：" + err.Error()
	}
	if !syncEnabled(st) {
		return "这台机器还没连同步"
	}
	return "已连 " + st.Repo
}

// kindStyle 把信息行的 kind 翻成颜色：ok 绿、warn 黄、missing 红，其余用普通版本色
func kindStyle(kind string) lipgloss.Style {
	switch kind {
	case "ok":
		return styles.Good
	case "warn", "behind":
		return styles.Warn
	case "missing":
		return styles.Bad
	}
	return styleVer
}

// notesHeight 是日志正文能占几行：留出三行招牌、空行和底部提示
func (m tuiModel) notesHeight() int {
	return max(m.height-7, 3)
}

// notesMaxScroll 是最多能往下滚几行
func (m tuiModel) notesMaxScroll() int {
	return max(len(m.notesLines(m.contentWidth()))-m.notesHeight(), 0)
}

// notesLines 把版本列表排成一行行文字；滚动按这个算
func (m tuiModel) notesLines(w int) []string {
	if m.note.loading {
		return []string{"  " + m.spin.View() + "  " + styles.Dim.Render("正在读取更新日志…")}
	}
	if m.note.err != "" {
		return []string{
			"  " + styles.Warn.Render(theme.Truncate("没读到更新日志: "+m.note.err, w-2)),
			"  " + styles.Dim.Render("按 r 重试，或看 "+m.note.tool.Repo+" 的 Releases"),
		}
	}
	if len(m.note.entries) == 0 {
		return []string{"  " + styles.Dim.Render("这个工具还没写更新日志")}
	}

	var lines []string
	for i, e := range m.note.entries {
		head := "  " + styles.Accent.Render("v"+e.Version)
		if e.Date != "" {
			head += "  " + styles.Dim.Render(e.Date)
		}
		switch {
		case i == 0 && e.Version == m.note.current:
			head += "  " + styles.Good.Render("当前 · 最新")
		case i == 0:
			head += "  " + styles.Warn.Render("最新")
		case e.Version == m.note.current:
			head += "  " + styles.Good.Render("当前")
		}
		lines = append(lines, head)
		for _, s := range e.Sections {
			if s.Title != "" {
				lines = append(lines, "    "+styles.Dim.Render(s.Title))
			}
			for _, it := range s.Items {
				lines = append(lines, wrapBullet(it, w-8)...)
			}
		}
		lines = append(lines, "")
	}
	return lines
}

// wrapBullet 把一条说明折成多行，续行缩进对齐在破折号后面
func wrapBullet(item string, width int) []string {
	if width < 12 {
		width = 12
	}
	parts := strings.Split(lipgloss.NewStyle().Width(width).Render(item), "\n")
	out := make([]string, 0, len(parts))
	for i, p := range parts {
		if i == 0 {
			out = append(out, "      - "+p)
		} else {
			out = append(out, "        "+p)
		}
	}
	return out
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
