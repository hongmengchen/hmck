package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// 多端同步：把本机几个配置文件打包、用同步密码加密，放进一个私有 GitHub 仓库。
// GitHub 上只有密文；密码不落盘，落盘的是派生出来的 32 字节密钥（sync.json，0600）。
// 换机器时输入同一个密码，从远端取回密文解密即可。
const (
	syncFormat      = 1
	syncRemoteFile  = "hmck-sync.enc"
	syncRepoName    = "hmck-config"
	syncKDF         = "pbkdf2-sha256"
	syncKDFIter     = 600000
	syncKDFMinIter  = 100000
	syncKDFMaxIter  = 5000000
	syncKeyLen      = 32
	syncSaltLen     = 16
	syncAAD         = "hmck-sync-v1"
	syncMaxFileSize = 2 << 20
	syncMaxTotal    = 8 << 20

	// syncStateName 自己不能进同步清单：远端内容里的同名项会被跳过，
	// 不然 pull 会把本机的密钥和版本记录冲掉。
	syncStateName = "config:hmck/sync.json"
)

// syncMu 让同一进程里的自动推送和手动推送排队，sync.json 不会被两边同时改
var syncMu sync.Mutex

// syncState 是这台机器的同步记录，存在 <UserConfigDir>/hmck/sync.json。
//
// Key 是同步密码派生出的密钥（base64），不是密码本身；它和别的工作配置一样只在
// 本机 0600 文件里，攻破 GitHub 仓库拿不到。LastSHA 是上次看到/写入的远端文件
// SHA，用来发现「别的设备推过新版本」；LastHash 是本地打包内容的哈希，没变就不推。
type syncState struct {
	Repo       string   `json:"repo,omitempty"`
	Key        string   `json:"key,omitempty"`
	Salt       []byte   `json:"salt,omitempty"`
	Iter       int      `json:"iter,omitempty"`
	LastSHA    string   `json:"last_sha,omitempty"`
	LastHash   string   `json:"last_hash,omitempty"`
	LastPush   string   `json:"last_push,omitempty"`
	LastPull   string   `json:"last_pull,omitempty"`
	LastError  string   `json:"last_error,omitempty"`
	ExtraPaths []string `json:"extra_paths,omitempty"`
}

func syncStatePath() (string, error) {
	dir, err := userConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "hmck", "sync.json"), nil
}

func loadSyncState() (syncState, error) {
	var st syncState
	path, err := syncStatePath()
	if err != nil {
		return st, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, fmt.Errorf("读不了 %s: %w", path, err)
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return syncState{}, fmt.Errorf("%s 不是合法 JSON: %w", path, err)
	}
	return st, nil
}

func saveSyncState(st syncState) (string, error) {
	path, err := syncStatePath()
	if err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return "", err
	}
	data = append(data, '\n')
	if err := writeFileAtomic(path, 0o600, data); err != nil {
		return "", err
	}
	return path, nil
}

// syncEnabled 表示这台机器配过密码、也认了仓库，之后的保存可以自动推。
func syncEnabled(st syncState) bool {
	return st.Repo != "" && st.Key != ""
}

func syncKeyBytes(st syncState) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(st.Key))
	if err != nil || len(key) != syncKeyLen {
		return nil, errors.New("本机的同步密钥坏了（sync.json 里的 key）；运行 hmck sync off 重新连接")
	}
	return key, nil
}

// ---------- 加密 ----------

// syncEnvelope 是 GitHub 上那份文件的格式。密文之外只放解密需要的参数：
// 密码派生用的 salt/迭代次数，和 AES-GCM 的 nonce。
type syncEnvelope struct {
	Version int    `json:"v"`
	KDF     string `json:"kdf"`
	Iter    int    `json:"iter"`
	Salt    []byte `json:"salt"`
	Nonce   []byte `json:"nonce"`
	Data    []byte `json:"data"`
}

func randomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("系统随机数不可用: %w", err)
	}
	return b, nil
}

func deriveSyncKey(pass string, salt []byte, iter int) ([]byte, error) {
	pass = strings.TrimSpace(pass)
	if pass == "" {
		return nil, errors.New("同步密码是空的")
	}
	key, err := pbkdf2.Key(sha256.New, pass, salt, iter, syncKeyLen)
	if err != nil {
		return nil, fmt.Errorf("派生密钥失败: %w", err)
	}
	return key, nil
}

