package main

import (
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// 所有测试都别往真实缓存目录写东西
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "hmck-test-cache-*")
	if err != nil {
		panic(err)
	}
	userCacheDir = func() (string, error) { return dir, nil }
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func TestConfigRoundTrip(t *testing.T) {
	withTempConfig(t)

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AutoDownload || cfg.AutoInstall {
		t.Fatalf("默认应当是关闭的: %+v", cfg)
	}

	path, err := saveConfig(appConfig{AutoDownload: true})
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(mustConfigDir(t), "hmck", "config.json") {
		t.Errorf("配置路径 = %s", path)
	}
	cfg, err = loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.AutoDownload || cfg.AutoInstall {
		t.Errorf("读回来不对: %+v", cfg)
	}

	// 打开自动安装时，自动下载必须一起打开
	if _, err := saveConfig(appConfig{AutoInstall: true}); err != nil {
		t.Fatal(err)
	}
	cfg, _ = loadConfig()
	if !cfg.AutoDownload || !cfg.AutoInstall {
		t.Errorf("自动安装应当隐含自动下载: %+v", cfg)
	}
}

func mustConfigDir(t *testing.T) string {
	t.Helper()
	dir, err := userConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestConfigBrokenJSON(t *testing.T) {
	dir := withTempConfig(t)
	path := filepath.Join(dir, "hmck", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{oops"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(); err == nil {
		t.Fatal("坏 JSON 应当报错")
	}
}

func TestToggleAutoSwitches(t *testing.T) {
	withTempConfig(t)
	m := newTUIModel()
	m.state = list

	updated, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	got := updated.(tuiModel)
	if !got.cfg.AutoDownload || got.cfg.AutoInstall {
		t.Fatalf("按 a 后 = %+v", got.cfg)
	}
	if cfg, _ := loadConfig(); !cfg.AutoDownload {
		t.Error("自动下载没写进配置")
	}

	updated, _ = got.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'A'}})
	got = updated.(tuiModel)
	if !got.cfg.AutoInstall || !got.cfg.AutoDownload {
		t.Fatalf("按 A 后 = %+v", got.cfg)
	}

	// 关掉自动下载时，自动安装也要一起关
	updated, _ = got.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	got = updated.(tuiModel)
	if got.cfg.AutoDownload || got.cfg.AutoInstall {
		t.Fatalf("关掉自动下载后 = %+v", got.cfg)
	}
}
