package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// glabAuthOK 是 `glab auth status` 登录成功时的样子（主机和账号都是假的）
const glabAuthOK = `git.example.com
  ✓ Logged in to git.example.com as alice (keyring)
  ✓ Git operations for git.example.com configured to use https protocol.
  ✓ API calls for git.example.com are made over https protocol.
  ✓ REST API Endpoint: https://git.example.com/api/v4/
  ✓ GraphQL Endpoint: https://git.example.com/api/graphql/
  ✓ Token found in operating system keyring: ******
`

// glabAuthFail 是没登录 / token 过期时的样子
const glabAuthFail = `git.example.com
  x git.example.com: API call failed: GET https://git.example.com/api/v4/user: 401 {message: 401 Unauthorized}
  ! No token found (checked config file, keyring, and environment variables).

 ERROR
`

func TestParseGlabAuthStatusLoggedIn(t *testing.T) {
	logins := parseGlabAuthStatus(glabAuthOK)
	if len(logins) != 1 {
		t.Fatalf("主机数 = %d, %+v", len(logins), logins)
	}
	l := logins[0]
	if l.Host != "git.example.com" || l.User != "alice" || !l.LoggedIn {
		t.Errorf("登录信息 = %+v", l)
	}
	if l.TokenSrc != "系统钥匙串" {
		t.Errorf("凭证来源 = %q", l.TokenSrc)
	}
	if l.GitProto != "https" || l.APIProto != "https" {
		t.Errorf("协议 = %q / %q", l.GitProto, l.APIProto)
	}
	if l.APIURL != "https://git.example.com/api/v4/" {
		t.Errorf("REST = %q", l.APIURL)
	}
	if l.GQLURL != "https://git.example.com/api/graphql/" {
		t.Errorf("GraphQL = %q", l.GQLURL)
	}
}

func TestParseGlabAuthStatusFailure(t *testing.T) {
	logins := parseGlabAuthStatus(glabAuthFail)
	if len(logins) != 1 {
		t.Fatalf("主机数 = %d, %+v", len(logins), logins)
	}
	l := logins[0]
	if l.LoggedIn {
		t.Errorf("不该算登录成功: %+v", l)
	}
	if l.Note != "API 调用失败：401 Unauthorized" {
		t.Errorf("失败说明 = %q", l.Note)
	}
}

// 没登录时登录页要给出能照做的一句话，不能把整条 URL 摊出来
func TestLoginInfoNotLoggedIn(t *testing.T) {
	st := toolState{Tool: tool{Key: "glab", Repo: "gitlab-org/cli", Status: true}, Path: "/tmp/glab"}
	st.Logins = parseGlabAuthStatus(glabAuthFail)

	head, kind, fields := loginInfo(st)
	if !strings.Contains(head, "还没登录") || kind != "missing" {
		t.Errorf("headline = %q, kind = %q", head, kind)
	}
	joined := fieldsText(fields)
	if strings.Contains(joined, "https://git.example.com/api") {
		t.Errorf("信息里不该带完整 URL: %s", joined)
	}
	if !strings.Contains(joined, "401 Unauthorized") {
		t.Errorf("信息里要说明失败原因: %s", joined)
	}
}

// 登录成功时：头一句话是「谁 @ 哪台主机」，字段里只有来源，没有 token
func TestLoginInfoLoggedIn(t *testing.T) {
	st := toolState{Tool: tool{Key: "glab", Repo: "gitlab-org/cli", Status: true}, Path: "/tmp/glab"}
	st.Logins = parseGlabAuthStatus(glabAuthOK)

	head, kind, fields := loginInfo(st)
	if head != "✓ 已登录 alice@git.example.com" || kind != "ok" {
		t.Errorf("headline = %q, kind = %q", head, kind)
	}
	joined := fieldsText(fields)
	if strings.Contains(joined, "*") || strings.Contains(joined, "Token") {
		t.Errorf("信息里不该出现 token 或它的打码: %s", joined)
	}
	for _, want := range []string{"系统钥匙串", "git https · API https", "GraphQL"} {
		if !strings.Contains(joined, want) {
			t.Errorf("缺 %q: %s", want, joined)
		}
	}
}

// 没装任何 glab：headline 告诉人去装，别报错
func TestLoginInfoNotInstalled(t *testing.T) {
	st := toolState{Tool: tool{Key: "glab", Repo: "gitlab-org/cli", Status: true}}
	head, kind, _ := loginInfo(st)
	if kind != "missing" || !strings.Contains(head, "还没装") {
		t.Errorf("headline = %q, kind = %q", head, kind)
	}
}