// encryptSyncPayload 用已经派生好的密钥加密。salt/iter 必须和派生这把密钥时
// 用的一致：换机器的人靠它们从密码重新算出同一把钥匙。
func encryptSyncPayload(key, plain, salt []byte, iter int) ([]byte, error) {
	if len(key) != syncKeyLen {
		return nil, errors.New("密钥长度不对")
	}
	if len(salt) < 8 {
		return nil, errors.New("同步 salt 不对")
	}
	if iter < syncKDFMinIter || iter > syncKDFMaxIter {
		iter = syncKDFIter
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce, err := randomBytes(gcm.NonceSize())
	if err != nil {
		return nil, err
	}
	sealed := gcm.Seal(nil, nonce, plain, []byte(syncAAD))
	env := syncEnvelope{
		Version: syncFormat, KDF: syncKDF, Iter: iter,
		Salt: salt, Nonce: nonce, Data: sealed,
	}
	return json.MarshalIndent(env, "", "  ")
}

func parseSyncEnvelope(raw []byte) (syncEnvelope, error) {
	var env syncEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return env, fmt.Errorf("远端文件不是 hmck 同步格式: %w", err)
	}
	switch {
	case env.Version != syncFormat:
		return env, fmt.Errorf("同步格式版本 %d 不认识（本机只认 %d）", env.Version, syncFormat)
	case env.KDF != syncKDF:
		return env, fmt.Errorf("不认识的加密方式 %q", env.KDF)
	case env.Iter < syncKDFMinIter || env.Iter > syncKDFMaxIter:
		return env, fmt.Errorf("迭代次数 %d 不在合理范围", env.Iter)
	case len(env.Salt) < 8:
		return env, errors.New("加密参数缺少 salt")
	}
	return env, nil
}

func decryptSyncPayload(key, raw []byte) ([]byte, error) {
	env, err := parseSyncEnvelope(raw)
	if err != nil {
		return nil, err
	}
	if len(key) != syncKeyLen {
		return nil, errors.New("密钥长度不对")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(env.Nonce) != gcm.NonceSize() {
		return nil, errors.New("加密参数 nonce 长度不对")
	}
	plain, err := gcm.Open(nil, env.Nonce, env.Data, []byte(syncAAD))
	if err != nil {
		return nil, errors.New("解不开：同步密码不对，或者远端数据被改过")
	}
	return plain, nil
}

// decryptWithPassphrase 是换机器时的路：从远端文件里读 salt/迭代次数，
// 用用户输入的密码派生密钥再解密。第一次 pull 就靠它把密钥留在本机。
func decryptWithPassphrase(pass string, raw []byte) (key, plain []byte, err error) {
	env, err := parseSyncEnvelope(raw)
	if err != nil {
		return nil, nil, err
	}
	key, err = deriveSyncKey(pass, env.Salt, env.Iter)
	if err != nil {
		return nil, nil, err
	}
	plain, err = decryptSyncPayload(key, raw)
	if err != nil {
		return nil, nil, err
	}
	return key, plain, nil
}

// ---------- 打包 ----------

// syncFile 是打包进去的一个文件。Name 是逻辑名（config:hmck/config.json、
// home:.argocd/token），换机器也认；Data 在 JSON 里自动按 base64 存。
type syncFile struct {
	Name string `json:"name"`
	Mode uint32 `json:"mode,omitempty"`
	Data []byte `json:"data"`
}

// Paths 是「额外同步路径」清单：另一台机器 pull 之后也照这份继续带，
// 不用每台都手工 hmck sync add 一遍。
type syncPayload struct {
	Version   int        `json:"version"`
	UpdatedAt time.Time  `json:"updated_at"`
	Paths     []string   `json:"paths,omitempty"`
	Files     []syncFile `json:"files"`
}

// syncNames 列出要同步的逻辑路径：hmck 自己的两个配置文件，加上每个工具
// 在注册表里声明的 SyncPaths，最后是用户手工加进来的路径。
func syncNames(st syncState) ([]string, error) {
	names := []string{"config:hmck/config.json", "config:hmck/tools.json"}
	tools, err := allTools()
	if err != nil {
		return nil, err
	}
	for _, t := range tools {
		names = append(names, t.SyncPaths...)
	}
	names = append(names, st.ExtraPaths...)

	seen := make(map[string]bool, len(names))
	out := make([]string, 0, len(names))
	for _, n := range names {
		if n == syncStateName {
			continue
		}
		if _, err := syncNamePath(n); err != nil {
			return nil, err
		}
		if seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	sort.Strings(out)
	return out, nil
}

// syncNamePath 把逻辑名换成绝对路径。逻辑名只允许两种前缀，别的写法直接拒绝，
// 免得手滑把 / 或 .. 写进来把整个家目录打包。
func syncNamePath(name string) (string, error) {
	var base, rel string
	switch {
	case strings.HasPrefix(name, "config:"):
		dir, err := userConfigDir()
		if err != nil {
			return "", err
		}
		base, rel = dir, strings.TrimPrefix(name, "config:")
	case strings.HasPrefix(name, "home:"):
		home, err := userHomeDir()
		if err != nil {
			return "", err
		}
		base, rel = home, strings.TrimPrefix(name, "home:")
	default:
		return "", fmt.Errorf("同步路径要写成 config:… 或 home:…：%s", name)
	}
	rel = filepath.Clean(filepath.FromSlash(rel))
	if rel == "." || rel == ".." || filepath.IsAbs(rel) ||
		strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("同步路径不能跳出目录：%s", name)
	}
	return filepath.Join(base, rel), nil
}

// normalizeSyncPath 把用户输入的路径收成逻辑名：能放进配置目录/家目录的
// 就存相对形式，换机器也能落到对应位置；都不在的保留绝对路径。
func normalizeSyncPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", errors.New("路径是空的")
	}
	if strings.HasPrefix(p, "config:") || strings.HasPrefix(p, "home:") {
		if _, err := syncNamePath(p); err != nil {
			return "", err
		}
		return filepath.ToSlash(p), nil
	}
	if p == "~" {
		return "", errors.New("不能把整个家目录加进来")
	}
	if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, "~"+string(filepath.Separator)) {
		return "home:" + filepath.ToSlash(strings.TrimPrefix(filepath.ToSlash(p), "~/")), nil
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	if dir, err := userConfigDir(); err == nil {
		if rel, err := filepath.Rel(dir, abs); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "config:" + filepath.ToSlash(rel), nil
		}
	}
	if home, err := userHomeDir(); err == nil {
		if rel, err := filepath.Rel(home, abs); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "home:" + filepath.ToSlash(rel), nil
		}
	}
	return "", fmt.Errorf("%s 不在配置目录或家目录里，不同机器上落不到同一个位置；请用 config:… 或 home:… 形式", shortenHome(abs))
}

