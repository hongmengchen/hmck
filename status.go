package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
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

// runCaptureOut 和 runCapture 一样带超时，但把 stdout 和 stderr 分开：
// kubectl 的 JSON 边上还会写报错，混在一起就解析不出来了
func runCaptureOut(bin string, args ...string) (stdout, stderr string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(os.Environ(), "HMCK_NO_MIGRATE=1")
	var eb strings.Builder
	cmd.Stderr = &eb
	out, err := cmd.Output()
	return string(out), eb.String(), err
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

// ---------- 集群状态（kubectl） ----------

// kubeStatus 是 kubectl 眼里的当前集群：用哪个 context、连哪台服务器、连不连得上。
// 只记名字、地址和版本，不读 kubeconfig 里的证书和 token。
type kubeStatus struct {
	Context       string
	Cluster       string
	Server        string
	ServerVersion string
	Reachable     bool
	Note          string // 连不上时的一句话
}

// readKubeStatus 跑两条只读命令：一条读当前 context，一条连集群问服务端版本。
// 两条失败也尽量给出结果——界面上要说清楚是「没有 context」还是「连不上」；
// 两条都一个字都没吐出来时才当读不出来（二进制坏了之类）。
func readKubeStatus(bin string) *kubeStatus {
	cfgOut, cfgErr, _ := runCaptureOut(bin, "config", "view", "--minify", "-o", "json")
	verOut, verErr, _ := runCaptureOut(bin, "version", "-o", "json", "--request-timeout=4s")

	k := &kubeStatus{}
	ctxName, cluster, server, cfgOK := parseKubeConfig(cfgOut)
	k.Context, k.Cluster, k.Server = ctxName, cluster, server

	// 连通性问服务端版本：拿到了就是连得上，顺带知道集群是哪个版本；
	// 连不上 / token 过期时 kubectl 只给 clientVersion 和一行报错
	if v := parseKubeServerVersion(verOut); v != "" {
		k.Reachable = true
		k.ServerVersion = v
	}

	switch {
	case k.Context == "" && cfgErr != "":
		// 没有当前 context 时 config view 自己会报 current-context must exist
		k.Note = kubeConnectNote(cfgErr)
	case !k.Reachable:
		k.Note = kubeConnectNote(verErr)
	}
	if !cfgOK && strings.TrimSpace(cfgOut) == "" && strings.TrimSpace(verOut) == "" &&
		strings.TrimSpace(cfgErr) == "" && strings.TrimSpace(verErr) == "" {
		return nil
	}
	return k
}

// parseKubeConfig 从 `kubectl config view --minify -o json` 里取当前 context、
// 集群名和 API server；kubeconfig 里的证书和 token 一概不碰
func parseKubeConfig(out string) (ctx, cluster, server string, ok bool) {
	var cfg struct {
		CurrentContext string `json:"current-context"`
		Clusters       []struct {
			Name    string `json:"name"`
			Cluster struct {
				Server string `json:"server"`
			} `json:"cluster"`
		} `json:"clusters"`
	}
	if json.Unmarshal([]byte(out), &cfg) != nil {
		return "", "", "", false
	}
	ctx = strings.TrimSpace(cfg.CurrentContext)
	// --minify 后 clusters 里只剩当前 context 用的那个
	if len(cfg.Clusters) > 0 {
		cluster = cfg.Clusters[0].Name
		server = cfg.Clusters[0].Cluster.Server
	}
	return ctx, cluster, server, true
}

// parseKubeServerVersion 从 `kubectl version -o json` 里取服务端版本；
// 连不上时输出里只有 clientVersion，这里返回空
func parseKubeServerVersion(out string) string {
	var ver struct {
		ServerVersion struct {
			GitVersion string `json:"gitVersion"`
		} `json:"serverVersion"`
	}
	if json.Unmarshal([]byte(out), &ver) != nil {
		return ""
	}
	return strings.TrimSpace(ver.ServerVersion.GitVersion)
}

var kubeLogPrefix = regexp.MustCompile(`^[EW]\d{4} `) // klog 的 W1123 10:21:32.123 … 前缀

// kubeConnectNote 把 kubectl 的报错压成一句人话
func kubeConnectNote(out string) string {
	line := ""
	for _, raw := range strings.Split(out, "\n") {
		s := strings.TrimSpace(raw)
		if s == "" || kubeLogPrefix.MatchString(s) || strings.HasPrefix(strings.ToLower(s), "warning") {
			continue
		}
		line = s
		break
	}
	if line == "" {
		return ""
	}
	low := strings.ToLower(line)
	switch {
	case strings.Contains(low, "must be logged in"), strings.Contains(low, "unauthorized"):
		return "认证没通过：token 可能过期了（用 kube-token 换新）"
	case strings.Contains(low, "current-context is not set"),
		strings.Contains(low, "current-context must exist"),
		strings.Contains(low, "no context"):
		return "没有设置当前 context"
	case strings.Contains(low, "i/o timeout"), strings.Contains(low, "context deadline exceeded"),
		strings.Contains(low, "timeout"):
		return "连接超时（检查网络或 VPN）"
	case strings.Contains(low, "connection refused"), strings.Contains(low, "connection to the server"),
		strings.Contains(low, "unable to connect to the server"), strings.Contains(low, "no such host"):
		return "连不上服务器（检查 kubeconfig 里的地址、网络或 VPN）"
	}
	line = strings.TrimPrefix(line, "error: ")
	if r := []rune(line); len(r) > 100 {
		line = string(r[:100]) + "…"
	}
	return line
}

// kubeBadge 是卡片第三行和状态页头一句：连上了说 context 短名，连不上说清是哪个集群
func kubeBadge(k *kubeStatus) (string, string) {
	if k == nil {
		return "读不出集群状态", "warn"
	}
	name := kubeShortName(k)
	switch {
	case k.Context == "":
		return "○ 没有当前 context · 跑 kubectl config use-context", "missing"
	case k.Reachable && name != "":
		return "✓ 已连通 " + name, "ok"
	case k.Reachable:
		return "✓ 已连通集群", "ok"
	case name != "":
		return "! " + name + " 连不上", "warn"
	default:
		return "! 连不上集群", "warn"
	}
}

// kubeShortName 把 EKS 那种长 ARN 压成集群名：取最后一段（cluster/Sought → Sought）
func kubeShortName(k *kubeStatus) string {
	name := k.Context
	if i := strings.LastIndex(name, "/"); i >= 0 && i < len(name)-1 {
		name = name[i+1:]
	}
	return name
}

// kubeInfo 是 kubectl 的头一条：当前 context、API server、服务端版本、连不连得上
func kubeInfo(st toolState) (string, string, []infoField) {
	if st.Kube == nil {
		if st.Missing() && len(st.Foreign) == 0 {
			return "○ 还没装 · 装好以后用 kubectl config use-context 选集群", "missing", nil
		}
		return "! 读不出集群状态", "warn", nil
	}
	k := st.Kube
	headline, kind := kubeBadge(k)

	var fields []infoField
	if k.Context != "" {
		fields = append(fields, infoField{Key: "当前 context", Value: k.Context})
	} else {
		fields = append(fields, infoField{Key: "当前 context", Value: "没有设置", Kind: "missing"})
	}
	if k.Cluster != "" && k.Cluster != k.Context {
		fields = append(fields, infoField{Key: "集群", Value: k.Cluster})
	}
	if k.Server != "" {
		fields = append(fields, infoField{Key: "API server", Value: k.Server})
	}
	if k.Reachable {
		fields = append(fields, infoField{Key: "连通", Value: "✓ 能连上", Kind: "ok"})
	} else {
		fields = append(fields, infoField{Key: "连通", Value: "✗ 连不上", Kind: "warn"})
	}
	if k.ServerVersion != "" {
		fields = append(fields, infoField{Key: "服务端版本", Value: k.ServerVersion})
	}
	if k.Note != "" {
		fields = append(fields, infoField{Key: "说明", Value: k.Note, Kind: "warn"})
	}
	return headline, kind, fields
}

// ---------- Argo CD 状态（argocd CLI） ----------

// argoTokenConfig 是 argo-token 写下的配置，字段和它自己的 config.json 一致：
// 连哪台服务器、token 放在哪个文件、要不要走 grpc-web。没有 token 本身。
type argoTokenConfig struct {
	Server    string `json:"server"`
	TokenFile string `json:"token_file"`
	GRPCWeb   bool   `json:"grpc_web"`
}

// argoStatus 是「argo-token 配好了没有、argocd CLI 拿这个 token 还能不能登录」。
// token 只在验证时过一遍子进程的环境变量，不进这里、也不打印。
type argoStatus struct {
	Configured bool
	Server     string // 配置里的地址，原样（带协议）
	Host       string // 给 argocd --server 用的 host[:port]
	TokenFile  string
	GRPCWeb    bool
	HasToken   bool
	Checkable  bool // 本机有 argocd CLI，能真的验一次
	Checked    bool // CLI 连上了服务器并给出了回答
	LoggedIn   bool
	User       string
	Issuer     string
	Note       string // 没配置 / 没 token / 验证没通过时的一句话
}

// argoConfigPath 是 argo-token 的配置文件，和它自己的 configPath 一致
func argoConfigPath() (string, error) {
	dir, err := userConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "argo-token", "config.json"), nil
}

