package main

import (
	"io"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/hongmengchen/tui/theme"
)

// 右侧面板：把工具自己的 TUI 放进一个伪终端里跑，输出喂给一台虚拟终端，
// 再把虚拟终端的屏幕接在列表右边。两种进法——enter 全屏（整块终端交给它，
// 跟以前一样），p 就在右边开半屏，跟列表并排看。
//
// 半屏只负责「看」和「输入」：按键按字节原样转给它。伪终端只有 Unix 上有，
// Windows 下 paneSupported 是 false，提示栏里就不摆这个键。

// ptyChild 是一个带终端的子进程：读写的是 pty 那一头，不是普通管道，
// 这样它才肯相信自己在真终端里、才会画全屏界面。
type ptyChild interface {
	io.Reader
	io.Writer
	Resize(cols, rows int) error
	Kill() error
	Wait() error
}

type pane struct {
	tool   tool
	term   *vt.SafeEmulator // 子进程画出来的那块屏幕
	child  ptyChild
	ch     chan tea.Msg // 输出、退出通知，一次只排一个
	done   chan struct{}
	width  int
	height int

	focused bool
	exited  bool
	err     error
}

type paneOutputMsg struct{ p *pane }
type paneExitMsg struct {
	p   *pane
	err error
}
type paneDeadMsg struct{}

// startPane 起一份工具，屏幕给它 width×height。失败时什么也没留下。
func startPane(t tool, path string, width, height int) (*pane, error) {
	child, err := openChild(path, width, height)
	if err != nil {
		return nil, err
	}
	p := &pane{
		tool:   t,
		term:   vt.NewSafeEmulator(width, height),
		child:  child,
		ch:     make(chan tea.Msg, 1),
		done:   make(chan struct{}),
		width:  width,
		height: height,
	}
	go p.pump()
	return p, nil
}

// pump 一直读到子进程关掉终端为止。排消息用阻塞发送：界面跟不上时子进程
// 就慢一点，不丢输出；面板关掉后 done 一关，这里立刻退出。
func (p *pane) pump() {
	buf := make([]byte, 32*1024)
	for {
		n, err := p.child.Read(buf)
		if n > 0 {
			p.term.Write(buf[:n])
			if !p.send(paneOutputMsg{p: p}) {
				return
			}
		}
		if err != nil {
			waitErr := p.child.Wait()
			p.send(paneExitMsg{p: p, err: waitErr})
			return
		}
	}
}

func (p *pane) send(msg tea.Msg) bool {
	select {
	case p.ch <- msg:
		return true
	case <-p.done:
		return false
	}
}

// wait 等面板的下一条消息：输出到了就重画，子进程退了就记下来
func (p *pane) wait() tea.Cmd {
	return func() tea.Msg {
		select {
		case msg := <-p.ch:
			return msg
		case <-p.done:
			return paneDeadMsg{}
		}
	}
}

func (p *pane) write(b []byte) {
	if p.exited || len(b) == 0 {
		return
	}
	p.child.Write(b)
}

func (p *pane) resize(width, height int) {
	if width <= 0 || height <= 0 || (width == p.width && height == p.height) {
		return
	}
	p.width, p.height = width, height
	p.term.Resize(width, height)
	p.child.Resize(width, height)
}

// close 杀掉子进程并让等待中的命令醒来；重复调用没关系
func (p *pane) close() {
	select {
	case <-p.done:
		return
	default:
	}
	close(p.done)
	p.child.Kill()
}

// lines 是面板要画的每一行。逐格取单元格再拼回带样式的字符串：
// 这样光标能画成一个反色的格子，输入密码时也看得见自己在哪。
func (p *pane) lines() []string {
	w, h := p.width, p.height
	if w <= 0 || h <= 0 {
		return nil
	}
	cur := p.term.CursorPosition()
	cx, cy := cur.X, cur.Y
	showCursor := p.focused && !p.exited && !p.term.CursorHidden()
	out := make([]string, h)
	for y := 0; y < h; y++ {
		line := uv.NewLine(w)
		for x := 0; x < w; x++ {
			if c := p.term.CellAt(x, y); c != nil {
				line[x] = *c.Clone()
			}
		}
		if showCursor && y == cy && cx >= 0 && cx < w {
			line[cx].Style.Attrs ^= uv.AttrReverse
		}
		out[y] = line.Render()
	}
	return out
}

// paneKeyBytes 把 Bubble Tea 的按键还原成终端里那串字节。
// 只求常见的那几个键对得上：方向键、回车、Tab、Esc、退格、Ctrl 组合和普通字符。
func paneKeyBytes(k tea.KeyMsg) []byte {
	switch t := k.Type; {
	case t == tea.KeyRunes:
		b := []byte(string(k.Runes))
		if k.Alt {
			return append([]byte{0x1b}, b...)
		}
		return b
	case t == tea.KeySpace:
		return []byte(" ")
	case t >= tea.KeyCtrlAt && t <= tea.KeyCtrlZ:
		// 回车(13)、Tab(9)、Esc 不在这段里，见下面
		return []byte{byte(t)}
	case t == tea.KeyBackspace:
		return []byte{0x7f}
	}
	switch k.Type {
	case tea.KeyEsc:
		return []byte{0x1b}
	case tea.KeyUp:
		return []byte("\x1b[A")
	case tea.KeyDown:
		return []byte("\x1b[B")
	case tea.KeyRight:
		return []byte("\x1b[C")
	case tea.KeyLeft:
		return []byte("\x1b[D")
	case tea.KeyHome:
		return []byte("\x1b[H")
	case tea.KeyEnd:
		return []byte("\x1b[F")
	case tea.KeyPgUp:
		return []byte("\x1b[5~")
	case tea.KeyPgDown:
		return []byte("\x1b[6~")
	case tea.KeyInsert:
		return []byte("\x1b[2~")
	case tea.KeyDelete:
		return []byte("\x1b[3~")
	case tea.KeyShiftTab:
		return []byte("\x1b[Z")
	}
	return nil
}