// collectSyncFiles 读一遍所有存在的文件。不存在的跳过（不是每台机器都有
// 每个工具的配置），目录和非普通文件也跳过，单个文件超过上限直接报错。
func collectSyncFiles(st syncState) ([]syncFile, error) {
	names, err := syncNames(st)
	if err != nil {
		return nil, err
	}
	var out []syncFile
	var total int64
	for _, name := range names {
		abs, err := syncNamePath(name)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(abs)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("看不了 %s: %w", shortenHome(abs), err)
		}
		if !info.Mode().IsRegular() {
			continue
		}
		if info.Size() > syncMaxFileSize {
			return nil, fmt.Errorf("%s 有 %s，超过单文件 %s 上限", shortenHome(abs), humanSize(info.Size()), humanSize(syncMaxFileSize))
		}
		total += info.Size()
		if total > syncMaxTotal {
			return nil, fmt.Errorf("要同步的文件加起来超过 %s 了；用 hmck sync rm 去掉不需要的路径", humanSize(syncMaxTotal))
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			return nil, fmt.Errorf("读不了 %s: %w", shortenHome(abs), err)
		}
		out = append(out, syncFile{Name: name, Mode: uint32(info.Mode().Perm()), Data: data})
	}
	return out, nil
}

// payloadHash 只算文件内容和路径清单，不算 UpdatedAt：内容没变就不用再推一次。
func payloadHash(files []syncFile, paths []string) (string, error) {
	b, err := json.Marshal(struct {
		Version int        `json:"version"`
		Paths   []string   `json:"paths,omitempty"`
		Files   []syncFile `json:"files"`
	}{syncFormat, paths, files})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// applySyncPayload 把远端内容写到本机。所有文件都按 0600 落盘：同步下来
// 的多半是 token 或带地址的配置，不该给同组/其他人读。dryRun 只算不写。
func applySyncPayload(p syncPayload, dryRun bool) (written, same []string, err error) {
	if p.Version != syncFormat {
		return nil, nil, fmt.Errorf("同步内容版本 %d 不认识", p.Version)
	}
	for _, f := range p.Files {
		if f.Name == syncStateName {
			continue
		}
		abs, err := syncNamePath(f.Name)
		if err != nil {
			return written, same, err
		}
		old, rerr := os.ReadFile(abs)
		if rerr == nil && string(old) == string(f.Data) {
			same = append(same, f.Name)
			continue
		}
		if rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
			return written, same, fmt.Errorf("读不了 %s: %w", shortenHome(abs), rerr)
		}
		if dryRun {
			written = append(written, f.Name)
			continue
		}
		if err := writeFileAtomic(abs, 0o600, f.Data); err != nil {
			return written, same, fmt.Errorf("写 %s 失败: %w", shortenHome(abs), err)
		}
		written = append(written, f.Name)
	}
	return written, same, nil
}

// mergeSyncFiles 把远端带回来的文件并进本地清单：同名以本地为准，
// 只有远端才有的文件原样带回去，这样各机器装的东西不一样也不会互相抹掉。
func mergeSyncFiles(local, remote []syncFile) []syncFile {
	seen := make(map[string]bool, len(local))
	out := append([]syncFile(nil), local...)
	for _, f := range local {
		seen[f.Name] = true
	}
	for _, f := range remote {
		if seen[f.Name] {
			continue
		}
		seen[f.Name] = true
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// diffAgainstPayload 比较本机现有文件和远端内容：本机有不一样的内容时，
// pull 会覆盖掉它，得先让用户知道（多半是本机改完还没推上去）。
func diffAgainstPayload(p syncPayload) (changed []string) {
	for _, f := range p.Files {
		abs, err := syncNamePath(f.Name)
		if err != nil {
			changed = append(changed, f.Name)
			continue
		}
		old, err := os.ReadFile(abs)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				changed = append(changed, f.Name)
			}
			continue
		}
		if string(old) != string(f.Data) {
			changed = append(changed, f.Name)
		}
	}
	return changed
}

// writeFileAtomic 和别处保存配置一样：同目录临时文件 + rename，权限一次到位。
func writeFileAtomic(path string, perm os.FileMode, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(name)
		return werr
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}
