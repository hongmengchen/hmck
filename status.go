package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// ---------- 登录状态（glab） ----------

// glabLogin 是 glab 对一家 GitLab 主机的登录情况。
// 只记「登录成了谁、token 从哪来」，不记 token 内容——
// `glab auth status` 自己给的就是打了码的，我们连打码的那个也不存。
type glabLogin struct {
	Host     string
	User     string
	TokenSrc string
	GitProto string // git 操作用的协议：https / ssh
	APIProto string // API 调用的协议
	APIURL   string // REST 端点
	GQLURL   string // GraphQL 端点
	Note     string // 没登录或出错时的一句话
	LoggedIn bool
}

// toolCopy 是拿来问状态的那个程序：hmck 装的那份优先，没有就用 PATH 上别人的副本。
// glab 常常是 brew 装的，hmck 不接管它，但登录状态照样读得出来。
func toolCopy(st toolState) string {
	if st.Path != "" {
		return st.Path
	}
	if len(st.Foreign) > 0 {
		return st.Foreign[0]
	}
	return ""
}

// glabAuthLogins 跑 `glab auth status` 读登录状态。
// 永远不加 --show-token：这个命令只看「登没登录」。
func glabAuthLogins(bin string) ([]glabLogin, error) {
	out, err := runCapture(bin, "auth", "status")
	logins := parseGlabAuthStatus(out)
	if len(logins) == 0 {
		if err != nil {
			return nil, fmt.Errorf("glab auth status 失败: %v", err)
		}
		return nil, fmt.Errorf("glab auth status 没给出主机信息")
	}
	return logins, nil
}

// runCapture 跑一个小命令并连 stderr 一起收：glab 的报错就写在 stderr 上。
// 有超时——登录状态最多等几秒，不能把检查卡住。
func runCapture(bin string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(os.Environ(), "HMCK_NO_MIGRATE=1")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// parseGlabAuthStatus 解析 `glab auth status` 的输出。
// 形状是：顶格一行主机名，下面缩进若干条明细（✓ / x / ! 开头）。
// 主机之间可以是空的、带空格的行，最后还可能有 ERROR 块。
func parseGlabAuthStatus(out string) []glabLogin {
	var hosts []glabLogin
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimRight(raw, " \t\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		if line[0] != ' ' && line[0] != '\t' {
			if fields := strings.Fields(line); len(fields) > 0 && looksLikeHost(fields[0]) {
				hosts = append(hosts, glabLogin{Host: fields[0]})
			}
			// 顶格的 ERROR 之类不属于任何主机
			continue
		}
		if len(hosts) == 0 {
			continue
		}
		host := &hosts[len(hosts)-1]
		marker, body := splitStatusMarker(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(body, "Logged in to "):
			if cur := loginLinePattern.FindStringSubmatch(body); cur != nil {
				host.LoggedIn = true
				if host.Host == "" {
					host.Host = cur[1]
				}
				host.User = cur[2]
				if len(cur) > 3 && cur[3] != "" {
					host.TokenSrc = tokenSourceName(cur[3])
				}
			}
		case strings.HasPrefix(body, "Git operations"):
			host.GitProto = protocolIn(body)
		case strings.HasPrefix(body, "API calls"):
			host.APIProto = protocolIn(body)
		case strings.HasPrefix(body, "REST API Endpoint:"):
			host.APIURL = valueAfterColon(body)
		case strings.HasPrefix(body, "GraphQL Endpoint:"):
			host.GQLURL = valueAfterColon(body)
		case strings.HasPrefix(body, "Token found in "):
			host.TokenSrc = tokenSourceName(strings.TrimPrefix(body, "Token found in "))
		case strings.HasPrefix(body, "No token found"):
			// 前面那行 `x …: 401` 已经把原因说清了，就别拿这句更含糊的盖掉
			if host.Note == "" {
				host.Note = "没有找到 token"
			}
		default:
			// 失败是 `x <host>: API call failed: GET <url>: 401 {message: 401 Unauthorized}`，
			// 只留状态码和人话，别把整条 URL 摊在界面上
			if (isBadMarker(marker) || strings.HasPrefix(body, host.Host+":")) && host.Note == "" {
				host.Note = failureNote(body)
			}
		}
	}
	return hosts
}

var (
	loginLinePattern   = regexp.MustCompile(`^Logged in to (\S+) as ([^\s(]+)(?:\s+\(([^)]*)\))?`)
	protocolPattern    = regexp.MustCompile(`\b(https?|ssh)\b`)
	failureCodePattern = regexp.MustCompile(`\b([45]\d\d)\s+([A-Za-z][A-Za-z ]*)\}`)
)

