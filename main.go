package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/hongmengchen/tui/theme"
)

var version = "0.8.0"

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	// 旧版装在 %LOCALAPPDATA%\<tool> 的，先把二进制搬到统一目录，配置不动
	migrateInstallLocations()

	if len(args) == 0 {
		// 终端里给 TUI；管道里给可 grep 的列表
		if isTTY(os.Stdin) && isTTY(os.Stdout) {
			return runTUI()
		}
		return runList(nil)
	}

	cmd, rest := args[0], args[1:]
	switch cmd {
	case "list", "ls":
		return runList(rest)
	case "run":
		return runRun(rest)
	case "install":
		return runInstall(rest)
	case "upgrade", "update":
		return runUpgrade(rest)
	case "add":
		return runAdd(rest)
	case "rm", "remove":
		return runRemove(rest)
	case "skill":
		return runSkill(rest)
	case "tui":
		return runTUI()
	case "version", "-v", "--version":
		fmt.Println("hmck " + version)
		return 0
	case "help", "-h", "--help":
		usage(os.Stdout)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "未知命令: %s\n\n", cmd)
		usage(os.Stderr)
		return 2
	}
}

func usage(w io.Writer) {
	fmt.Fprintf(w, `hmck %s — 管理我自己的命令行小工具

用法:
  hmck                     打开管理界面（TUI）
  hmck list [--json]       列出工具、已装版本、最新版本
  hmck run <tool> [参数…]  启动工具，参数原样传给它
  hmck install <tool>      安装最新版（--force 重装）
  hmck upgrade [<tool>]    升级；不带参数就升级所有可升级的工具（包括 hmck 自己）
  hmck add <owner/repo>    把一个新工具加进本机配置，之后就能 install/upgrade
  hmck rm <tool>           删掉自定义工具（内置的删不了）
  hmck skill               给 hmck 和已安装的工具装 agent skill
  hmck version
  hmck help

管理的工具:
`, version)
	tools, _ := allTools()
	for _, t := range tools {
		mark := ""
		if t.Custom {
			mark = "（自定义）"
		}
		fmt.Fprintf(w, "  %-12s %s%s\n", t.Key, t.Desc, mark)
	}
	fmt.Fprintf(w, `
例:
  hmck                    # 选工具，回车直接进它的 TUI（hmck 自己按 enter 是升级）
  hmck run npm-token      # 命令行直接启动
  hmck upgrade            # 把 hmck 和所有工具升到最新
  hmck upgrade hmck       # 只升级 hmck 自己
`)
}

// ---------- list ----------

type listItem struct {
	Key        string `json:"key"`
	Name       string `json:"name"`
	Installed  string `json:"installed,omitempty"`
	Latest     string `json:"latest,omitempty"`
	Path       string `json:"path,omitempty"`
	Status     string `json:"status"`
	Upgradable bool   `json:"upgradable"`
	UpdatedAt  string `json:"updated_at,omitempty"`
	Custom     bool   `json:"custom,omitempty"`
	Error      string `json:"error,omitempty"`
}

