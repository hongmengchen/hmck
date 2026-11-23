package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/hongmengchen/tui/theme"
)

// catalogEmbedded 是编进程序里的目录副本：线上读不到时拿它顶上，
// 用户至少还能看到已知的那几个工具
//
//go:embed docs/catalog.json
var catalogEmbedded []byte

// catalogURL 是线上维护的工具目录。目录里加一个工具只用提交这份 JSON，
// 不用发新版 hmck；HMCK_CATALOG_URL 可以指向自己的镜像（内网或测试）
var catalogURL = "https://raw.githubusercontent.com/hongmengchen/hmck/main/docs/catalog.json"

var catalogClient = &http.Client{Timeout: 10 * time.Second}

// 目录再长也不至于超过这个数；超了只取前几条，别让一页塞不下或者被喂爆内存
const maxCatalogTools = 200

type catalogDoc struct {
	Version int    `json:"version"`
	Tools   []tool `json:"tools"`
}

// catalogResult 是一次目录加载的结果。source 说明这份是从网上拿的还是程序自带的；
// 网上那份没读到时 err 有原因，但自带的照常能用
type catalogResult struct {
	tools   []tool
	source  string // online / bundled
	skipped int
	err     string
}

// loadCatalog 先读线上目录，读不到就退回编进程序里的同一份
func loadCatalog() catalogResult {
	url := strings.TrimSpace(os.Getenv("HMCK_CATALOG_URL"))
	if url == "" {
		url = catalogURL
	}
	tools, skipped, err := fetchCatalog(url)
	if err == nil {
		return catalogResult{tools: tools, source: "online", skipped: skipped}
	}
	tools, bSkipped, berr := parseCatalog(catalogEmbedded)
	if berr != nil {
		// 自带的也坏了，那只能是程序自己的问题
		return catalogResult{err: err.Error()}
	}
	return catalogResult{tools: tools, source: "bundled", skipped: bSkipped, err: err.Error()}
}

func fetchCatalog(url string) ([]tool, int, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", "hmck/"+version)
	resp, err := catalogClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("目录返回 %s", resp.Status)
	}
	// 目录就几十 KB，限一下大小，别被一个坏链接拖住
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, 0, err
	}
	return parseCatalog(data)
}

// parseCatalog 校验目录条目：key 不合法、平台写得不对、跟前面重复的整条跳过；
// 内置工具的 key 一律用程序里的定义，目录改不了它从哪里装、怎么校验
func parseCatalog(data []byte) ([]tool, int, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, 0, errors.New("目录是空的")
	}
	var doc catalogDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, 0, fmt.Errorf("目录格式不对: %w", err)
	}
	if doc.Version != 1 {
		return nil, 0, fmt.Errorf("不认识的目录版本 %d", doc.Version)
	}
	if len(doc.Tools) == 0 {
		return nil, 0, errors.New("目录里没有工具")
	}

	seen := map[string]bool{}
	out := make([]tool, 0, len(doc.Tools))
	skipped := 0
	for _, t := range doc.Tools {
		if err := normalizeCustom(&t); err != nil {
			skipped++
			continue
		}
		if !validCatalogHost(t.Host) {
			skipped++
			continue
		}
		key := strings.ToLower(t.Key)
		if seen[key] {
			skipped++
			continue
		}
		seen[key] = true
		if b, ok := builtinTool(t.Key); ok {
			out = append(out, b)
			continue
		}
		t.Custom = false // 来自目录，不是本机配置
		out = append(out, t)
	}
	if len(out) == 0 {
		return nil, skipped, errors.New("目录里的条目都读不懂")
	}
	if len(out) > maxCatalogTools {
		out = out[:maxCatalogTools]
	}
	return out, skipped, nil
}

func validCatalogHost(h string) bool {
	switch strings.ToLower(strings.TrimSpace(h)) {
	case "", "github", "gitlab", "kubernetes":
		return true
	}
	return false
}

func builtinTool(key string) (tool, bool) {
	for _, t := range registry {
		if strings.EqualFold(t.Key, key) {
			return t, true
		}
	}
	return tool{}, false
}

// platformSupported 看这个工具有没有当前这台机器的包，没有就别让人白等一次下载
func platformSupported(t tool) bool {
	want := runtime.GOOS + "/" + runtime.GOARCH
	for _, p := range t.Platforms {
		if p == want {
			return true
		}
	}
	return false
}