// looksLikeHost 判断顶格那行是不是主机名：有 `:` 或 `.`，且没有空格和斜杠
func looksLikeHost(s string) bool {
	if s == "" || strings.ContainsAny(s, " /\\") {
		return false
	}
	return strings.ContainsAny(s, ".:")
}

// splitStatusMarker 去掉行首的 ✓ / x / ! 标记，留下正文
func splitStatusMarker(s string) (marker, body string) {
	for _, m := range []string{"✓", "✔", "√", "✗", "✖", "×", "x", "!"} {
		if strings.HasPrefix(s, m+" ") {
			return m, strings.TrimSpace(strings.TrimPrefix(s, m+" "))
		}
	}
	return "", s
}

func isBadMarker(m string) bool {
	switch m {
	case "x", "✗", "✖", "×", "!":
		return true
	}
	return false
}

// protocolIn 从 `… configured to use https protocol.` 里取出协议
func protocolIn(s string) string {
	if m := protocolPattern.FindString(strings.ToLower(s)); m != "" {
		return m
	}
	return ""
}

func valueAfterColon(s string) string {
	if _, v, ok := strings.Cut(s, ":"); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

// tokenSourceName 把 glab 的说法换成中文；只报来源，不碰 token 本身
func tokenSourceName(s string) string {
	src := strings.TrimSpace(s)
	if i := strings.Index(src, ":"); i >= 0 { // `operating system keyring: ******` 后面那段丢掉
		src = strings.TrimSpace(src[:i])
	}
	switch src {
	case "operating system keyring":
		return "系统钥匙串"
	case "config file":
		return "配置文件"
	case "environment variables":
		return "环境变量"
	}
	return src
}

// failureNote 把 glab 的报错行压成一句话：`API 调用失败：401 Unauthorized`
func failureNote(body string) string {
	if m := failureCodePattern.FindStringSubmatch(body); m != nil {
		return "API 调用失败：" + m[1] + " " + strings.TrimSpace(m[2])
	}
	if _, v, ok := strings.Cut(body, "API call failed: "); ok {
		if i := strings.LastIndex(v, ": "); i > 0 && len(v)-i < 120 {
			return "API 调用失败：" + strings.TrimSpace(v[i+2:])
		}
		return "API 调用失败"
	}
	return body
}

// loginBadgeOf 从读到的登录信息里挑一句短的，给卡片和状态行用
func loginBadgeOf(logins []glabLogin) (string, string) {
	for _, l := range logins {
		if !l.LoggedIn {
			continue
		}
		if l.User != "" {
			return "已登录 " + l.User, "ok"
		}
		return "已登录 " + l.Host, "ok"
	}
	if len(logins) > 0 {
		return "还没登录", "missing"
	}
	return "", ""
}

// ---------- 信息页的数据 ----------

// infoField 是信息页上的一行：左边名字，右边值
type infoField struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	Kind  string `json:"kind,omitempty"` // ok / warn / missing，决定怎么染色
}

// toolInfo 是一个工具的当前状况；`hmck info` 和 TUI 的信息页共用
type toolInfo struct {
	Key      string      `json:"key"`
	Headline string      `json:"headline"`
	Kind     string      `json:"kind"`
	Fields   []infoField `json:"fields"`
	Err      string      `json:"error,omitempty"`
}

// toolInfoFor 汇总一个工具的信息：能读登录状态的先放登录，再放版本、位置这些
func toolInfoFor(st toolState) toolInfo {
	label, kind := st.Status()
	info := toolInfo{Key: st.Tool.Key, Headline: label, Kind: kind, Fields: []infoField{}}
	if st.Tool.Status {
		if head, k, fields := loginInfo(st); head != "" {
			info.Headline, info.Kind = head, k
			info.Fields = append(info.Fields, fields...)
		}
	}
	info.Fields = append(info.Fields, installFields(st)...)
	return info
}