func runList(args []string) int {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var asJSON bool
	fs.BoolVar(&asJSON, "json", false, "输出 JSON")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, "参数不对: "+err.Error())
		return 2
	}

	states, cfgErr := inspectAll()
	if cfgErr != nil {
		fmt.Fprintf(os.Stderr, "自定义工具配置有问题: %v\n", cfgErr)
	}
	if asJSON {
		items := make([]listItem, 0, len(states))
		for _, st := range states {
			items = append(items, toListItem(st))
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(items); err != nil {
			return 2
		}
		return 0
	}

	color := isTTY(os.Stdout)
	type row struct{ name, installed, latest, status string }
	rows := make([]row, 0, len(states))
	for _, st := range states {
		installed, latest := st.Installed, st.Latest
		if installed == "" {
			installed = "—"
		}
		if latest == "" {
			latest = "—"
		}
		label, _ := st.Status()
		rows = append(rows, row{st.Tool.Key, installed, latest, label})
	}

	// 中文按显示宽度补空格，tabwriter 只数字符会错位
	w1, w2, w3 := lipgloss.Width("NAME"), lipgloss.Width("已安装"), lipgloss.Width("最新")
	for _, r := range rows {
		w1 = max(w1, lipgloss.Width(r.name))
		w2 = max(w2, lipgloss.Width(r.installed))
		w3 = max(w3, lipgloss.Width(r.latest))
	}
	fmt.Printf("%s  %s  %s  %s\n",
		dim(theme.Pad("NAME", w1), color), dim(theme.Pad("已安装", w2), color),
		dim(theme.Pad("最新", w3), color), dim("状态", color))
	for i, r := range rows {
		_, kind := states[i].Status()
		fmt.Printf("%s  %s  %s  %s\n",
			theme.Pad(r.name, w1), theme.Pad(r.installed, w2), theme.Pad(r.latest, w3),
			paint(r.status, kind, color))
	}

	for _, st := range states {
		if st.CheckErr != "" {
			fmt.Fprintf(os.Stderr, "%s: %s\n", st.Tool.Key, st.CheckErr)
		}
	}
	return 0
}

func toListItem(st toolState) listItem {
	label, _ := st.Status()
	updated := ""
	if !st.UpdatedAt.IsZero() {
		updated = st.UpdatedAt.Format(time.RFC3339)
	}
	return listItem{
		Key: st.Tool.Key, Name: st.Tool.Name,
		Installed: st.Installed, Latest: st.Latest, Path: shortenHome(st.Path),
		Status: label, Upgradable: st.Behind(), UpdatedAt: updated,
		Custom: st.Tool.Custom, Error: st.CheckErr,
	}
}

// inspectAll 一次查完本地和远端；CLI 的 list 用这个
func inspectAll() ([]toolState, error) {
	states, err := inspectLocalAll(nil)
	if err != nil {
		return states, err
	}
	latestAll(states)
	return states, nil
}

// inspectLocalAll 只扫本机，不联网；tools 为 nil 时重新读配置
func inspectLocalAll(tools []tool) ([]toolState, error) {
	var err error
	if tools == nil {
		tools, err = allTools()
	}
	states := make([]toolState, len(tools))
	done := make(chan int, len(tools))
	for i, t := range tools {
		go func(i int, t tool) {
			states[i] = inspectLocal(t)
			done <- i
		}(i, t)
	}
	for range tools {
		<-done
	}
	return states, err
}

// latestAll 并发查所有工具的最新版，就地写回 states
func latestAll(states []toolState) {
	done := make(chan int, len(states))
	for i := range states {
		go func(i int) {
			states[i] = inspectLatest(states[i])
			done <- i
		}(i)
	}
	for range states {
		<-done
	}
}

// ---------- run ----------

func runRun(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "要启动哪个工具？例如: hmck run npm-token")
		return 2
	}
	if strings.EqualFold(strings.TrimSpace(args[0]), "hmck") {
		fmt.Fprintln(os.Stderr, "hmck 就是它自己，直接运行 hmck 就行")
		return 2
	}
	t, ok := toolByKey(args[0])
	if !ok {
		fmt.Fprintf(os.Stderr, "不认识这个工具: %s\n可用: %s\n", args[0], keysText())
		return 2
	}
	path, found := findTool(t)
	if !found {
		fmt.Fprintf(os.Stderr, "%s 还没装。先运行:\n  hmck install %s\n", t.Key, t.Key)
		return 1
	}

	cmd := exec.Command(path, args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode()
		}
		fmt.Fprintf(os.Stderr, "启动 %s 失败: %v\n", t.Key, err)
		return 1
	}
	return 0
}

// ---------- install / upgrade ----------

