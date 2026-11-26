package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestSyncEncryptDecrypt(t *testing.T) {
	key := bytes.Repeat([]byte{0x2a}, syncKeyLen)
	salt := bytes.Repeat([]byte{0x11}, syncSaltLen)
	plain := []byte(`{"version":1,"files":[{"name":"home:.argocd/token"}]}`)

	raw, err := encryptSyncPayload(key, plain, salt, 100000)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if bytes.Contains(raw, []byte("argocd")) {
		t.Fatal("密文里不该出现明文路径")
	}
	got, err := decryptSyncPayload(key, raw)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("解密结果不一致: %s", got)
	}

	wrong := bytes.Repeat([]byte{0x2b}, syncKeyLen)
	if _, err := decryptSyncPayload(wrong, raw); err == nil {
		t.Fatal("换一把钥匙应该解不开")
	}
	tampered := append([]byte(nil), raw...)
	tampered[len(tampered)-4] ^= 0xff
	if _, err := decryptSyncPayload(key, tampered); err == nil {
		t.Fatal("改过的密文应该解不开")
	}
}

func TestDeriveSyncKeyFromEnvelope(t *testing.T) {
	salt := bytes.Repeat([]byte{0x33}, syncSaltLen)
	key, err := deriveSyncKey("hunter2", salt, 100000)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := encryptSyncPayload(key, []byte("hello"), salt, 100000)
	if err != nil {
		t.Fatal(err)
	}
	gotKey, plain, err := decryptWithPassphrase("hunter2", raw)
	if err != nil {
		t.Fatalf("从密码解密: %v", err)
	}
	if !bytes.Equal(gotKey, key) || string(plain) != "hello" {
		t.Fatalf("派生的钥匙或明文不对: %x %q", gotKey, plain)
	}
	if _, _, err := decryptWithPassphrase("wrong", raw); err == nil {
		t.Fatal("密码不对应该失败")
	}
}

func TestSyncNamePathRejectsEscapes(t *testing.T) {
	isolateConfig(t)
	home := t.TempDir()
	oldHome := userHomeDir
	userHomeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { userHomeDir = oldHome })

	for _, name := range []string{
		"config:../../etc/passwd",
		"home:../outside",
		"home:/etc/passwd",
		"other:file",
		"config:",
	} {
		if _, err := syncNamePath(name); err == nil {
			t.Errorf("%q 应该被拒绝", name)
		}
	}
	if got, err := syncNamePath("home:.argocd/token"); err != nil || got != filepath.Join(home, ".argocd", "token") {
		t.Errorf("home 路径展开不对: %q %v", got, err)
	}
}

func TestNormalizeSyncPath(t *testing.T) {
	isolateConfig(t)
	home := t.TempDir()
	oldHome := userHomeDir
	userHomeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { userHomeDir = oldHome })

	cases := map[string]string{
		"~/.npmrc":                "home:.npmrc",
		"config:hmck/config.json": "config:hmck/config.json",
		filepath.Join(home, ".x"): "home:.x",
	}
	for in, want := range cases {
		got, err := normalizeSyncPath(in)
		if err != nil {
			t.Errorf("normalize(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("normalize(%q) = %q, want %q", in, got, want)
		}
	}
	if _, err := normalizeSyncPath("/etc/hosts"); err == nil {
		t.Error("家目录和配置目录之外的路径应该拒绝")
	}
}