// paneLayout 是左右两栏各占多少：面板取右半边，左边列表至少留 36 列，
// 底部提示和它们上面那条空行优先占位。
type paneLayout struct {
	leftW int
	paneW int
	rows  int // 两栏共同的高度，含面板标题行
	hints string
}

// paneFits 只看终端够不够宽够不够高，不排内容——提示栏要用它，
// 不能反过来让排版去问提示栏（会绕成环）。
func (m tuiModel) paneFits() bool {
	return paneSupported && m.width >= 80 && m.height >= 16
}

func (m tuiModel) paneLayout() (paneLayout, bool) {
	if !m.paneFits() {
		return paneLayout{}, false
	}
	paneW := m.width / 2
	if paneW > 84 {
		paneW = 84
	}
	leftW := m.width - paneW - 3 // 中间 " │ " 三列
	if leftW < 36 || paneW < 36 {
		return paneLayout{}, false
	}
	hints := m.listHints()
	hintLines := strings.Count(hints, "\n") + 1
	rows := m.height - 2 - hintLines - 1
	if rows < 8 {
		return paneLayout{}, false
	}
	return paneLayout{leftW: leftW, paneW: paneW, rows: rows, hints: hints}, true
}

// paneColumn 是右侧那一栏：第一行写工具名和状态，下面就是子进程的屏幕
func (m tuiModel) paneColumn(l paneLayout) []string {
	p := m.pane
	title := styles.Key.Render(p.tool.Key)
	switch {
	case p.exited:
		title += styles.Warn.Render("  已退出")
		if p.err != nil {
			title += styles.Dim.Render(" · " + oneLine(p.err.Error()))
		}
	case p.focused:
		title += styles.Good.Render("  运行中 · 按键直接发过去")
	default:
		title += styles.Dim.Render("  运行中")
	}
	out := []string{title}
	screen := p.lines()
	for i := 0; i < l.rows-1; i++ {
		if i < len(screen) {
			out = append(out, screen[i])
		} else {
			out = append(out, "")
		}
	}
	return out
}

// viewSplit 把列表（左）和面板（右）并排拼起来，底部提示照旧横跨整屏
func (m tuiModel) viewSplit(l paneLayout) string {
	left := m.listBodyWithin(l.leftW, l.rows)
	right := m.paneColumn(l)
	bar := styles.Dim.Render(" │ ")
	var b strings.Builder
	for i := 0; i < l.rows; i++ {
		ls, rs := "", ""
		if i < len(left) {
			ls = left[i]
		}
		if i < len(right) {
			rs = right[i]
		}
		b.WriteString(fitWidth(ls, l.leftW))
		b.WriteString(bar)
		b.WriteString(fitWidth(rs, l.paneW))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(l.hints)
	return b.String()
}

// oneLine 把出错信息压成一行，标题行放得下
func oneLine(s string) string {
	s = strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
	return theme.Truncate(s, 36)
}

// fitWidth 把一行补/裁到正好 w 列，ANSI 样式不占宽度
func fitWidth(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if s != "" {
		if lipgloss.Width(s) > w {
			s = ansi.Truncate(s, w, "…")
		}
	}
	if n := lipgloss.Width(s); n < w {
		s += strings.Repeat(" ", w-n)
	}
	return s
}

// listBodyWithin 在 width×rows 的框里画列表本体（不含底部提示），
// 放不下时跟整屏一样先退紧凑版、再退只画光标附近
func (m tuiModel) listBodyWithin(width, rows int) []string {
	cp := m
	cp.width = width + 4 // contentWidth 会减掉 4
	body := cp.viewListBody(false, 0)
	if strings.Count(body, "\n") > rows {
		body = cp.viewListBody(true, 0)
	}
	if strings.Count(body, "\n") > rows {
		body = cp.viewListBody(true, max(rows-1, 1))
	}
	lines := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
	if len(lines) > rows {
		lines = lines[:rows]
	}
	return lines
}

// openPane 在右边开一份工具的 TUI。先关掉原来那份，键盘直接交给新的
func (m tuiModel) openPane(st toolState) (tea.Model, tea.Cmd) {
	bin := toolCopy(st)
	if bin == "" {
		return m, nil
	}
	l, ok := m.paneLayout()
	if !ok {
		return m, nil
	}
	m.closePane()
	p, err := startPane(st.Tool, bin, l.paneW, l.rows-1)
	if err != nil {
		m.state = result
		m.resultLines = []resultLine{
			{text: st.Tool.Key + " 没能在这里打开: " + err.Error(), ok: false},
			{text: "换 enter 全屏打开试试", ok: false},
		}
		return m, nil
	}
	p.focused = true
	m.pane = p
	return m, p.wait()
}

// closePane 收起右侧面板，子进程一起结束
func (m *tuiModel) closePane() {
	if m.pane == nil {
		return
	}
	m.pane.close()
	m.pane = nil
}

// syncPaneSize 在终端尺寸变了以后把面板和子进程一起调到新大小；
// 新尺寸摆不下两栏就干脆收起来，退回整屏列表
func (m *tuiModel) syncPaneSize() {
	if m.pane == nil {
		return
	}
	l, ok := m.paneLayout()
	if !ok {
		m.closePane()
		return
	}
	m.pane.resize(l.paneW, l.rows-1)
}