func runInstall(args []string) int {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var force bool
	fs.BoolVar(&force, "force", false, "已是最新也重新下载")
	if err := fs.Parse(reorderFlags(args)); err != nil {
		fmt.Fprintln(os.Stderr, "参数不对: "+err.Error())
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintf(os.Stderr, "用法: hmck install <tool>\n可用: %s\n", keysText())
		return 2
	}
	t, ok := toolByKey(fs.Arg(0))
	if !ok {
		fmt.Fprintf(os.Stderr, "不认识这个工具: %s\n可用: %s\n", fs.Arg(0), keysText())
		return 2
	}

	st := inspect(t)
	if st.Installed != "" && st.Latest != "" && !isNewer(st.Latest, st.Installed) && !force {
		fmt.Printf("%s %s 已是最新\n", t.Key, st.Installed)
		return 0
	}
	return doInstall(st, force)
}

func runUpgrade(args []string) int {
	fs := flag.NewFlagSet("upgrade", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var force bool
	fs.BoolVar(&force, "force", false, "已是最新也重新下载")
	if err := fs.Parse(reorderFlags(args)); err != nil {
		fmt.Fprintln(os.Stderr, "参数不对: "+err.Error())
		return 2
	}

	var targets []tool
	if fs.NArg() > 0 {
		for _, key := range fs.Args() {
			t, ok := toolByKey(key)
			if !ok {
				fmt.Fprintf(os.Stderr, "不认识这个工具: %s\n可用: %s\n", key, keysText())
				return 2
			}
			targets = append(targets, t)
		}
	} else {
		var err error
		if targets, err = allTools(); err != nil {
			fmt.Fprintf(os.Stderr, "自定义工具配置有问题: %v\n", err)
			targets = registry
		}
	}

	states, cfgErr := inspectAll()
	if cfgErr != nil {
		fmt.Fprintf(os.Stderr, "自定义工具配置有问题: %v\n", cfgErr)
	}
	byKey := map[string]toolState{}
	for _, st := range states {
		byKey[st.Tool.Key] = st
	}

	failed := false
	for _, t := range targets {
		st := byKey[t.Key]
		switch {
		case st.Missing():
			fmt.Printf("%s: 未安装，跳过（要装请用 hmck install %s）\n", t.Key, t.Key)
		case st.Latest == "":
			fmt.Fprintf(os.Stderr, "%s: 查不到最新版: %s\n", t.Key, st.CheckErr)
			failed = true
		case !st.Behind() && !force:
			fmt.Printf("%s: %s 已是最新\n", t.Key, st.Installed)
		default:
			if doInstall(st, force) != 0 {
				failed = true
			}
		}
	}
	if failed {
		return 1
	}
	return 0
}

func doInstall(st toolState, force bool) int {
	t := st.Tool
	from := st.Installed
	if from == "" {
		from = "未安装"
	}
	latest := st.Latest
	if latest == "" {
		latest = "最新版"
	}
	fmt.Printf("%s: %s → %s，下载中…\n", t.Key, from, latest)

	newVer, err := installTool(st)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: 升级失败: %v\n", t.Key, err)
		return 1
	}
	path, _ := findTool(t)
	if newVer == "" {
		fmt.Printf("%s: 已更新（版本号没读出来）\n", t.Key)
	} else {
		fmt.Printf("%s: %s 已就绪  %s\n", t.Key, newVer, shortenHome(path))
	}
	return 0
}

func keysText() string {
	tools, _ := allTools()
	var keys []string
	for _, t := range tools {
		keys = append(keys, t.Key)
	}
	return strings.Join(keys, ", ")
}

// ---------- 自定义工具 ----------

