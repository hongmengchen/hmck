package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// 自定义工具存在本机配置里，加进来就能用 hmck 安装/升级，不用重新编译 hmck。
var userConfigDir = os.UserConfigDir // 测试里替换

var defaultPlatforms = []string{
	"darwin/amd64", "darwin/arm64",
	"linux/amd64", "linux/arm64",
	"windows/amd64", "windows/arm64",
}

var (
	knownOS   = map[string]bool{"darwin": true, "linux": true, "windows": true}
	knownArch = map[string]bool{"amd64": true, "arm64": true}
)

func customToolsPath() (string, error) {
	dir, err := userConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "hmck", "tools.json"), nil
}

type toolsConfig struct {
	Tools []tool `json:"tools"`
}

func loadCustomTools() ([]tool, error) {
	path, err := customToolsPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读不了 %s: %w", path, err)
	}

	var cfg toolsConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("%s 不是合法 JSON: %w", path, err)
	}
	out := make([]tool, 0, len(cfg.Tools))
	for i := range cfg.Tools {
		t := cfg.Tools[i]
		if err := normalizeCustom(&t); err != nil {
			return nil, fmt.Errorf("%s 第 %d 条（%s）: %w", path, i+1, t.Key, err)
		}
		t.Custom = true
		out = append(out, t)
	}
	return out, nil
}

func normalizeCustom(t *tool) error {
	t.Key = strings.TrimSpace(t.Key)
	t.Name = strings.TrimSpace(t.Name)
	t.Desc = strings.TrimSpace(t.Desc)
	t.Repo = strings.TrimSpace(t.Repo)
	t.Binary = strings.TrimSpace(t.Binary)

	if t.Key == "" {
		return errors.New("缺少 key")
	}
	if strings.ContainsAny(t.Key, " /\\") {
		return fmt.Errorf("key %q 不能包含空白或斜杠", t.Key)
	}
	if !strings.Contains(t.Repo, "/") {
		return fmt.Errorf("repo %q 需要写成 owner/name", t.Repo)
	}
	if t.Binary == "" {
		t.Binary = t.Key
	}
	if t.Name == "" {
		t.Name = t.Key
	}
	if len(t.Platforms) == 0 {
		t.Platforms = append([]string(nil), defaultPlatforms...)
	}
	for _, p := range t.Platforms {
		if !strings.Contains(p, "/") {
			return fmt.Errorf("平台 %q 需要写成 goos/goarch", p)
		}
	}
	return nil
}

func saveCustomTools(ts []tool) error {
	path, err := customToolsPath()
	if err != nil {
		return err
	}
	clean := make([]tool, len(ts))
	for i, t := range ts {
		t.Custom = false
		clean[i] = t
	}
	data, err := json.MarshalIndent(toolsConfig{Tools: clean}, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tools-*.json")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		os.Remove(name)
		if werr == nil {
			werr = cerr
		}
		return werr
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

func isBuiltin(key string) bool {
	for _, t := range registry {
		if strings.EqualFold(t.Key, key) {
			return true
		}
	}
	return false
}

// addCustomTool 新增或（force 时）覆盖一个自定义工具，返回配置文件路径
func addCustomTool(t tool, force bool) (string, error) {
	if err := normalizeCustom(&t); err != nil {
		return "", err
	}
	if isBuiltin(t.Key) {
		return "", fmt.Errorf("%s 是内置工具，不能这样覆盖", t.Key)
	}

	existing, err := loadCustomTools()
	if err != nil {
		return "", err
	}
	out := make([]tool, 0, len(existing)+1)
	replaced := false
	for _, e := range existing {
		if strings.EqualFold(e.Key, t.Key) {
			if !force {
				return "", fmt.Errorf("%s 已经添加过了；要改配置加 --force", t.Key)
			}
			out = append(out, t)
			replaced = true
			continue
		}
		out = append(out, e)
	}
	if !replaced {
		out = append(out, t)
	}
	if err := saveCustomTools(out); err != nil {
		return "", err
	}
	path, _ := customToolsPath()
	return path, nil
}

// removeCustomTool 只删自定义工具；内置的删不了
func removeCustomTool(key string) (string, error) {
	key = strings.TrimSpace(key)
	if isBuiltin(key) {
		return "", fmt.Errorf("%s 是内置工具，删不了", key)
	}
	existing, err := loadCustomTools()
	if err != nil {
		return "", err
	}
	out := make([]tool, 0, len(existing))
	found := false
	for _, e := range existing {
		if strings.EqualFold(e.Key, key) {
			found = true
			continue
		}
		out = append(out, e)
	}
	if !found {
		return "", fmt.Errorf("没有自定义工具 %q", key)
	}
	if err := saveCustomTools(out); err != nil {
		return "", err
	}
	path, _ := customToolsPath()
	return path, nil
}

// parseRepo 接受 owner/name 或 GitHub 链接
func parseRepo(s string) (string, error) {
	s = strings.TrimSpace(s)
	for _, prefix := range []string{"https://github.com/", "http://github.com/", "github.com/"} {
		s = strings.TrimPrefix(s, prefix)
	}
	s = strings.TrimSuffix(s, ".git")
	s = strings.Trim(s, "/")
	parts := strings.Split(s, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", errors.New("仓库要写成 owner/name（也可以直接贴 GitHub 链接）")
	}
	return parts[0] + "/" + parts[1], nil
}

func defaultKeyForRepo(repo string) string {
	return strings.ToLower(path.Base(repo))
}

// inferPlatforms 从发布资产名里读出 <key>_<goos>_<goarch>[.exe]
func inferPlatforms(key string, assets []string) []string {
	var out []string
	for _, name := range assets {
		n := strings.TrimSuffix(name, ".exe")
		if !strings.HasPrefix(n, key+"_") {
			continue
		}
		parts := strings.Split(strings.TrimPrefix(n, key+"_"), "_")
		if len(parts) != 2 {
			continue
		}
		goos, goarch := parts[0], parts[1]
		if !knownOS[goos] || !knownArch[goarch] {
			continue
		}
		p := goos + "/" + goarch
		if !containsString(out, p) {
			out = append(out, p)
		}
	}
	sortPlatforms(out)
	return out
}

func hasChecksums(assets []string) bool {
	return containsString(assets, "checksums.txt")
}

func sortPlatforms(ps []string) {
	order := func(p string) int {
		osRank := map[string]int{"darwin": 0, "linux": 1, "windows": 2}[strings.SplitN(p, "/", 2)[0]]
		archRank := map[string]int{"amd64": 0, "arm64": 1}[strings.SplitN(p, "/", 2)[1]]
		return osRank*10 + archRank
	}
	sort.SliceStable(ps, func(i, j int) bool { return order(ps[i]) < order(ps[j]) })
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