// argoDefaultTokenFile 是 argo-token 的默认 token 位置
func argoDefaultTokenFile() string {
	home, err := userHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".argocd", "token")
}

// argoLocalTokenFile 把配置里的 token 路径落到本机。配置会跟着 hmck sync
// 在几台机器之间走，从 Windows 同步来的可能是 `~\.argocd\token`：
// 两种分隔符都认，落到当前系统的家目录下；路径本身不解释的话状态就会说错。
func argoLocalTokenFile(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return argoDefaultTokenFile()
	}
	home, err := userHomeDir()
	if err == nil && home != "" {
		switch {
		case p == "~":
			p = home
		case strings.HasPrefix(p, "~/"), strings.HasPrefix(p, `~\`):
			rel := strings.ReplaceAll(p[2:], `\`, "/")
			p = filepath.Join(home, filepath.FromSlash(rel))
		}
	}
	return filepath.Clean(p)
}

// readArgoStatus 读 argo-token 的配置，再用官方 argocd CLI 验一次 token。
// 不要求 argo-token 自己装着：配置在就先说配置，argocd 在就再验一遍 token。
func readArgoStatus() *argoStatus {
	path, err := argoConfigPath()
	if err != nil {
		return nil
	}
	a := &argoStatus{}
	data, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
		return a // 从来没配置过，界面按「还没配置」说
	case err != nil:
		a.Note = "读不了 argo-token 配置：" + trimNote(err.Error())
		return a
	}
	var cfg argoTokenConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		a.Note = "argo-token 配置不是合法 JSON"
		return a
	}

	a.Configured = true
	a.Server = strings.TrimSpace(cfg.Server)
	a.Host = argoServerHost(a.Server)
	a.GRPCWeb = cfg.GRPCWeb
	a.TokenFile = argoLocalTokenFile(cfg.TokenFile)

	var token string
	if a.TokenFile != "" {
		if data, err := os.ReadFile(a.TokenFile); err == nil {
			token = strings.TrimSpace(string(data))
			a.HasToken = token != ""
		}
	}
	if a.Server == "" || !a.HasToken {
		return a
	}

	t, ok := toolByKey("argocd")
	if !ok {
		return a
	}
	bin := toolBinary(t)
	if bin == "" {
		return a
	}
	a.Checkable = true
	ans := argoCheckToken(bin, cfg, a.Host, token)
	a.Checked = ans.Answered
	a.LoggedIn = ans.LoggedIn
	a.User = ans.User
	a.Issuer = ans.Issuer
	a.Note = ans.Note
	return a
}

// argoAnswer 是 argocd CLI 对「这个 token 还能不能用」的回答
type argoAnswer struct {
	Answered bool // 连上了服务器、给出了回答（认或不认 token）
	LoggedIn bool
	User     string
	Issuer   string
	Note     string
}

// argoCheckToken 用官方 CLI 问一次 `argocd account get-user-info`——只读命令。
// token 只放进子进程的 ARGOCD_AUTH_TOKEN（不进参数，ps 看不到）；
// --config 指向一个不存在的临时路径：既不读用户已经登录过的 context，
// 也不让它在那儿建文件。空文件反而会被新版 argocd 当成
// 「current-context 没设」直接拒绝。
func argoCheckToken(bin string, cfg argoTokenConfig, host, token string) argoAnswer {
	dir, err := os.MkdirTemp("", "hmck-argocd-*")
	if err != nil {
		return argoAnswer{Note: "建不了临时目录，跳过验证"}
	}
	defer os.RemoveAll(dir)
	confPath := filepath.Join(dir, "config")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	args := []string{"--config", confPath, "--server", host}
	if cfg.GRPCWeb {
		args = append(args, "--grpc-web")
	}
	// 只有 http:// 才加 --plaintext；https 上加它是往明文端口打
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(cfg.Server)), "http://") {
		args = append(args, "--plaintext")
	}
	args = append(args, "account", "get-user-info")

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = argoProbeEnv(token)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()

	loggedIn, user, issuer, answered := parseArgoUserInfo(string(out))
	if answered {
		if loggedIn {
			return argoAnswer{Answered: true, LoggedIn: true, User: user, Issuer: issuer}
		}
		return argoAnswer{Answered: true, Note: "服务器不认这个 token（重新运行 argo-token 换一个）"}
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return argoAnswer{Note: "验证超时（10 秒没连上服务器）"}
	}
	note := argoConnectNote(stderr.String())
	if note == "" && err != nil {
		note = "argocd 退出异常：" + trimNote(err.Error())
	}
	if note == "" {
		note = "argocd 没给出登录状态"
	}
	// 服务器明确拒了 token 也是「回答了」：不是没连上，是连上了但不认这个 token
	return argoAnswer{Answered: strings.Contains(note, "不认这个 token"), Note: note}
}

// argoProbeEnv 是验证时给子进程的环境变量：先清掉调用方 shell 里的 Argo 变量
// （比如 ~/.argocd/env 里的 ARGOCD_OPTS），再只放这一份 token。
func argoProbeEnv(token string) []string {
	drop := map[string]bool{
		"ARGOCD_AUTH_TOKEN": true,
		"ARGOCD_SERVER":     true,
		"ARGOCD_OPTS":       true,
		"ARGOCD_CONTEXT":    true,
		"ARGOCD_CONFIG":     true,
	}
	env := make([]string, 0, len(os.Environ())+2)
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); drop[k] {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "HMCK_NO_MIGRATE=1", "ARGOCD_AUTH_TOKEN="+token)
}

// parseArgoUserInfo 解析 `argocd account get-user-info` 的纯文本输出：
//
//	Logged In: true
//	Username: alice
//	Issuer: https://dex.example.com
//	Groups: ...
//
// 退出码 0 也可能配着 `Logged In: false`，所以以这一行为准。
func parseArgoUserInfo(out string) (loggedIn bool, user, issuer string, answered bool) {
	for _, raw := range strings.Split(out, "\n") {
		key, val, ok := strings.Cut(strings.TrimSpace(raw), ":")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		val = strings.TrimSpace(val)
		switch key {
		case "logged in":
			answered = true
			loggedIn = strings.EqualFold(val, "true")
		case "username":
			user = val
		case "issuer":
			issuer = val
		}
	}
	return loggedIn, user, issuer, answered
}

// argoConnectNote 把 argocd 的报错压成一句人话。报错里不会有 token，
// 这里也不再展开整条 rpc 信息。
func argoConnectNote(out string) string {
	line := ""
	for _, raw := range strings.Split(out, "\n") {
		if s := strings.TrimSpace(raw); s != "" {
			line = s
			break
		}
	}
	if line == "" {
		return ""
	}
	low := strings.ToLower(line)
	switch {
	case strings.Contains(low, "unauthenticated"), strings.Contains(low, "invalid session"),
		strings.Contains(low, "signature is invalid"), strings.Contains(low, "token is expired"),
		strings.Contains(low, " 401"):
		return "服务器不认这个 token（重新运行 argo-token 换一个）"
	case strings.Contains(low, "deadline exceeded"), strings.Contains(low, "timeout"):
		return "连接超时（检查网络或 VPN）"
	case strings.Contains(low, "connection refused"), strings.Contains(low, "no such host"),
		strings.Contains(low, "connection reset"), strings.Contains(low, "unavailable"):
		return "连不上服务器（检查地址、网络或 VPN）"
	case strings.Contains(low, "x509"), strings.Contains(low, "tls"):
		return "TLS 握手失败（证书或代理）"
	}
	line = strings.TrimPrefix(line, "rpc error: ")
	line = strings.TrimPrefix(line, "error: ")
	return trimNote(line)
}

// trimNote 剁掉一句提示的尾巴，界面上一行放得下
func trimNote(s string) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > 80 {
		return string(r[:80]) + "…"
	}
	return s
}

// argoServerHost 把配置里的 URL 变成 argocd --server 认的 host[:port]
func argoServerHost(server string) string {
	s := strings.TrimSpace(server)
	if strings.Contains(s, "://") {
		if u, err := url.Parse(s); err == nil && u.Host != "" {
			return u.Host
		}
	}
	return strings.TrimRight(s, "/")
}

// argoBadge 是卡片第三行和信息页头一句：配置好没有、token 还能不能用
func argoBadge(a *argoStatus, missing bool) (string, string) {
	if a == nil {
		return "! 读不出 Argo 配置", "warn"
	}
	switch {
	case !a.Configured && a.Note != "":
		return "! " + a.Note, "warn"
	case !a.Configured && missing:
		return "○ 还没配置 · 装好后填 Argo 服务器和 token", "missing"
	case !a.Configured:
		return "○ 还没配置 · 运行 hmck run argo-token 填写", "missing"
	case a.Server == "":
		return "! 配置里没有服务器地址 · 重新运行 argo-token", "warn"
	case !a.HasToken:
		return "! 没有 token · 运行 hmck run argo-token 粘贴", "warn"
	case a.LoggedIn && a.User != "":
		return "✓ token 可用（" + a.User + "）", "ok"
	case a.LoggedIn:
		return "✓ token 可用", "ok"
	case !a.Checkable:
		return "已配置 · 没装 argocd，没法验证", "warn"
	case a.Checked:
		return "! token 不可用 · 重新运行 argo-token", "warn"
	default:
		return "! 验证没通过：" + a.Note, "warn"
	}
}

// argoInfo 是 argo-token 的头一条：配到哪、token 在不在、argocd 怎么说
func argoInfo(st toolState) (string, string, []infoField) {
	a := st.Argo
	if a == nil {
		return "! 读不出 Argo 配置", "warn", nil
	}
	headline, kind := argoBadge(a, st.Missing())

	var fields []infoField
	if a.Server != "" {
		fields = append(fields, infoField{Key: "服务器", Value: a.Server})
	}
	if a.User != "" {
		fields = append(fields, infoField{Key: "账号", Value: a.User})
	}
	if a.Issuer != "" {
		fields = append(fields, infoField{Key: "签发方", Value: a.Issuer})
	}
	if a.Configured {
		tok, tokKind := "✗ 没找到 token 文件", "missing"
		if a.HasToken {
			tok, tokKind = "✓ 已写入", ""
		}
		if a.TokenFile != "" {
			tok += "（" + shortenHome(a.TokenFile) + "）"
		}
		fields = append(fields, infoField{Key: "token", Value: tok, Kind: tokKind})
		grpc := "关"
		if a.GRPCWeb {
			grpc = "开（--grpc-web）"
		}
		fields = append(fields, infoField{Key: "grpc-web", Value: grpc})
	}

	switch {
	case a.LoggedIn:
		fields = append(fields, infoField{
			Key: "验证", Value: "✓ argocd account get-user-info 说 token 可用", Kind: "ok",
		})
	case a.Checked:
		fields = append(fields, infoField{Key: "验证", Value: "✗ 服务器不认这个 token", Kind: "warn"})
	case a.Configured && a.HasToken && !a.Checkable:
		fields = append(fields, infoField{Key: "验证", Value: "没跑：本机没有 argocd CLI", Kind: "warn"})
	case a.Configured && a.HasToken:
		fields = append(fields, infoField{Key: "验证", Value: "✗ 没连上服务器", Kind: "warn"})
	}
	if a.Note != "" && !a.LoggedIn {
		fields = append(fields, infoField{Key: "说明", Value: a.Note, Kind: "warn"})
	}
	if a.Configured && a.HasToken && !a.Checkable {
		fields = append(fields, infoField{Key: "提示", Value: "hmck install argocd 之后就能自动验证"})
	}
	return headline, kind, fields
}

// statusInfo 按工具选读状态的那一套：glab 看登录，kubectl 看集群，argo-token 看 token
func statusInfo(st toolState) (string, string, []infoField) {
	switch st.Tool.StatusSource {
	case "kubectl":
		return kubeInfo(st)
	case "argocd":
		return argoInfo(st)
	}
	return loginInfo(st)
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
		if head, k, fields := statusInfo(st); head != "" {
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
		if st.Missing() && len(st.Foreign) == 0 {
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
		out = append(out, infoField{Key: "位置", Value: "没被 hmck 接管", Kind: "warn"})
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
		// 没接管时，PATH 上这唯一一份就是正在用的，不能叫「其他副本」
		label := "其他副本"
		if st.Path == "" {
			label = "用的这份"
		}
		out = append(out, infoField{Key: label, Value: strings.Join(paths, "\n")})
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
