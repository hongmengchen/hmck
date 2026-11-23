package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/rand"
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

// glab 这类工具：GitLab 发版，资产是 tar.gz / zip，版本号写进资产名
func glabLikeTool() tool {
	return tool{
		Key:       "glab",
		Name:      "glab",
		Host:      "gitlab",
		Repo:      "gitlab-org/cli",
		Binary:    "glab",
		Asset:     "glab_{ver}_{goos}_{goarch}.{archive}",
		Archive:   true,
		Checksums: true,
		Platforms: []string{"darwin/arm64", "windows/amd64"},
		OwnDir:    true,
	}
}

func TestGitLabAssetNames(t *testing.T) {
	gl := glabLikeTool()
	if got := assetName(gl, "darwin", "arm64", "v1.131.0"); got != "glab_1.131.0_darwin_arm64.tar.gz" {
		t.Errorf("darwin asset = %q", got)
	}
	if got := assetName(gl, "windows", "amd64", "1.131.0"); got != "glab_1.131.0_windows_amd64.zip" {
		t.Errorf("windows asset = %q", got)
	}
	// 不写模板时还是内置工具的老约定
	npm := tool{Key: "npm-token", Binary: "npm-token"}
	if got := assetName(npm, "linux", "amd64", "0.5.0"); got != "npm-token_linux_amd64" {
		t.Errorf("默认模板 = %q", got)
	}
}