// 路径字段推到远端时要变成随机器走的写法，拉下来再变回本机路径：
// Windows 上写的 ~\.argocd\token 到了 macOS 仍然落到同一个文件。
func TestPortableSyncDataRoundTrip(t *testing.T) {
	isolateConfig(t)
	home := t.TempDir()
	oldHome := userHomeDir
	userHomeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { userHomeDir = oldHome })

	argoName := "config:argo-token/config.json"
	argoLocal := []byte("{\"server\": \"https://argocd.example.com\", \"token_file\": \"~\\\\.argocd\\\\token\", \"grpc_web\": true}\n")

	up, err := portableSyncData(argoName, argoLocal, syncToRemote)
	if err != nil {
		t.Fatalf("推向远端: %v", err)
	}
	if !strings.Contains(string(up), `"home:.argocd/token"`) {
		t.Errorf("token_file 应变成 home: 写法，得到 %s", up)
	}
	if strings.Contains(string(up), `\\`) {
		t.Errorf("推向远端的内容不应再有反斜杠路径: %s", up)
	}

	down, err := portableSyncData(argoName, up, syncFromRemote)
	if err != nil {
		t.Fatalf("拉回本机: %v", err)
	}
	wantPath := filepath.ToSlash(filepath.Join(home, ".argocd", "token"))
	if !strings.Contains(string(down), `"`+wantPath+`"`) {
		t.Errorf("token_file 应展开成本机路径 %q，得到 %s", wantPath, down)
	}

	// npm-token 的 file 是写配置那台机器的绝对路径；本机能对上的存 home:，
	// 对不上（比如另一台 Windows 的 C:\Users\…）就留空，让它回到默认 ~/.npmrc。
	npmName := "config:npm-token/config.json"
	npmLocal := []byte(`{"auto_refresh": false, "file": "` +
		strings.ReplaceAll(filepath.Join(home, ".npmrc"), `\`, `\\`) +
		`", "updated_at": "2026-11-23T12:16:01+08:00"}`)
	up, err = portableSyncData(npmName, npmLocal, syncToRemote)
	if err != nil {
		t.Fatalf("npm 推向远端: %v", err)
	}
	if !strings.Contains(string(up), `"home:.npmrc"`) {
		t.Errorf("file 应变成 home:.npmrc，得到 %s", up)
	}
	if strings.Contains(string(up), "updated_at") {
		t.Errorf("本机时间戳不该跟着同步: %s", up)
	}

	foreign, err := portableSyncData(npmName, []byte(`{"file": "C:\\Users\\someone-else\\.npmrc"}`), syncToRemote)
	if err != nil {
		t.Fatalf("外来绝对路径: %v", err)
	}
	if !strings.Contains(string(foreign), `"file": ""`) {
		t.Errorf("换机器必错的路径应留空，得到 %s", foreign)
	}

	// 没有规则的文件原样返回，一个字节都不改。
	raw := []byte("not json at all")
	if got, err := portableSyncData("config:kube-token/config.yaml", raw, syncToRemote); err != nil || string(got) != string(raw) {
		t.Errorf("没有规则的文件不该改: %q %v", got, err)
	}
	if got, err := portableSyncData(argoName, raw, syncToRemote); err != nil || string(got) != string(raw) {
		t.Errorf("不是 JSON 的文件不该改: %q %v", got, err)
	}
}

func TestMergeSyncFilesKeepsRemoteOnly(t *testing.T) {
	local := []syncFile{{Name: "home:a", Data: []byte("local")}}
	remote := []syncFile{
		{Name: "home:a", Data: []byte("remote")},
		{Name: "home:b", Data: []byte("only-remote")},
	}
	got := mergeSyncFiles(local, remote)
	if len(got) != 2 {
		t.Fatalf("合并后应有 2 个文件: %+v", got)
	}
	if string(got[0].Data) != "local" {
		t.Errorf("同名文件应以本机为准，得到 %q", got[0].Data)
	}
	if got[1].Name != "home:b" || string(got[1].Data) != "only-remote" {
		t.Errorf("远端独有的文件应保留: %+v", got[1])
	}
}

// ~/.argocd/env 是从配置生成的本机文件，老版本把它推上去了；升级后不该
// 继续在设备之间传，也不该在 pull 时盖掉本机那份。
func TestSyncDropsRetiredEnvFile(t *testing.T) {
	isolateConfig(t)
	retired := syncFile{Name: "home:.argocd/env", Data: []byte("export ARGOCD_SERVER='x'\n")}

	got := mergeSyncFiles([]syncFile{{Name: "home:.argocd/token", Data: []byte("t")}}, []syncFile{retired})
	for _, f := range got {
		if f.Name == retired.Name {
			t.Fatalf("推送时不该再把 %s 带回去: %+v", retired.Name, got)
		}
	}

	st, err := loadSyncState()
	if err != nil {
		t.Fatal(err)
	}
	p, err := filterSyncPayloadForDevice(syncPayload{Files: []syncFile{retired, {Name: "home:.argocd/token"}}}, st)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Files) != 1 || p.Files[0].Name != "home:.argocd/token" {
		t.Errorf("pull 时不该写 %s: %+v", retired.Name, p.Files)
	}
}

// fakeGitHub 是只够 hmck 用的最小 GitHub API：认人、建仓库、读写一个文件。
type fakeGitHub struct {
	mu    sync.Mutex
	login string
	repos map[string]bool
	files map[string][]byte // "owner/repo/文件名"
	puts  int
}

func newFakeGitHub() *fakeGitHub {
	return &fakeGitHub{login: "alice", repos: map[string]bool{}, files: map[string][]byte{}}
}