// ---------- 命令行：hmck catalog ----------

type catalogItem struct {
	Key       string   `json:"key"`
	Name      string   `json:"name,omitempty"`
	Desc      string   `json:"desc,omitempty"`
	Repo      string   `json:"repo"`
	Host      string   `json:"host,omitempty"`
	Platforms []string `json:"platforms,omitempty"`
	Managed   bool     `json:"managed"`
	Installed string   `json:"installed,omitempty"`
	Foreign   bool     `json:"foreign,omitempty"`
	Available bool     `json:"available"`
	Source    string   `json:"source"`
}

func runCatalog(args []string) int {
	fs := flag.NewFlagSet("catalog", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var asJSON bool
	fs.BoolVar(&asJSON, "json", false, "输出 JSON")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, "参数不对: "+err.Error())
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "用法: hmck catalog [--json]")
		return 2
	}

	res := loadCatalog()
	if res.err != "" {
		fmt.Fprintf(os.Stderr, "在线目录没读到: %s（用随程序内置的一份）\n", res.err)
	}
	if len(res.tools) == 0 {
		fmt.Fprintln(os.Stderr, "工具目录读不到: "+res.err)
		return 1
	}

	// 只看本机，不联网：目录本身是来挑工具的，装没装、在哪一份够用了
	states, _ := inspectLocalAll(nil)
	byKey := make(map[string]toolState, len(states))
	for _, st := range states {
		byKey[strings.ToLower(st.Tool.Key)] = st
	}

	items := make([]catalogItem, 0, len(res.tools))
	for _, t := range res.tools {
		item := catalogItem{
			Key: t.Key, Name: t.Name, Desc: t.Desc, Repo: t.Repo, Host: t.Host,
			Platforms: t.Platforms, Available: platformSupported(t), Source: res.source,
		}
		if st, ok := byKey[strings.ToLower(t.Key)]; ok {
			item.Managed = true
			item.Installed = st.Installed
			item.Foreign = st.Installed == "" && len(st.Foreign) > 0
			if item.Foreign && st.ForeignVer != "" {
				item.Installed = st.ForeignVer
			}
		}
		items = append(items, item)
	}

	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(items); err != nil {
			return 2
		}
		return 0
	}

	color := isTTY(os.Stdout)
	source := "在线"
	if res.source == "bundled" {
		source = "内置副本"
	}
	fmt.Printf("工具目录 · %s · %d 个工具\n\n", source, len(items))
	wKey, wStatus := lipgloss.Width("NAME"), lipgloss.Width("状态")
	for _, it := range items {
		wKey = max(wKey, lipgloss.Width(it.Key))
		wStatus = max(wStatus, lipgloss.Width(catalogItemStatus(it)))
	}
	fmt.Printf("  %s  %s  %s  %s\n",
		dim(theme.Pad("NAME", wKey), color), dim(theme.Pad("状态", wStatus), color),
		dim("说明", color), dim("仓库", color))
	for _, it := range items {
		status, kind := catalogItemStatusKind(it)
		fmt.Printf("  %s  %s  %s  %s\n",
			theme.Pad(it.Key, wKey), paint(theme.Pad(status, wStatus), kind, color),
			theme.Truncate(it.Desc, 40), dim(it.Repo, color))
	}
	if res.skipped > 0 {
		fmt.Fprintf(os.Stderr, "目录里有 %d 条读不懂，已跳过\n", res.skipped)
	}
	fmt.Println()
	fmt.Println("在 TUI 里按 n 可以挑着装；命令行先 hmck add <仓库> 再 hmck install <key>")
	return 0
}

func catalogItemStatus(it catalogItem) string {
	s, _ := catalogItemStatusKind(it)
	return s
}

func catalogItemStatusKind(it catalogItem) (string, string) {
	switch {
	case !it.Available:
		return "本平台没有", "missing"
	case !it.Managed:
		return "未添加", "missing"
	case it.Installed != "" && it.Foreign:
		return "PATH 上 " + it.Installed, "warn"
	case it.Installed != "":
		return it.Installed, "ok"
	default:
		return "未安装", "missing"
	}
}