func TestGitLabLatestTag(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/permalink":
			w.Header().Set("Location", "/gitlab-org/cli/-/releases/v1.131.0")
			w.WriteHeader(http.StatusTemporaryRedirect)
		case "/permalink-missing":
			w.WriteHeader(http.StatusNotFound)
		case "/releases":
			fmt.Fprint(w, `[{"tag_name":"v1.130.0"},{"tag_name":"v1.129.0"}]`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	oldLatest, oldAPI := gitlabLatestURL, gitlabReleasesAPIURL
	defer func() { gitlabLatestURL, gitlabReleasesAPIURL = oldLatest, oldAPI }()

	gl := glabLikeTool()
	gitlabLatestURL = func(string) string { return srv.URL + "/permalink" }
	if tag, err := latestTag(gl); err != nil || tag != "v1.131.0" {
		t.Fatalf("permalink: tag=%q err=%v", tag, err)
	}

	gitlabLatestURL = func(string) string { return srv.URL + "/permalink-missing" }
	gitlabReleasesAPIURL = func(string) string { return srv.URL + "/releases" }
	if tag, err := latestTag(gl); err != nil || tag != "v1.130.0" {
		t.Fatalf("api fallback: tag=%q err=%v", tag, err)
	}
}

// 带版本号的 GitLab 资产地址：/<repo>/-/releases/<tag>/downloads/<asset>
func TestGitLabAssetURL(t *testing.T) {
	old := gitlabAssetURL
	defer func() { gitlabAssetURL = old }()
	var got string
	gitlabAssetURL = func(repo, tag, asset string) string {
		got = repo + "|" + tag + "|" + asset
		return "http://example.invalid/" + asset
	}
	tl := glabLikeTool()
	if u := releaseAssetURLFor(tl, "v1.131.0", "glab_1.131.0_darwin_arm64.tar.gz"); !strings.Contains(u, "glab_1.131.0_darwin_arm64.tar.gz") {
		t.Errorf("url = %q", u)
	}
	if got != "gitlab-org/cli|v1.131.0|glab_1.131.0_darwin_arm64.tar.gz" {
		t.Errorf("参数 = %q", got)
	}
}

func makeTarGz(t *testing.T, entries map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, data := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func makeZip(t *testing.T, entries map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// 从 tar.gz 里取出 bin/glab，校验的是压缩包本身
func TestFetchToolFromTarGz(t *testing.T) {
	// 压缩后的资产也要超过 downloadAsset 的最小长度，所以用随机内容
	payload := make([]byte, 220_000)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	archive := makeTarGz(t, map[string][]byte{
		"LICENSE":   []byte("license"),
		"bin/glab":  payload,
		"README.md": []byte("readme"),
		"bin/other": []byte("other"),
	})
	sum := sha256.Sum256(archive)
	asset := assetName(glabLikeTool(), runtime.GOOS, runtime.GOARCH, "9.9.7")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/checksums.txt"):
			fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), asset)
		case strings.HasSuffix(r.URL.Path, "/"+asset):
			w.Write(archive)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	old := gitlabAssetURL
	gitlabAssetURL = func(repo, tag, asset string) string { return srv.URL + "/download/" + tag + "/" + asset }
	defer func() { gitlabAssetURL = old }()

	// 测试机器不一定支持 darwin/arm64，用本机平台跑真实解压流程
	tl := glabLikeTool()
	tl.Platforms = []string{runtime.GOOS + "/" + runtime.GOARCH}

	st := toolState{Tool: tl, Latest: "9.9.7"}
	got, ver, err := fetchTool(st, nil)
	if err != nil {
		t.Fatalf("fetchTool: %v", err)
	}
	if ver != "9.9.7" {
		t.Errorf("version = %q", ver)
	}
	content, err := os.ReadFile(got)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(content, payload) {
		t.Errorf("解出来的二进制不对，长度 %d", len(content))
	}
	if fi, _ := os.Stat(got); fi.Mode().Perm()&0o100 == 0 {
		t.Errorf("缺少可执行位: %v", fi.Mode())
	}
}

func TestExtractEntryZip(t *testing.T) {
	dir := t.TempDir()
	payload := bytes.Repeat([]byte("ZIPBIN"), 1000)
	archive := makeZip(t, map[string][]byte{
		"bin/glab.exe": payload,
	})
	p := filepath.Join(dir, "glab_1.0.0_windows_amd64.zip")
	if err := os.WriteFile(p, archive, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := extractEntry(p, dir, "bin/glab.exe")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(out)
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Error("zip 里的内容不对")
	}
}

// 找不到指定路径时退到任意同名文件；都没有就报错
func TestExtractBinaryFallback(t *testing.T) {
	dir := t.TempDir()
	payload := bytes.Repeat([]byte("BIN"), 1000)
	p := filepath.Join(dir, "tool_1.0.0_linux_amd64.tar.gz")
	if err := os.WriteFile(p, makeTarGz(t, map[string][]byte{"glab": payload}), 0o644); err != nil {
		t.Fatal(err)
	}
	tl := tool{Key: "glab", Binary: "glab", Archive: true}
	out, err := extractBinary(tl, p, dir)
	if err != nil {
		t.Fatalf("应当退到同名文件: %v", err)
	}
	os.Remove(out)

	empty := filepath.Join(dir, "empty.tar.gz")
	if err := os.WriteFile(empty, makeTarGz(t, map[string][]byte{"README": []byte("x")}), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := extractBinary(tl, empty, dir); err == nil {
		t.Error("找不到二进制应当报错")
	}
}

// PATH 上别人装的同名程序要能列出来，但不算 hmck 管的那一份
func TestForeignCopies(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH 分隔符和 .exe 在这里不相关")
	}
	managedDir, otherDir, linkDir := t.TempDir(), t.TempDir(), t.TempDir()
	tl := tool{Key: "glab", Binary: "glab", OwnDir: true}
	name := binaryName(tl, runtime.GOOS)
	for _, d := range []string{managedDir, otherDir} {
		if err := os.WriteFile(filepath.Join(d, name), []byte("bin"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// PATH 里同一个文件经软链接出现两次，只算一份
	os.Symlink(filepath.Join(otherDir, name), filepath.Join(linkDir, name))

	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", strings.Join([]string{managedDir, linkDir, otherDir}, string(os.PathListSeparator)))
	defer os.Setenv("PATH", oldPath)

	managed := filepath.Join(managedDir, name)
	got := foreignCopies(tl, managed)
	if len(got) != 1 || got[0] != realPath(filepath.Join(otherDir, name)) {
		t.Errorf("foreignCopies = %v", got)
	}
}

// OwnDir 的工具不去 PATH 上认领别人的副本
func TestFindToolOwnDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("用默认安装目录行为验证即可")
	}
	dir := t.TempDir()
	tl := tool{Key: "glab", Binary: "glab", OwnDir: true}
	name := binaryName(tl, runtime.GOOS)
	if err := os.WriteFile(filepath.Join(dir, name), []byte("foreign"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	if p, ok := findTool(tl); ok && filepath.Dir(p) == dir {
		t.Errorf("OwnDir 不该去 PATH 认领 %q", p)
	}
}
