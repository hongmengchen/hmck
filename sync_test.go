package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
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