// 只装了 PATH 上别人的那份：版本那一行说的是那份的版本，并标明没被接管
func TestInstallFieldsForeignVersion(t *testing.T) {
	st := toolState{
		Tool:       tool{Key: "glab", Repo: "gitlab-org/cli", OwnDir: true},
		Foreign:    []string{"/opt/homebrew/bin/glab"},
		ForeignVer: "1.130.0",
	}
	fields := fieldsText(installFields(st))
	if !strings.Contains(fields, "1.130.0（PATH 上的那份）") {
		t.Errorf("版本没写清是 PATH 上那份: %s", fields)
	}
	if !strings.Contains(fields, "没被 hmck 接管") {
		t.Errorf("位置没说明: %s", fields)
	}
}

// toolInfoFor 把登录放在版本前面：先回答「登没登录」，再回答「装的哪一版」
func TestToolInfoLoginFirst(t *testing.T) {
	st := toolState{
		Tool:      tool{Key: "glab", Repo: "gitlab-org/cli", Status: true},
		Path:      "/home/alice/.local/bin/glab",
		Installed: "1.131.0",
		Latest:    "1.131.0",
	}
	st.Logins = parseGlabAuthStatus(glabAuthOK)

	info := toolInfoFor(st)
	if info.Kind != "ok" || !strings.HasPrefix(info.Headline, "✓") {
		t.Errorf("headline = %q, kind = %q", info.Headline, info.Kind)
	}
	if len(info.Fields) < 2 || info.Fields[0].Key != "主机" {
		t.Fatalf("第一条应该是主机: %+v", info.Fields)
	}
	if last := info.Fields[len(info.Fields)-1]; last.Key != "仓库" {
		t.Errorf("最后一条应该是仓库: %+v", last)
	}
}

func fieldsText(fields []infoField) string {
	var b strings.Builder
	for _, f := range fields {
		b.WriteString(f.Key + " " + f.Value + " " + f.Kind + "\n")
	}
	return b.String()
}

// kubeConfigMinified 是 `kubectl config view --minify -o json` 的样子（值都是假的）
const kubeConfigMinified = `{
  "kind": "Config",
  "apiVersion": "v1",
  "current-context": "arn:aws-cn:eks:cn-northwest-1:123456789012:cluster/Sought",
  "clusters": [{
    "name": "arn:aws-cn:eks:cn-northwest-1:123456789012:cluster/Sought",
    "cluster": {
      "server": "https://ABC123.gr7.cn-northwest-1.eks.amazonaws.com.cn",
      "certificate-authority-data": "Y2VydA=="
    }
  }],
  "contexts": [{
    "name": "arn:aws-cn:eks:cn-northwest-1:123456789012:cluster/Sought",
    "context": {"cluster": "arn:aws-cn:eks:cn-northwest-1:123456789012:cluster/Sought", "user": "ops"}
  }],
  "users": [{"name": "ops", "user": {"token": "should-not-be-read"}}]
}`

const kubeVersionConnected = `{
  "clientVersion": {"gitVersion": "v1.37.2"},
  "kustomizeVersion": "v5.8.1",
  "serverVersion": {"gitVersion": "v1.34.12-eks-91f89a9"}
}`

const kubeVersionNoServer = `{
  "clientVersion": {"gitVersion": "v1.37.2"},
  "kustomizeVersion": "v5.8.1"
}`

func TestParseKubeConfig(t *testing.T) {
	ctx, cluster, server, ok := parseKubeConfig(kubeConfigMinified)
	if !ok {
		t.Fatal("应当解析成功")
	}
	if ctx != "arn:aws-cn:eks:cn-northwest-1:123456789012:cluster/Sought" {
		t.Errorf("context = %q", ctx)
	}
	if cluster != ctx {
		t.Errorf("集群 = %q", cluster)
	}
	if server != "https://ABC123.gr7.cn-northwest-1.eks.amazonaws.com.cn" {
		t.Errorf("server = %q", server)
	}
	if _, _, _, ok := parseKubeConfig("error: current-context must exist"); ok {
		t.Error("报错文字不该当成 kubeconfig")
	}
}

