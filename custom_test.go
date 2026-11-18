package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func withTempConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old := userConfigDir
	userConfigDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { userConfigDir = old })
	return dir
}

func TestParseRepo(t *testing.T) {
	cases := map[string]string{
		"owner/repo":                        "owner/repo",
		"https://github.com/owner/repo":     "owner/repo",
		"https://github.com/owner/repo.git": "owner/repo",
		"github.com/owner/repo/":            "owner/repo",
	}
	for in, want := range cases {
		got, err := parseRepo(in)
		if err != nil || got != want {
			t.Errorf("parseRepo(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "owner", "https://gitlab.com/a/b"} {
		if got, err := parseRepo(bad); err == nil {
			t.Errorf("parseRepo(%q) 应该报错，得到 %q", bad, got)
		}
	}
}

func TestInferPlatformsAndChecksums(t *testing.T) {
	assets := []string{
		"my-tool_darwin_arm64", "my-tool_linux_amd64", "my-tool_windows_amd64.exe",
		"my-tool_darwin_arm64.tar.gz", "other_linux_amd64", "my-tool_readme",
	}
	got := inferPlatforms("my-tool", assets)
	want := []string{"darwin/arm64", "linux/amd64", "windows/amd64"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("platforms = %v, want %v", got, want)
	}
	if !hasChecksums([]string{"my-tool_linux_amd64", "checksums.txt"}) {
		t.Error("应该识别出 checksums.txt")
	}
	if hasChecksums([]string{"my-tool_linux_amd64"}) {
		t.Error("没有 checksums.txt 却报告有")
	}
}

func TestCustomToolRoundTrip(t *testing.T) {
	dir := withTempConfig(t)

	tl := tool{Key: "my-tool", Repo: "owner/my-tool", Platforms: []string{"linux/amd64"}}
	path, err := addCustomTool(tl, false)
	if err != nil {
		t.Fatalf("addCustomTool: %v", err)
	}
	if path != filepath.Join(dir, "hmck", "tools.json") {
		t.Errorf("配置路径 = %s", path)
	}

	tools, err := allTools()
	if err != nil {
		t.Fatal(err)
	}
	var found *tool
	for i := range tools {
		if tools[i].Key == "my-tool" {
			found = &tools[i]
		}
	}
	if found == nil {
		t.Fatal("allTools 里没有 my-tool")
	}
	if !found.Custom || found.Binary != "my-tool" || found.Name != "my-tool" {
		t.Errorf("自定义工具字段没补全: %+v", *found)
	}
	if len(tools) != len(registry)+1 {
		t.Errorf("工具数 = %d，内置 %d", len(tools), len(registry))
	}

	if _, err := addCustomTool(tl, false); err == nil {
		t.Error("重复添加同一 key 应当报错")
	}
	if _, err := addCustomTool(tool{Key: "my-tool", Repo: "owner/other", Platforms: []string{"linux/amd64"}}, true); err != nil {
		t.Errorf("--force 覆盖失败: %v", err)
	}
	if _, err := removeCustomTool("npm-token"); err == nil {
		t.Error("内置工具不该能删")
	}
	if _, err := removeCustomTool("my-tool"); err != nil {
		t.Errorf("删除失败: %v", err)
	}
	if _, err := loadCustomTools(); err != nil {
		t.Fatal(err)
	}
	tools, _ = allTools()
	for _, tl := range tools {
		if tl.Key == "my-tool" {
			t.Error("删掉后还在列表里")
		}
	}
}

func TestCustomKeyCannotShadowBuiltinOnAdd(t *testing.T) {
	withTempConfig(t)
	if _, err := addCustomTool(tool{Key: "npm-token", Repo: "someone/else", Platforms: []string{"linux/amd64"}}, true); err == nil {
		t.Error("不应该允许覆盖内置 key")
	}
}

func TestCustomConfigBroken(t *testing.T) {
	dir := withTempConfig(t)
	path := filepath.Join(dir, "hmck", "tools.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{oops"), 0o600); err != nil {
		t.Fatal(err)
	}
	tools, err := allTools()
	if err == nil {
		t.Error("坏配置应当报错")
	}
	if len(tools) != len(registry) {
		t.Errorf("坏配置时仍应返回内置工具，得到 %d 个", len(tools))
	}
}

func TestRunAddWithFakeRelease(t *testing.T) {
	withTempConfig(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"tag_name":"v1.0.0","assets":[
			{"name":"my-tool_darwin_arm64"},
			{"name":"my-tool_linux_amd64"},
			{"name":"my-tool_windows_amd64.exe"},
			{"name":"checksums.txt"}]}`)
	}))
	defer srv.Close()

	old := releaseAPIURL
	releaseAPIURL = func(repo string) string { return srv.URL + "/releases/latest" }
	defer func() { releaseAPIURL = old }()

	// 位置参数在前、带值的选项在后，也要能解析
	if code := runAdd([]string{"https://github.com/owner/my-tool", "--desc", "演示用"}); code != 0 {
		t.Fatalf("runAdd 退出码 = %d", code)
	}
	custom, err := loadCustomTools()
	if err != nil {
		t.Fatal(err)
	}
	if len(custom) != 1 {
		t.Fatalf("自定义工具 = %+v", custom)
	}
	got := custom[0]
	if got.Key != "my-tool" || got.Repo != "owner/my-tool" || !got.Checksums {
		t.Errorf("添加结果 = %+v", got)
	}
	if strings.Join(got.Platforms, ",") != "darwin/arm64,linux/amd64,windows/amd64" {
		t.Errorf("平台 = %v", got.Platforms)
	}
}

func TestRunRemoveCustom(t *testing.T) {
	withTempConfig(t)
	if _, err := addCustomTool(tool{Key: "tmp-tool", Repo: "owner/tmp-tool", Platforms: []string{"linux/amd64"}}, false); err != nil {
		t.Fatal(err)
	}
	if code := runRemove([]string{"tmp-tool"}); code != 0 {
		t.Fatalf("runRemove 退出码 = %d", code)
	}
	custom, err := loadCustomTools()
	if err != nil {
		t.Fatal(err)
	}
	if len(custom) != 0 {
		t.Errorf("删完还剩 %+v", custom)
	}
	if code := runRemove([]string{"kube-token"}); code == 0 {
		t.Error("删除内置工具应当失败")
	}
}
