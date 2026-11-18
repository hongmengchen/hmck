package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var version = "0.1.0"

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
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
  hmck upgrade [<tool>]    升级；不带参数就升级所有可升级的工具
  hmck version
  hmck help

管理的工具:
`, version)
	for _, t := range registry {
		fmt.Fprintf(w, "  %-12s %s\n", t.Key, t.Desc)
	}
	fmt.Fprintf(w, `
例:
  hmck                    # 选 kube-token / npm-token，回车直接进它的 TUI
  hmck run npm-token      # 命令行直接启动
  hmck upgrade            # 把所有工具升到最新
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

	states := inspectAll()
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
		dim(pad("NAME", w1), color), dim(pad("已安装", w2), color),
		dim(pad("最新", w3), color), dim("状态", color))
	for i, r := range rows {
		_, kind := states[i].Status()
		fmt.Printf("%s  %s  %s  %s\n",
			pad(r.name, w1), pad(r.installed, w2), pad(r.latest, w3),
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
	return listItem{
		Key: st.Tool.Key, Name: st.Tool.Name,
		Installed: st.Installed, Latest: st.Latest, Path: shortenHome(st.Path),
		Status: label, Upgradable: st.Behind(), Error: st.CheckErr,
	}
}

func inspectAll() []toolState {
	states := make([]toolState, len(registry))
	done := make(chan int, len(registry))
	for i, t := range registry {
		go func(i int, t tool) {
			states[i] = inspect(t)
			done <- i
		}(i, t)
	}
	for range registry {
		<-done
	}
	return states
}

// ---------- run ----------

func runRun(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "要启动哪个工具？例如: hmck run npm-token")
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
		targets = registry
	}

	states := inspectAll()
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
	var keys []string
	for _, t := range registry {
		keys = append(keys, t.Key)
	}
	return strings.Join(keys, ", ")
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
	var flags, rest []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") && a != "-" {
			flags = append(flags, a)
		} else {
			rest = append(rest, a)
		}
	}
	return append(flags, rest...)
}

func pad(s string, w int) string {
	if n := lipgloss.Width(s); n < w {
		return s + strings.Repeat(" ", w-n)
	}
	return s
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