func TestParseKubeServerVersion(t *testing.T) {
	if got := parseKubeServerVersion(kubeVersionConnected); got != "v1.34.12-eks-91f89a9" {
		t.Errorf("服务端版本 = %q", got)
	}
	// 连不上时只有 clientVersion：不能把客户端版本当成集群连上了
	if got := parseKubeServerVersion(kubeVersionNoServer); got != "" {
		t.Errorf("没有 serverVersion 时应当返回空，得到 %q", got)
	}
}

func TestKubeConnectNote(t *testing.T) {
	cases := []struct{ in, want string }{
		{"error: You must be logged in to the server (the server has asked for the client to provide credentials)", "认证没通过"},
		{"error: current-context must exist in order to minify", "没有设置当前 context"},
		{"Unable to connect to the server: dial tcp 10.0.0.1:443: i/o timeout", "连接超时"},
		{"The connection to the server localhost:8080 was refused - did you specify the right host or port?", "连不上服务器"},
		{"W1123 10:21:32.123456   12345 loader.go:223] Config not found: /tmp/none\nerror: no such host", "连不上服务器"},
	}
	for _, c := range cases {
		if got := kubeConnectNote(c.in); !strings.Contains(got, c.want) {
			t.Errorf("kubeConnectNote(%q) = %q，想看到 %q", c.in, got, c.want)
		}
	}
}

func TestKubeBadge(t *testing.T) {
	connected := &kubeStatus{
		Context:       "arn:aws-cn:eks:cn-northwest-1:123456789012:cluster/Sought",
		ServerVersion: "v1.34.12-eks-91f89a9",
		Reachable:     true,
	}
	if head, kind := kubeBadge(connected); head != "✓ 已连通 Sought" || kind != "ok" {
		t.Errorf("headline = %q, kind = %q", head, kind)
	}

	connected.Reachable = false
	if head, kind := kubeBadge(connected); head != "! Sought 连不上" || kind != "warn" {
		t.Errorf("headline = %q, kind = %q", head, kind)
	}

	if head, kind := kubeBadge(&kubeStatus{}); kind != "missing" || !strings.Contains(head, "没有当前 context") {
		t.Errorf("headline = %q, kind = %q", head, kind)
	}
	if head, kind := kubeBadge(nil); kind != "warn" || !strings.Contains(head, "读不出") {
		t.Errorf("headline = %q, kind = %q", head, kind)
	}
}

// kubectl 的状态页：当前 context、API server、服务端版本都要露出来，
// 但 kubeconfig 里的 token 一个字都不能带出来
func TestKubeInfo(t *testing.T) {
	ctx, cluster, server, _ := parseKubeConfig(kubeConfigMinified)
	st := toolState{
		Tool:      tool{Key: "kubectl", Repo: "kubernetes/kubernetes", Status: true, StatusSource: "kubectl"},
		Path:      "/home/alice/.local/bin/kubectl",
		Installed: "1.37.2",
		Latest:    "1.37.2",
		Kube: &kubeStatus{
			Context: ctx, Cluster: cluster, Server: server,
			ServerVersion: parseKubeServerVersion(kubeVersionConnected), Reachable: true,
		},
	}
	info := toolInfoFor(st)
	if info.Kind != "ok" || !strings.Contains(info.Headline, "已连通 Sought") {
		t.Errorf("headline = %q, kind = %q", info.Headline, info.Kind)
	}
	joined := fieldsText(info.Fields)
	for _, want := range []string{ctx, server, "v1.34.12-eks-91f89a9", "✓ 能连上"} {
		if !strings.Contains(joined, want) {
			t.Errorf("缺 %q: %s", want, joined)
		}
	}
	if strings.Contains(joined, "should-not-be-read") || strings.Contains(joined, "token") {
		t.Errorf("信息里不该出现 token: %s", joined)
	}
}

// ---------- Argo CD 状态 ----------

func TestParseArgoUserInfo(t *testing.T) {
	cases := []struct {
		name   string
		out    string
		login  bool
		user   string
		issuer string
		answer bool
	}{
		{"登录成功", "Logged In: true\nUsername: alice\nIssuer: https://dex.example.com\nGroups: dev,ops\n", true, "alice", "https://dex.example.com", true},
		{"退出码 0 但没登录", "Logged In: false\n", false, "", "", true},
		{"缩进和空行", "\n  Logged In: true\n  Username: bob\n", true, "bob", "", true},
		{"不是这个命令的输出", "rpc error: something\n", false, "", "", false},
		{"空输出", "", false, "", "", false},
	}
	for _, c := range cases {
		login, user, issuer, answer := parseArgoUserInfo(c.out)
		if login != c.login || user != c.user || issuer != c.issuer || answer != c.answer {
			t.Errorf("%s: got (%v,%q,%q,%v), want (%v,%q,%q,%v)",
				c.name, login, user, issuer, answer, c.login, c.user, c.issuer, c.answer)
		}
	}
}