// loginInfo 是 glab 这类工具的头一条：登的是哪台主机、谁、token 从哪来
func loginInfo(st toolState) (string, string, []infoField) {
	if len(st.Logins) == 0 {
		if st.Missing() {
			return "○ 还没装 · 装好以后在终端里跑 glab auth login", "missing", nil
		}
		return "! 读不出登录状态", "warn", nil
	}

	primary := st.Logins[0]
	for _, l := range st.Logins {
		if l.LoggedIn {
			primary = l
			break
		}
	}
	headline, kind := "○ 还没登录 · 在终端里跑 glab auth login", "missing"
	if primary.LoggedIn {
		headline, kind = "✓ 已登录 "+primary.Host, "ok"
		if primary.User != "" {
			headline = "✓ 已登录 " + primary.User + "@" + primary.Host
		}
	}

	fields := []infoField{{Key: "主机", Value: primary.Host}}
	if primary.User != "" {
		fields = append(fields, infoField{Key: "账号", Value: primary.User})
	}
	switch {
	case primary.TokenSrc != "":
		fields = append(fields, infoField{Key: "凭证", Value: primary.TokenSrc})
	case primary.LoggedIn:
		fields = append(fields, infoField{Key: "凭证", Value: "已找到（来源没说）"})
	default:
		fields = append(fields, infoField{Key: "凭证", Value: "没有找到 token", Kind: "missing"})
	}
	if p := protoText(primary); p != "" {
		fields = append(fields, infoField{Key: "传输", Value: p})
	}
	if primary.APIURL != "" {
		fields = append(fields, infoField{Key: "REST API", Value: primary.APIURL})
	}
	if primary.GQLURL != "" {
		fields = append(fields, infoField{Key: "GraphQL", Value: primary.GQLURL})
	}
	if !primary.LoggedIn && primary.Note != "" {
		fields = append(fields, infoField{Key: "说明", Value: primary.Note, Kind: "warn"})
	}

	var others []string
	for _, l := range st.Logins {
		if l.Host == primary.Host {
			continue
		}
		label := l.Host
		switch {
		case !l.LoggedIn:
			label += "（没登录）"
		case l.User != "":
			label += "（" + l.User + "）"
		}
		others = append(others, label)
	}
	if len(others) > 0 {
		fields = append(fields, infoField{Key: "其他主机", Value: strings.Join(others, "\n")})
	}
	return headline, kind, fields
}

// protoText 把 git / API 两个协议拼成一行
func protoText(l glabLogin) string {
	var parts []string
	if l.GitProto != "" {
		parts = append(parts, "git "+l.GitProto)
	}
	if l.APIProto != "" {
		parts = append(parts, "API "+l.APIProto)
	}
	return strings.Join(parts, " · ")
}

// installFields 是每个工具都有的那几行：版本、位置、更新时间、其他副本
func installFields(st toolState) []infoField {
	var out []infoField
	ver, verKind := orDash(st.Installed), ""
	if st.Installed == "" && st.ForeignVer != "" {
		ver, verKind = st.ForeignVer+"（PATH 上的那份）", "warn"
	}
	out = append(out,
		infoField{Key: "版本", Value: ver, Kind: verKind},
		infoField{Key: "最新", Value: orDash(st.Latest)},
	)
	switch {
	case st.Path != "":
		out = append(out, infoField{Key: "位置", Value: shortenHome(st.Path)})
	case len(st.Foreign) > 0:
		out = append(out, infoField{Key: "位置", Value: "没被 hmck 接管（用下面这一份）", Kind: "warn"})
	default:
		out = append(out, infoField{Key: "位置", Value: "—"})
	}
	if !st.UpdatedAt.IsZero() {
		out = append(out, infoField{Key: "更新时间", Value: st.UpdatedAt.Format("2006-01-02 15:04")})
	}
	if len(st.Foreign) > 0 {
		paths := make([]string, 0, len(st.Foreign))
		for _, p := range st.Foreign {
			paths = append(paths, shortenHome(p))
		}
		out = append(out, infoField{Key: "其他副本", Value: strings.Join(paths, "\n")})
	}
	if st.CheckErr != "" {
		out = append(out, infoField{Key: "检查", Value: st.CheckErr, Kind: "warn"})
	}
	out = append(out, infoField{Key: "仓库", Value: st.Tool.Repo})
	return out
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// ---------- `hmck info --json` ----------

type infoItem struct {
	Key       string      `json:"key"`
	Name      string      `json:"name"`
	Repo      string      `json:"repo"`
	Installed string      `json:"installed,omitempty"`
	Latest    string      `json:"latest,omitempty"`
	Path      string      `json:"path,omitempty"`
	Foreign   []string    `json:"foreign,omitempty"`
	Status    string      `json:"status"`
	Headline  string      `json:"headline"`
	Kind      string      `json:"kind"`
	Fields    []infoField `json:"fields"`
	Error     string      `json:"error,omitempty"`
}

func infoItemFor(st toolState) infoItem {
	info := toolInfoFor(st)
	label, _ := st.Status()
	it := infoItem{
		Key: st.Tool.Key, Name: st.Tool.Name, Repo: st.Tool.Repo,
		Installed: st.Installed, Latest: st.Latest, Path: shortenHome(st.Path),
		Status: label, Headline: info.Headline, Kind: info.Kind,
		Fields: info.Fields, Error: info.Err,
	}
	for _, p := range st.Foreign {
		it.Foreign = append(it.Foreign, shortenHome(p))
	}
	return it
}