func fileSHA(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer test-token" {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/")
	switch {
	case path == "user" && r.Method == http.MethodGet:
		_ = json.NewEncoder(w).Encode(map[string]string{"login": f.login})
	case strings.HasPrefix(path, "user/repos") && r.Method == http.MethodPost:
		var body struct {
			Name     string `json:"name"`
			Private  bool   `json:"private"`
			AutoInit bool   `json:"auto_init"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Name == "" || !body.Private || !body.AutoInit {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"message":"private+auto_init required"}`))
			return
		}
		f.repos[f.login+"/"+body.Name] = true
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"name":"` + body.Name + `","private":true}`))
	case strings.HasPrefix(path, "repos/"):
		rest := strings.TrimPrefix(path, "repos/")
		parts := strings.SplitN(rest, "/", 3)
		if len(parts) < 2 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		repo := parts[0] + "/" + parts[1]
		if !f.repos[repo] {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
			return
		}
		if len(parts) == 2 {
			_, _ = w.Write([]byte(`{"private":true}`))
			return
		}
		const contents = "contents/"
		if !strings.HasPrefix(parts[2], contents) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		file := strings.TrimPrefix(parts[2], contents)
		key := repo + "/" + file
		switch r.Method {
		case http.MethodGet:
			data, ok := f.files[key]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"message":"Not Found"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{
				"content": base64.StdEncoding.EncodeToString(data),
				"sha":     fileSHA(data),
			})
		case http.MethodPut:
			var body struct {
				Content string `json:"content"`
				SHA     string `json:"sha"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			data, err := base64.StdEncoding.DecodeString(body.Content)
			if err != nil {
				w.WriteHeader(http.StatusUnprocessableEntity)
				return
			}
			if old, ok := f.files[key]; ok && body.SHA != fileSHA(old) {
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"message":"sha does not match"}`))
				return
			}
			f.files[key] = data
			f.puts++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"content": map[string]string{"sha": fileSHA(data)},
			})
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func setupSyncTest(t *testing.T) (*fakeGitHub, string, string) {
	t.Helper()
	isolateConfig(t)
	home := t.TempDir()
	oldHome := userHomeDir
	userHomeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { userHomeDir = oldHome })

	fake := newFakeGitHub()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	t.Setenv("HMCK_GITHUB_API", srv.URL)

	oldToken := syncGHToken
	syncGHToken = func() (string, error) { return "test-token", nil }
	t.Cleanup(func() { syncGHToken = oldToken })

	cfgDir, err := userConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(cfgDir, "hmck"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		"config.json": "{\n  \"auto_download\": true\n}\n",
		"tools.json":  "{\n  \"tools\": []\n}\n",
	} {
		if err := os.WriteFile(filepath.Join(cfgDir, "hmck", name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return fake, home, cfgDir
}

// payloadFiles 解开同步数据，按逻辑名取文件内容。
func payloadFiles(t *testing.T, plain []byte) map[string]string {
	t.Helper()
	var p syncPayload
	if err := json.Unmarshal(plain, &p); err != nil {
		t.Fatalf("同步数据不是合法 JSON: %v", err)
	}
	out := map[string]string{}
	for _, f := range p.Files {
		out[f.Name] = string(f.Data)
	}
	return out
}

func writeSyncTestState(t *testing.T, pass string) ([]byte, syncState) {
	t.Helper()
	key, err := deriveSyncKey(pass, bytes.Repeat([]byte{0x44}, syncSaltLen), 100000)
	if err != nil {
		t.Fatal(err)
	}
	st := syncState{
		Repo: "alice/" + syncRepoName,
		Key:  base64.StdEncoding.EncodeToString(key),
		Salt: bytes.Repeat([]byte{0x44}, syncSaltLen),
		Iter: 100000,
	}
	if _, err := saveSyncState(st); err != nil {
		t.Fatal(err)
	}
	return key, st
}

func TestSyncPushPullRoundTrip(t *testing.T) {
	fake, home, _ := setupSyncTest(t)
	tokenPath := filepath.Join(home, ".argocd", "token")
	if err := os.MkdirAll(filepath.Dir(tokenPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenPath, []byte("secret-token-v1"), 0o600); err != nil {
		t.Fatal(err)
	}

	key, _ := writeSyncTestState(t, "correct horse battery staple")

	msg, err := syncPush(syncOptions{})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if !strings.Contains(msg, "已推送") {
		t.Errorf("push 提示不对: %s", msg)
	}

	raw := fake.files["alice/"+syncRepoName+"/"+syncRemoteFile]
	if len(raw) == 0 {
		t.Fatal("仓库里没有同步文件")
	}
	if bytes.Contains(raw, []byte("secret-token-v1")) {
		t.Fatal("远端出现了明文 token")
	}
	if !bytes.Contains(raw, []byte(`"kdf"`)) {
		t.Fatalf("远端不是加密信封: %s", raw)
	}

	// 删掉本机 token，pull 应该把它原样写回来（0600）
	if err := os.Remove(tokenPath); err != nil {
		t.Fatal(err)
	}
	msg, err = syncPull(syncOptions{})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if !strings.Contains(msg, "写入 1 个文件") {
		t.Errorf("pull 提示不对: %s", msg)
	}
	got, err := os.ReadFile(tokenPath)
	if err != nil || string(got) != "secret-token-v1" {
		t.Fatalf("token 没恢复: %q %v", got, err)
	}
	if info, err := os.Stat(tokenPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("pull 下来的文件应该是 0600: %v %v", info, err)
	}

	// 改本机 token 再推，远端应该跟着更新
	if err := os.WriteFile(tokenPath, []byte("secret-token-v2"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := syncPush(syncOptions{}); err != nil {
		t.Fatalf("第二次 push: %v", err)
	}
	plain, err := decryptSyncPayload(key, fake.files["alice/"+syncRepoName+"/"+syncRemoteFile])
	if err != nil {
		t.Fatal(err)
	}
	if got := payloadFiles(t, plain)["home:.argocd/token"]; got != "secret-token-v2" {
		t.Fatalf("远端没有更新成 v2: %q", got)
	}

	// 本机没有变化时应该跳过，不再产生一次提交
	puts := fake.puts
	if msg, err := syncPush(syncOptions{}); err != nil || !strings.Contains(msg, "不用推") {
		t.Fatalf("没变化应跳过: %q %v", msg, err)
	}
	if fake.puts != puts {
		t.Errorf("没变化却推送了 %d 次", fake.puts-puts)
	}
}

func TestSyncDryRunShowsContentWithoutMutating(t *testing.T) {
	_, home, _ := setupSyncTest(t)
	logicalName := "home:.hmck-preview.yaml"
	filePath := filepath.Join(home, ".hmck-preview.yaml")
	if err := os.WriteFile(filePath, []byte("endpoint: remote.example\nmode: production\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeSyncTestState(t, "preview-passphrase")
	st, err := loadSyncState()
	if err != nil {
		t.Fatal(err)
	}
	st.ExtraPaths = []string{logicalName}
	if _, err := saveSyncState(st); err != nil {
		t.Fatal(err)
	}
	if _, err := syncPush(syncOptions{}); err != nil {
		t.Fatalf("push: %v", err)
	}

	local := []byte("endpoint: local.example\nmode: development\n")
	if err := os.WriteFile(filePath, local, 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := loadSyncState()
	if err != nil {
		t.Fatal(err)
	}

	var preview []syncPreviewFile
	msg, err := syncPullWithPreview(syncOptions{dryRun: true}, &preview)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if !strings.Contains(msg, "普通 pull 会拒绝覆盖") {
		t.Fatalf("预览应提示普通 pull 会因本机冲突拒绝: %s", msg)
	}
	if len(preview) == 0 || !strings.Contains(renderSyncPreview(preview), "- endpoint: local.example") || !strings.Contains(renderSyncPreview(preview), "+ endpoint: remote.example") {
		t.Fatal("预览没有展示本机与远端的实际内容差异")
	}
	got, err := os.ReadFile(filePath)
	if err != nil || !bytes.Equal(got, local) {
		t.Fatal("dry-run 改写了本机文件")
	}
	after, err := loadSyncState()
	if err != nil {
		t.Fatal(err)
	}
	if after.LastPull != before.LastPull || after.LastHash != before.LastHash || after.LastSHA != before.LastSHA {
		t.Fatal("dry-run 改变了本机同步状态")
	}
}

func TestSyncPreviewHidesCredentialContent(t *testing.T) {
	secrets := []string{"secret-token-value", "password-value-123"}
	files := []syncPreviewFile{
		makeSyncPreviewFile("home:.argocd/token", []byte(secrets[0]), []byte("different-secret-token"), true),
		makeSyncPreviewFile("home:.config/service/settings.yaml", []byte("password: "+secrets[1]+"\n"), []byte("password: another-password\n"), true),
	}
	if !files[0].Sensitive || !files[1].Sensitive {
		t.Fatal("检测到凭据的文件应整体标记为隐藏")
	}
	output := renderSyncPreview(files)
	for _, secret := range secrets {
		if strings.Contains(output, secret) {
			t.Fatal("预览泄露了凭据内容")
		}
	}
}

func TestSyncPreviewShowsTextDiffAndEscapesControls(t *testing.T) {
	f := makeSyncPreviewFile("home:.config/example.yaml", []byte("name: old\nkeep: yes\n"), []byte("name: new\nkeep: yes\n"), true)
	output := renderSyncPreview([]syncPreviewFile{f})
	if !strings.Contains(output, "- name: old") || !strings.Contains(output, "+ name: new") || strings.Contains(output, "keep: yes") {
		t.Fatal("预览应只展示实际变更行")
	}
	f = makeSyncPreviewFile("home:.config/example.yaml", []byte("name: old\x1b[31m\n"), []byte("name: new\n"), true)
	if strings.Contains(renderSyncPreview([]syncPreviewFile{f}), "\x1b") {
		t.Fatal("预览不应透传终端控制字符")
	}
}

func TestSyncPushKeepsRemoteOnlyFiles(t *testing.T) {
	fake, home, _ := setupSyncTest(t)
	tokenPath := filepath.Join(home, ".argocd", "token")
	if err := os.MkdirAll(filepath.Dir(tokenPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenPath, []byte("remote-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	key, _ := writeSyncTestState(t, "passphrase")
	if _, err := syncPush(syncOptions{}); err != nil {
		t.Fatalf("push: %v", err)
	}

	// 模拟另一台机器上没有这个 token：本机删掉后再推，远端不能丢
	if err := os.Remove(tokenPath); err != nil {
		t.Fatal(err)
	}
	if _, err := syncPush(syncOptions{}); err != nil {
		t.Fatalf("第二次 push: %v", err)
	}
	plain, err := decryptSyncPayload(key, fake.files["alice/"+syncRepoName+"/"+syncRemoteFile])
	if err != nil {
		t.Fatal(err)
	}
	if got := payloadFiles(t, plain)["home:.argocd/token"]; got != "remote-token" {
		t.Fatalf("本机没有的文件应继续留在远端，实际 %q", got)
	}
}

func TestSyncPushDetectsOtherDevice(t *testing.T) {
	fake, home, _ := setupSyncTest(t)
	if err := os.MkdirAll(filepath.Join(home, ".argocd"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".argocd", "token"), []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeSyncTestState(t, "passphrase")
	if _, err := syncPush(syncOptions{}); err != nil {
		t.Fatalf("push: %v", err)
	}

	// 另一台设备改了远端（SHA 变了）
	key := "alice/" + syncRepoName + "/" + syncRemoteFile
	fake.mu.Lock()
	fake.files[key] = []byte(`{"v":1}`)
	fake.mu.Unlock()

	if _, err := syncPush(syncOptions{}); err == nil || !strings.Contains(err.Error(), "别的设备") {
		t.Fatalf("应提示先 pull: %v", err)
	}
	if _, err := syncPush(syncOptions{force: true}); err != nil {
		t.Fatalf("--force 应该能覆盖: %v", err)
	}
}

func TestSyncPullWithPassphraseOnNewDevice(t *testing.T) {
	_, home, _ := setupSyncTest(t)
	mustWrite := func(path, data string) {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite(filepath.Join(home, ".argocd", "token"), "from-device-a")
	writeSyncTestState(t, "shared-pass")
	if _, err := syncPush(syncOptions{}); err != nil {
		t.Fatalf("push: %v", err)
	}

	// 新设备：什么都不记得，只有密码
	if err := os.Remove(filepath.Join(home, ".argocd", "token")); err != nil {
		t.Fatal(err)
	}
	if _, err := saveSyncState(syncState{}); err != nil {
		t.Fatal(err)
	}

	oldStdin := os.Stdin
	f, err := os.CreateTemp(t.TempDir(), "pass")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("wrong-pass\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	os.Stdin = f
	t.Cleanup(func() { os.Stdin = oldStdin })

	if _, err := syncPull(syncOptions{passStdin: true}); err == nil || !strings.Contains(err.Error(), "密码不对") {
		t.Fatalf("错密码应报错: %v", err)
	}

	if _, err := f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(0); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("shared-pass\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := syncPull(syncOptions{passStdin: true}); err != nil {
		t.Fatalf("正确密码 pull: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(home, ".argocd", "token"))
	if err != nil || string(got) != "from-device-a" {
		t.Fatalf("新设备没拿到 token: %q %v", got, err)
	}
	// 钥匙已经记下来，后面不需要再输密码
	st, err := loadSyncState()
	if err != nil || st.Key == "" || !syncEnabled(st) {
		t.Fatalf("pull 后应记住密钥: %+v %v", st, err)
	}
}

func TestSyncPathListDoesNotIncludeState(t *testing.T) {
	setupSyncTest(t)
	st, err := loadSyncState()
	if err != nil {
		t.Fatal(err)
	}
	st.ExtraPaths = []string{"home:.zshrc", syncStateName}
	names, err := syncNames(st)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if n == syncStateName {
			t.Fatal("sync.json 不该出现在同步清单里")
		}
		if n == "home:.zshrc" {
			return
		}
	}
	t.Fatal("手工加的路径没出现在清单里")
}

// 设备表按 ID 合并：时间取较晚的，名字用新的，结果排序稳定
func TestMergeSyncDevices(t *testing.T) {
	a := []syncDevice{
		{ID: "a1", Name: "mac-mini", OS: "darwin", LastPush: "2026-11-24T01:00:00Z"},
		{ID: "b2", Name: "win-11", LastPull: "2026-11-23T02:00:00Z"},
	}
	b := []syncDevice{
		{ID: "a1", Name: "mac-mini-2", LastPush: "2026-11-20T01:00:00Z", LastPull: "2026-11-24T02:00:00Z"},
		{ID: "c3", Name: "ci-box"},
	}
	got := mergeSyncDevices(a, b)
	if len(got) != 3 {
		t.Fatalf("合并后应该 3 台: %+v", got)
	}
	if got[0].ID != "a1" || got[1].ID != "b2" || got[2].ID != "c3" {
		t.Fatalf("应按 ID 排序: %+v", got)
	}
	if got[0].Name != "mac-mini-2" {
		t.Errorf("名字应该用新的: %+v", got[0])
	}
	if got[0].LastPush != "2026-11-24T01:00:00Z" || got[0].LastPull != "2026-11-24T02:00:00Z" {
		t.Errorf("时间应该各取较晚的: %+v", got[0])
	}
	// 没有 ID 的登记项丢掉，别在列表里冒出一台无名设备
	if got := mergeSyncDevices([]syncDevice{{Name: "ghost"}}); len(got) != 0 {
		t.Errorf("空 ID 不该进设备表: %+v", got)
	}
}

func TestUpsertSyncDevice(t *testing.T) {
	list := []syncDevice{{ID: "a1", Name: "old"}, {ID: "b2", Name: "win-11"}}
	got := upsertSyncDevice(list, syncDevice{ID: "a1", Name: "new"})
	if len(got) != 2 || got[0].ID != "a1" || got[0].Name != "new" {
		t.Fatalf("同 ID 应该替换: %+v", got)
	}
	got = upsertSyncDevice(got, syncDevice{ID: "c3", Name: "ci-box"})
	if len(got) != 3 || got[2].ID != "c3" {
		t.Fatalf("新设备应该插到末尾: %+v", got)
	}
}

// 两台机器交替推送/拉取，设备表要互相看得到，ID 各是各的
func TestSyncDevicesAcrossMachines(t *testing.T) {
	fake, home, _ := setupSyncTest(t)
	tokenPath := filepath.Join(home, ".argocd", "token")
	if err := os.MkdirAll(filepath.Dir(tokenPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenPath, []byte("from-a"), 0o600); err != nil {
		t.Fatal(err)
	}
	key, _ := writeSyncTestState(t, "shared-pass")

	// 机器 A 第一次推送
	if _, err := syncPush(syncOptions{}); err != nil {
		t.Fatalf("A push: %v", err)
	}
	stA, err := loadSyncState()
	if err != nil {
		t.Fatal(err)
	}
	if stA.DeviceID == "" || len(stA.Devices) != 1 {
		t.Fatalf("A 推完应该有自己一条设备记录: %+v", stA)
	}
	devA := stA.Devices[0]
	if devA.LastPush == "" || devA.ID != stA.DeviceID {
		t.Fatalf("A 的记录不对: %+v", devA)
	}

	// 机器 B：什么都不记得，只带密码拉下来
	if _, err := saveSyncState(syncState{}); err != nil {
		t.Fatal(err)
	}
	if _, err := syncPull(syncOptions{pass: "shared-pass"}); err != nil {
		t.Fatalf("B pull: %v", err)
	}
	stB, err := loadSyncState()
	if err != nil {
		t.Fatal(err)
	}
	if stB.DeviceID == "" || stB.DeviceID == devA.ID {
		t.Fatalf("B 应该有自己的 ID: A=%q B=%q", devA.ID, stB.DeviceID)
	}
	if len(stB.Devices) != 2 {
		t.Fatalf("B 应该看到两台设备: %+v", stB.Devices)
	}
	if dev, ok := findSyncDevice(stB.Devices, devA.ID); !ok || dev.Name == "" {
		t.Fatalf("B 的设备表里没有 A: %+v", stB.Devices)
	}

	// B 改了配置推上去，远端要记着两台
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte("# b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stB.ExtraPaths = []string{"home:.zshrc"}
	if _, err := saveSyncState(stB); err != nil {
		t.Fatal(err)
	}
	if _, err := syncPush(syncOptions{}); err != nil {
		t.Fatalf("B push: %v", err)
	}
	plain, err := decryptSyncPayload(key, fake.files["alice/"+syncRepoName+"/"+syncRemoteFile])
	if err != nil {
		t.Fatal(err)
	}
	var p syncPayload
	if err := json.Unmarshal(plain, &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Devices) != 2 {
		t.Fatalf("远端应该有 A、B 两台: %+v", p.Devices)
	}

	// 回到 A：刷新设备列表就能看到 B，不用等下一次 pull
	if _, err := saveSyncState(stA); err != nil {
		t.Fatal(err)
	}
	if msg, err := refreshSyncDevices(); err != nil || !strings.Contains(msg, "设备列表已更新：2 台") {
		t.Fatalf("A 刷新设备: %q %v", msg, err)
	}
	stA, err = loadSyncState()
	if err != nil {
		t.Fatal(err)
	}
	if dev, ok := findSyncDevice(stA.Devices, stB.DeviceID); !ok || dev.LastPush == "" {
		t.Fatalf("A 没看到 B 的推送记录: %+v", stA.Devices)
	}
}

// sync status --json 里要有设备表，agent 直接读
func TestSyncStatusJSONHasDevices(t *testing.T) {
	st := syncState{
		Repo:     "alice/" + syncRepoName,
		Key:      "k",
		DeviceID: "a1",
		Devices:  []syncDevice{{ID: "a1", Name: "mac-mini", LastPush: "2026-11-24T01:00:00Z"}},
	}
	data := syncStatusJSON(st, []string{"home:.zshrc"}, "已安装")
	devices, ok := data["devices"].([]syncDevice)
	if !ok || len(devices) != 1 || devices[0].Name != "mac-mini" {
		t.Fatalf("JSON 里没有设备表: %+v", data["devices"])
	}
	if data["device_id"] != "a1" {
		t.Errorf("device_id 不对: %v", data["device_id"])
	}
}

// hmck sync --json 走的是 status：输出里有设备表字段
func TestSyncFlagsGoToStatus(t *testing.T) {
	isolateConfig(t)
	out, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = out
	code := runSync([]string{"--json"})
	os.Stdout = old
	if code != 0 {
		t.Fatalf("hmck sync --json 退出码 %d", code)
	}
	if _, err := out.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(out)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("输出不是 JSON: %v\n%s", err, data)
	}
	if _, ok := got["devices"]; !ok {
		t.Errorf("status JSON 里没有 devices: %s", data)
	}
}

func TestSyncToolOptOutFiltersManifestPaths(t *testing.T) {
	_, _, configDir := setupSyncTest(t)
	custom := toolsConfig{Tools: []tool{{
		Key: "custom-sync", Repo: "owner/custom-sync",
		SyncPaths: []string{"home:.custom-sync/settings.json"},
	}}}
	data, err := json.Marshal(custom)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "hmck", "tools.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	tools, err := syncableTools()
	if err != nil {
		t.Fatal(err)
	}
	foundCustom := false
	for _, tool := range tools {
		if tool.Key == "custom-sync" {
			foundCustom = true
		}
	}
	if !foundCustom {
		t.Fatal("声明 SyncPaths 的自定义工具没有进入可管理清单")
	}

	st := syncState{
		DisabledTools: []string{"ARGO-TOKEN", "custom-sync", "removed-tool"},
		ExtraPaths:    []string{"home:.argocd/token", "home:.custom-sync/settings.json"},
	}
	if syncToolEnabled(st, "argo-token") || !syncToolEnabled(st, "kube-token") {
		t.Fatal("逐工具开关应按 key 生效，未配置工具默认开启")
	}
	names, err := syncNames(st)
	if err != nil {
		t.Fatal(err)
	}
	for _, disabled := range []string{
		"config:argo-token/config.json", "home:.argocd/token", "home:.argocd/env",
		"home:.custom-sync/settings.json",
	} {
		for _, name := range names {
			if name == disabled {
				t.Errorf("关闭工具后路径仍在同步清单中: %s", disabled)
			}
		}
	}
	for _, enabled := range []string{"config:hmck/config.json", "config:hmck/tools.json", "config:kube-token/config.yaml"} {
		found := false
		for _, name := range names {
			found = found || name == enabled
		}
		if !found {
			t.Errorf("未关闭的同步路径被误删: %s", enabled)
		}
	}

	setSyncToolEnabled(&st, "custom-sync", true)
	if !syncToolEnabled(st, "custom-sync") || syncToolEnabled(st, "argo-token") {
		t.Fatal("重新开启自定义工具时不应影响其他工具设置")
	}
	if !containsString(st.DisabledTools, "removed-tool") {
		t.Fatal("暂时不可用的未知工具设置应保留")
	}
	if _, err := saveSyncState(st); err != nil {
		t.Fatal(err)
	}
	reloaded, err := loadSyncState()
	if err != nil {
		t.Fatal(err)
	}
	if !containsString(reloaded.DisabledTools, "removed-tool") || syncToolEnabled(reloaded, "argo-token") {
		t.Fatal("本机禁用设置没有可靠持久化")
	}
}

func TestDisabledToolPushKeepsExistingRemoteData(t *testing.T) {
	fake, home, configDir := setupSyncTest(t)
	tokenPath := filepath.Join(home, ".argocd", "token")
	configPath := filepath.Join(configDir, "argo-token", "config.json")
	for _, path := range []string{tokenPath, configPath} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(tokenPath, []byte("remote-token-v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("remote config v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	key, _ := writeSyncTestState(t, "preservation-passphrase")
	if _, err := syncPush(syncOptions{}); err != nil {
		t.Fatalf("初次推送失败: %v", err)
	}

	st, err := loadSyncState()
	if err != nil {
		t.Fatal(err)
	}
	st.DisabledTools = []string{"argo-token"}
	// 即使用户额外添加了相同路径，工具开关仍然优先。
	st.ExtraPaths = []string{"home:.argocd/token"}
	if _, err := saveSyncState(st); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenPath, []byte("local-token-v2"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := syncPush(syncOptions{}); err != nil {
		t.Fatalf("关闭工具后的推送失败: %v", err)
	}

	plain, err := decryptSyncPayload(key, fake.files["alice/"+syncRepoName+"/"+syncRemoteFile])
	if err != nil {
		t.Fatal(err)
	}
	files := payloadFiles(t, plain)
	if files["home:.argocd/token"] != "remote-token-v1" {
		t.Error("关闭的工具不应把本机新值推上去，也不应删除远端旧数据")
	}
	if files["config:argo-token/config.json"] != "remote config v1" {
		t.Error("关闭的工具配置应继续保留在远端")
	}
}

func TestDisabledToolPullSkipsConflictsPreviewAndApply(t *testing.T) {
	fake, home, _ := setupSyncTest(t)
	tokenPath := filepath.Join(home, ".argocd", "token")
	if err := os.MkdirAll(filepath.Dir(tokenPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenPath, []byte("remote-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	key, _ := writeSyncTestState(t, "pull-filter-passphrase")
	if _, err := syncPush(syncOptions{}); err != nil {
		t.Fatalf("初次推送失败: %v", err)
	}
	remoteKey := "alice/" + syncRepoName + "/" + syncRemoteFile
	remoteRaw := append([]byte(nil), fake.files[remoteKey]...)
	plain, err := decryptSyncPayload(key, remoteRaw)
	if err != nil {
		t.Fatal(err)
	}
	var remotePayload syncPayload
	if err := json.Unmarshal(plain, &remotePayload); err != nil {
		t.Fatal(err)
	}

	st, err := loadSyncState()
	if err != nil {
		t.Fatal(err)
	}
	st.DisabledTools = []string{"argo-token"}
	if _, err := saveSyncState(st); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenPath, []byte("local-token"), 0o600); err != nil {
		t.Fatal(err)
	}

	var preview []syncPreviewFile
	msg, err := syncPullWithPreview(syncOptions{dryRun: true}, &preview)
	if err != nil {
		t.Fatalf("禁用工具后的预览失败: %v", err)
	}
	if strings.Contains(msg, "普通 pull 会拒绝覆盖") {
		t.Fatalf("禁用路径不应触发冲突检查: %s", msg)
	}
	previewText := renderSyncPreview(preview)
	if strings.Contains(previewText, "home:.argocd/token") || strings.Contains(previewText, "remote-token") {
		t.Fatal("预览不应列出或显示禁用工具的远端文件")
	}
	local, err := os.ReadFile(tokenPath)
	if err != nil || string(local) != "local-token" {
		t.Fatal("dry-run 改动了禁用工具的本机文件")
	}

	if _, err := syncPull(syncOptions{}); err != nil {
		t.Fatalf("禁用路径不应阻止 pull: %v", err)
	}
	local, err = os.ReadFile(tokenPath)
	if err != nil || string(local) != "local-token" {
		t.Fatal("pull 覆盖或删除了本机禁用工具的文件")
	}
	after, err := loadSyncState()
	if err != nil {
		t.Fatal(err)
	}
	if after.LastSHA != fileSHA(remoteRaw) {
		t.Fatal("pull 应继续按未过滤的原始远端 payload 更新 SHA 历史")
	}
	expectedHash, err := payloadHash(remotePayload.Files, mergeSyncPaths(st.ExtraPaths, remotePayload.Paths))
	if err != nil {
		t.Fatal(err)
	}
	if after.LastHash != expectedHash {
		t.Fatal("pull 应继续按原始远端 payload 更新内容历史")
	}
	if !containsString(after.DisabledTools, "argo-token") {
		t.Fatal("pull 不应覆盖本机逐工具设置")
	}
}