func TestArgoConnectNote(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"rpc error: code = Unauthenticated desc = invalid session: signature is invalid\n", "服务器不认这个 token（重新运行 argo-token 换一个）"},
		{"error: context deadline exceeded\n", "连接超时（检查网络或 VPN）"},
		{"rpc error: code = Unavailable desc = connection refused\n", "连不上服务器（检查地址、网络或 VPN）"},
		{"x509: certificate signed by unknown authority", "TLS 握手失败（证书或代理）"},
		{"something else entirely", "something else entirely"},
	}
	for _, c := range cases {
		if got := argoConnectNote(c.in); got != c.want {
			t.Errorf("argoConnectNote(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	long := strings.Repeat("x", 200)
	if got := argoConnectNote("error: " + long); len([]rune(got)) > 81 {
		t.Errorf("长报错要压短: %d", len([]rune(got)))
	}
}

func TestArgoServerHost(t *testing.T) {
	cases := map[string]string{
		"https://argocd.example.com":  "argocd.example.com",
		"https://argocd.example.com/": "argocd.example.com",
		"http://127.0.0.1:8080":       "127.0.0.1:8080",
		"argocd.example.com:443":      "argocd.example.com:443",
		"":                            "",
	}
	for in, want := range cases {
		if got := argoServerHost(in); got != want {
			t.Errorf("argoServerHost(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestArgoBadge(t *testing.T) {
	cfg := &argoStatus{Configured: true, Server: "https://argocd.example.com", HasToken: true}

	if head, kind := argoBadge(nil, false); kind != "warn" || !strings.Contains(head, "读不出") {
		t.Errorf("nil: %q %q", head, kind)
	}
	if head, kind := argoBadge(&argoStatus{}, false); kind != "missing" || !strings.Contains(head, "还没配置") {
		t.Errorf("没配置: %q %q", head, kind)
	}
	if head, kind := argoBadge(&argoStatus{}, true); kind != "missing" || !strings.Contains(head, "装好后") {
		t.Errorf("没装也没配置: %q %q", head, kind)
	}
	if head, kind := argoBadge(&argoStatus{Configured: true}, false); kind != "warn" || !strings.Contains(head, "服务器地址") {
		t.Errorf("缺地址: %q %q", head, kind)
	}
	noToken := &argoStatus{Configured: true, Server: "https://argocd.example.com"}
	if head, kind := argoBadge(noToken, false); kind != "warn" || !strings.Contains(head, "没有 token") {
		t.Errorf("没 token: %q %q", head, kind)
	}

	ok := *cfg
	ok.LoggedIn, ok.Checkable, ok.Checked, ok.User = true, true, true, "alice"
	if head, kind := argoBadge(&ok, false); head != "✓ token 可用（alice）" || kind != "ok" {
		t.Errorf("可用: %q %q", head, kind)
	}

	if head, kind := argoBadge(cfg, false); kind != "warn" || !strings.Contains(head, "没装 argocd") {
		t.Errorf("没法验证: %q %q", head, kind)
	}

	rejected := *cfg
	rejected.Checkable, rejected.Checked = true, true
	rejected.Note = "服务器不认这个 token（重新运行 argo-token 换一个）"
	if head, kind := argoBadge(&rejected, false); kind != "warn" || !strings.Contains(head, "token 不可用") {
		t.Errorf("被拒: %q %q", head, kind)
	}

	unreachable := *cfg
	unreachable.Checkable = true
	unreachable.Note = "连接超时（检查网络或 VPN）"
	if head, kind := argoBadge(&unreachable, false); kind != "warn" || !strings.Contains(head, "验证没通过") {
		t.Errorf("连不上: %q %q", head, kind)
	}
}

// 信息页只报「有没有 token、存在哪」，token 本身一个字都不能出现
func TestArgoInfoFields(t *testing.T) {
	st := toolState{
		Tool:      tool{Key: "argo-token", Repo: "hongmengchen/argo-token", Status: true, StatusSource: "argocd"},
		Path:      "/home/alice/.local/bin/argo-token",
		Installed: "0.1.1",
		Latest:    "0.1.1",
		Argo: &argoStatus{
			Configured: true, Server: "https://argocd.example.com", Host: "argocd.example.com",
			TokenFile: "/home/alice/.argocd/token", GRPCWeb: true, HasToken: true,
			Checkable: true, Checked: true, LoggedIn: true, User: "alice",
			Issuer: "https://dex.example.com",
		},
	}
	info := toolInfoFor(st)
	if info.Kind != "ok" || !strings.Contains(info.Headline, "token 可用") {
		t.Errorf("headline = %q, kind = %q", info.Headline, info.Kind)
	}
	joined := fieldsText(info.Fields)
	for _, want := range []string{"https://argocd.example.com", "alice", "https://dex.example.com", "已写入", "grpc-web", "开", "get-user-info"} {
		if !strings.Contains(joined, want) {
			t.Errorf("缺 %q: %s", want, joined)
		}
	}
	if strings.Contains(joined, "hmck-test-token") {
		t.Errorf("信息里不该出现 token: %s", joined)
	}
}

// argoSandbox 把家目录、配置目录、PATH 都换到临时目录：
// 不碰真实的 ~/.argocd，也不会去连真实服务器
type argoSandbox struct {
	home, config, bin string
}

func newArgoSandbox(t *testing.T) *argoSandbox {
	t.Helper()
	sb := &argoSandbox{home: t.TempDir(), config: t.TempDir(), bin: t.TempDir()}
	oldHome, oldCfg, oldExe := userHomeDir, userConfigDir, runningExe
	userHomeDir = func() (string, error) { return sb.home, nil }
	userConfigDir = func() (string, error) { return sb.config, nil }
	runningExe = func() (string, error) { return filepath.Join(t.TempDir(), "hmck"), nil }
	t.Cleanup(func() { userHomeDir, userConfigDir, runningExe = oldHome, oldCfg, oldExe })
	t.Setenv("PATH", sb.bin)
	t.Setenv("HOME", sb.home)
	return sb
}

func (sb *argoSandbox) configure(t *testing.T, server, token string) {
	t.Helper()
	cfg := fmt.Sprintf(`{"server":%q,"token_file":%q,"grpc_web":true}`,
		server, filepath.Join(sb.home, ".argocd", "token"))
	writeFile(t, filepath.Join(sb.config, "argo-token", "config.json"), cfg)
	if token != "" {
		writeFile(t, filepath.Join(sb.home, ".argocd", "token"), token+"\n")
	}
}

// fakeArgocd 在 PATH 上放一个假的 argocd，用它自己的命令行和验证输出冒充官方 CLI
func (sb *argoSandbox) fakeArgocd(t *testing.T, logPath, body string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("假的 argocd 是 shell 脚本，Windows 上不跑")
	}
	writeFile(t, filepath.Join(sb.bin, "argocd"), "#!/bin/sh\n"+body)
}

// 配置会跟着 sync 跨机器走：Windows 写下的 `~\.argocd\token` 在 macOS 上
// 也得落到本机的 ~/.argocd/token，不然明明有 token 也会说没有
func TestArgoLocalTokenFile(t *testing.T) {
	sb := newArgoSandbox(t)
	want := filepath.Join(sb.home, ".argocd", "token")
	for _, in := range []string{"", `~\.argocd\token`, "~/.argocd/token", "~/./.argocd/token"} {
		if got := argoLocalTokenFile(in); got != want {
			t.Errorf("argoLocalTokenFile(%q) = %q, want %q", in, got, want)
		}
	}
	if got := argoLocalTokenFile("~"); got != sb.home {
		t.Errorf("~ 应该落到家目录: %q", got)
	}
	if got := argoLocalTokenFile("/etc/argo/token"); got != "/etc/argo/token" {
		t.Errorf("别的路径原样保留: %q", got)
	}
}

const argoTestToken = "hmck-test-token"

func TestReadArgoStatusVerifiesTokenWithCLI(t *testing.T) {
	sb := newArgoSandbox(t)
	sb.configure(t, "https://argocd.example.com", argoTestToken)
	logPath := filepath.Join(t.TempDir(), "calls.log")
	sb.fakeArgocd(t, logPath, `
{
  echo "args:$*"
  echo "token:${ARGOCD_AUTH_TOKEN}"
  echo "opts:${ARGOCD_OPTS:-<unset>}"
} > '`+logPath+`'
printf 'Logged In: true\nUsername: alice\nIssuer: https://dex.example.com\nGroups: dev\n'
`)
	t.Setenv("ARGOCD_OPTS", "--insecure") // 调用方 shell 里的 Argo 变量不该混进来

	a := readArgoStatus()
	if a == nil || !a.Configured || a.Server != "https://argocd.example.com" || a.Host != "argocd.example.com" {
		t.Fatalf("配置读错: %+v", a)
	}
	if !a.Checkable || !a.Checked || !a.LoggedIn || a.User != "alice" || a.Issuer != "https://dex.example.com" {
		t.Errorf("验证结果 = %+v", a)
	}
	if !a.HasToken || !a.GRPCWeb {
		t.Errorf("token/grpc-web = %v/%v", a.HasToken, a.GRPCWeb)
	}

	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := string(log)
	if strings.Contains(strings.SplitN(lines, "\n", 2)[0], argoTestToken) {
		t.Errorf("token 出现在命令行参数里: %s", lines)
	}
	if !strings.Contains(lines, "token:"+argoTestToken) {
		t.Errorf("token 没通过环境变量传进去: %s", lines)
	}
	if !strings.Contains(lines, "opts:<unset>") {
		t.Errorf("调用方的 ARGOCD_OPTS 应该被清掉: %s", lines)
	}
	if !strings.Contains(lines, "--config") || !strings.Contains(lines, "--server argocd.example.com") ||
		!strings.Contains(lines, "--grpc-web") || !strings.Contains(lines, "account get-user-info") {
		t.Errorf("参数不对: %s", lines)
	}
	if strings.Contains(lines, "--plaintext") {
		t.Errorf("https 不该带 --plaintext: %s", lines)
	}
	if strings.Contains(a.Note+a.User+a.Issuer, argoTestToken) {
		t.Error("状态里不该带 token")
	}
}

func TestReadArgoStatusRejectsBadToken(t *testing.T) {
	sb := newArgoSandbox(t)
	sb.configure(t, "https://argocd.example.com", argoTestToken)
	sb.fakeArgocd(t, filepath.Join(t.TempDir(), "calls.log"), `
echo 'rpc error: code = Unauthenticated desc = invalid session: signature is invalid' >&2
exit 1
`)
	a := readArgoStatus()
	if !a.Configured || !a.HasToken {
		t.Fatalf("配置/token 没读到: %+v", a)
	}
	// 连上了、服务器明确拒了 token：算「验证过」，但不算「可用」
	if a.LoggedIn || !a.Checked || !a.Checkable {
		t.Errorf("不该当成验证通过: %+v", a)
	}
	if !strings.Contains(a.Note, "不认这个 token") {
		t.Errorf("说明 = %q", a.Note)
	}
	if head, kind := argoBadge(a, false); kind != "warn" || !strings.Contains(head, "token 不可用") {
		t.Errorf("badge = %q %q", head, kind)
	}
}

func TestReadArgoStatusWithoutCLI(t *testing.T) {
	sb := newArgoSandbox(t)
	sb.configure(t, "https://argocd.example.com", argoTestToken)
	a := readArgoStatus()
	if !a.Configured || !a.HasToken || a.Checkable || a.Checked {
		t.Errorf("没有 argocd 时只该说配置: %+v", a)
	}
	if head, kind := argoBadge(a, false); kind != "warn" || !strings.Contains(head, "没装 argocd") {
		t.Errorf("badge = %q %q", head, kind)
	}
	_, _, fields := argoInfo(toolState{Tool: tool{Key: "argo-token"}, Argo: a})
	joined := fieldsText(fields)
	if !strings.Contains(joined, "没跑：本机没有 argocd CLI") || !strings.Contains(joined, "hmck install argocd") {
		t.Errorf("信息页没说清怎么验证: %s", joined)
	}
}

func TestReadArgoStatusNotConfigured(t *testing.T) {
	newArgoSandbox(t)
	a := readArgoStatus()
	if a == nil || a.Configured || a.HasToken || a.Checked {
		t.Fatalf("没配置时不该有状态: %+v", a)
	}
	if head, kind := argoBadge(a, false); kind != "missing" || !strings.Contains(head, "还没配置") {
		t.Errorf("badge = %q %q", head, kind)
	}
}
