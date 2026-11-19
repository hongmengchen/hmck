package main

import (
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