func runAdd(args []string) int {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var key, desc, binary, platforms string
	var checksums, skill, force bool
	fs.StringVar(&key, "key", "", "工具 key（也用作发布资产前缀，默认取仓库名）")
	fs.StringVar(&desc, "desc", "", "一句话说明")
	fs.StringVar(&binary, "binary", "", "可执行文件名（默认同 key）")
	fs.StringVar(&platforms, "platforms", "", "支持的平台，逗号分隔，如 darwin/arm64,linux/amd64")
	fs.BoolVar(&checksums, "checksums", false, "发布里有 checksums.txt（默认自动探测）")
	fs.BoolVar(&skill, "skill", false, "该工具支持 `<tool> skill --quiet`")
	fs.BoolVar(&force, "force", false, "覆盖同名的自定义工具")
	valueFlags := map[string]bool{"key": true, "desc": true, "binary": true, "platforms": true}
	if err := fs.Parse(reorderArgs(args, valueFlags)); err != nil {
		fmt.Fprintln(os.Stderr, "参数不对: "+err.Error())
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "用法: hmck add <owner/repo> [--key NAME] [--desc 说明] [--platforms list] [--checksums] [--skill]")
		return 2
	}
	repo, err := parseRepo(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return 2
	}
	if key == "" {
		key = defaultKeyForRepo(repo)
	}

	t := tool{Key: key, Name: key, Desc: desc, Repo: repo, Binary: binary,
		Checksums: checksums, Skill: skill}

	if platforms != "" {
		t.Platforms = splitList(platforms)
	} else {
		info, err := fetchReleaseInfo(repo)
		if err != nil {
			fmt.Fprintf(os.Stderr, "提示: 没读到 %s 的 release（%v），先按常见平台记录\n", repo, err)
			t.Platforms = append([]string(nil), defaultPlatforms...)
		} else {
			t.Platforms = inferPlatforms(key, info.Assets)
			if len(t.Platforms) == 0 {
				fmt.Fprintf(os.Stderr, "提示: release 里没有 %s_<os>_<arch> 资产，先按常见平台记录\n", key)
				t.Platforms = append([]string(nil), defaultPlatforms...)
			}
			if hasChecksums(info.Assets) {
				t.Checksums = true
			}
		}
	}

	path, err := addCustomTool(t, force)
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return 2
	}

	fmt.Printf("已添加 %s → %s\n", t.Key, t.Repo)
	if t.Desc != "" {
		fmt.Printf("说明    %s\n", t.Desc)
	}
	fmt.Printf("平台    %s\n", strings.Join(t.Platforms, ", "))
	if t.Checksums {
		fmt.Println("校验    发布里有 checksums.txt，升级时校验 SHA-256")
	}
	if t.Skill {
		fmt.Println("skill   装完/升级后会跑 " + t.Key + " skill --quiet")
	}
	fmt.Printf("配置    %s\n", shortenHome(path))
	fmt.Printf("\n接下来: hmck install %s\n", t.Key)
	return 0
}

func runRemove(args []string) int {
	fs := flag.NewFlagSet("rm", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err := fs.Parse(reorderFlags(args)); err != nil {
		fmt.Fprintln(os.Stderr, "参数不对: "+err.Error())
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "用法: hmck rm <tool>")
		return 2
	}
	path, err := removeCustomTool(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return 2
	}
	fmt.Printf("已从配置里删掉 %s\n配置    %s\n", fs.Arg(0), shortenHome(path))
	fmt.Println("（已经装好的二进制不会自动删除；要删文件自己去对应目录）")
	return 0
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ---------- 颜色 ----------

func dim(s string, color bool) string {
	if !color {
		return s
	}
	return "\x1b[2m" + s + "\x1b[0m"
}

// reorderFlags 让 `hmck upgrade npm-token --force` 和 `--force npm-token` 都能用
func reorderFlags(args []string) []string {
	return reorderArgs(args, nil)
}

// reorderArgs 把选项挪到位置参数前面；valueFlags 里的选项会连值一起挪，
// 这样 `hmck add owner/repo --desc 一句话` 也能正常解析。
func reorderArgs(args []string, valueFlags map[string]bool) []string {
	var flags, rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") && a != "-" {
			flags = append(flags, a)
			name := strings.TrimLeft(a, "-")
			if !strings.Contains(name, "=") && valueFlags[name] && i+1 < len(args) {
				flags = append(flags, args[i+1])
				i++
			}
			continue
		}
		rest = append(rest, a)
	}
	return append(flags, rest...)
}

func paint(s, kind string, color bool) string {
	if !color {
		return s
	}
	code := map[string]string{"ok": "32", "behind": "33", "missing": "31", "warn": "33"}[kind]
	if code == "" {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}
