package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestIsNewer(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"0.2.0", "0.1.9", true},
		{"0.1.10", "0.1.9", true},
		{"v1.0.0", "0.9.9", true},
		{"0.1.0", "0.1.0", false},
		{"0.1.0", "0.1.1", false},
	}
	for _, c := range cases {
		if got := isNewer(c.a, c.b); got != c.want {
			t.Errorf("isNewer(%q,%q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestAssetNamesAndSupport(t *testing.T) {
	var npmTool tool
	for _, t0 := range registry {
		if t0.Key == "npm-token" {
			npmTool = t0
		}
	}
	if got := assetName(npmTool, "darwin", "arm64"); got != "npm-token_darwin_arm64" {
		t.Errorf("asset = %q", got)
	}
	if got := assetName(npmTool, "windows", "amd64"); got != "npm-token_windows_amd64.exe" {
		t.Errorf("windows asset = %q", got)
	}
	if got := binaryName(npmTool, "windows"); got != "npm-token.exe" {
		t.Errorf("binary = %q", got)
	}
	if !supports(npmTool, "windows", "arm64") {
		t.Error("npm-token 应当支持 windows/arm64")
	}

	var kube tool
	for _, t0 := range registry {
		if t0.Key == "kube-token" {
			kube = t0
		}
	}
	if supports(kube, "windows", "arm64") {
		t.Error("kube-token 没有 windows/arm64 发布")
	}
}

func TestToolByKey(t *testing.T) {
	for _, key := range []string{"npm-token", "npM-token", "npm"} {
		if got, ok := toolByKey(key); !ok || got.Key != "npm-token" {
			t.Errorf("toolByKey(%q) = %v, %v", key, got.Key, ok)
		}
	}
	if _, ok := toolByKey("nope"); ok {
		t.Error("不该认识 nope")
	}
}

func TestLatestTagRedirectAndFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases/latest":
			w.Header().Set("Location", "/o/r/releases/tag/v9.9.9")
			w.WriteHeader(http.StatusFound)
		case "/notfound":
			w.WriteHeader(http.StatusNotFound)
		case "/api/releases/latest":
			fmt.Fprint(w, `{"tag_name":"v8.8.8"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	oldPage, oldAPI := latestPageURL, releaseAPIURL
	defer func() { latestPageURL, releaseAPIURL = oldPage, oldAPI }()

	latestPageURL = func(string) string { return srv.URL + "/releases/latest" }
	if tag, err := latestTag("o/r"); err != nil || tag != "v9.9.9" {
		t.Fatalf("redirect: tag=%q err=%v", tag, err)
	}

	latestPageURL = func(string) string { return srv.URL + "/notfound" }
	releaseAPIURL = func(string) string { return srv.URL + "/api/releases/latest" }
	if tag, err := latestTag("o/r"); err != nil || tag != "v8.8.8" {
		t.Fatalf("api fallback: tag=%q err=%v", tag, err)
	}
}

func fakeReleaseServer(t *testing.T, payload []byte, checksum string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/checksums.txt"):
			fmt.Fprintf(w, "%s  %s\n", checksum, "fake_"+runtime.GOOS+"_"+runtime.GOARCH)
		case strings.Contains(r.URL.Path, "/download/"):
			w.Write(payload)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestInstallToolWithChecksum(t *testing.T) {
	payload := bytes.Repeat([]byte("BINARY"), 40_000) // > 100KB
	sum := sha256.Sum256(payload)
	srv := fakeReleaseServer(t, payload, hex.EncodeToString(sum[:]))
	defer srv.Close()

	old := releaseAssetURL
	releaseAssetURL = func(repo, tag, asset string) string { return srv.URL + "/download/" + tag + "/" + asset }
	defer func() { releaseAssetURL = old }()

	tl := tool{Key: "fake", Repo: "o/r", Binary: "fake",
		Platforms: []string{runtime.GOOS + "/" + runtime.GOARCH}, Checksums: true}
	dir := t.TempDir()
	target := filepath.Join(dir, binaryName(tl, runtime.GOOS))
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := installTool(toolState{Tool: tl, Path: target, Installed: "0.0.1", Latest: "9.9.9"})
	if err != nil {
		t.Fatalf("installTool: %v", err)
	}
	if got != "9.9.9" {
		t.Errorf("version = %q", got)
	}
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(content, payload) {
		t.Errorf("文件内容没被替换，长度 %d", len(content))
	}
	st, _ := os.Stat(target)
	if st.Mode().Perm()&0o100 == 0 {
		t.Errorf("缺少可执行位: %v", st.Mode())
	}
}

func TestInstallToolChecksumMismatch(t *testing.T) {
	payload := bytes.Repeat([]byte("BINARY"), 40_000)
	srv := fakeReleaseServer(t, payload, strings.Repeat("0", 64))
	defer srv.Close()

	old := releaseAssetURL
	releaseAssetURL = func(repo, tag, asset string) string { return srv.URL + "/download/" + tag + "/" + asset }
	defer func() { releaseAssetURL = old }()

	tl := tool{Key: "fake", Repo: "o/r", Binary: "fake",
		Platforms: []string{runtime.GOOS + "/" + runtime.GOARCH}, Checksums: true}
	dir := t.TempDir()
	target := filepath.Join(dir, binaryName(tl, runtime.GOOS))
	os.WriteFile(target, []byte("old"), 0o755)

	if _, err := installTool(toolState{Tool: tl, Path: target, Latest: "9.9.9"}); err == nil {
		t.Fatal("校验不匹配却成功了")
	}
	content, _ := os.ReadFile(target)
	if string(content) != "old" {
		t.Errorf("校验失败时不该动原文件: %q", content)
	}
}

func TestInstallToolUnsupportedPlatform(t *testing.T) {
	tl := tool{Key: "fake", Repo: "o/r", Binary: "fake", Platforms: []string{"plan9/mips"}}
	if _, err := installTool(toolState{Tool: tl, Latest: "1.0.0"}); err == nil {
		t.Error("不支持的平台应当报错")
	}
}
