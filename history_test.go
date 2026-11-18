package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 升级成功后写进 state.json，换个进程也读得回来；版本对不上就不沿用
func TestRecordUpdatePersists(t *testing.T) {
	isolateConfig(t)

	recordUpdate("tool-a", "1.2.3")
	at, ok := historyUpdatedAt("tool-a", "1.2.3")
	if !ok || at.IsZero() {
		t.Fatalf("刚记的更新时间读不到: %v %v", at, ok)
	}
	if _, ok := historyUpdatedAt("tool-a", "1.2.4"); ok {
		t.Error("版本变了还沿用旧的更新时间")
	}

	path, err := historyPath()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("state.json 没写出来: %v", err)
	}
	var saved map[string]updateRecord
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatalf("state.json 不是合法 JSON: %v", err)
	}
	if saved["tool-a"].Version != "1.2.3" {
		t.Errorf("落盘内容 = %+v", saved)
	}

	// 模拟下次启动：内存缓存清掉，重新从文件读
	resetHistory()
	if _, ok := historyUpdatedAt("tool-a", "1.2.3"); !ok {
		t.Error("重新加载后读不到记录")
	}
}

// 不是 hmck 装的（或记录被删了），用二进制自己的修改时间兜底
func TestUpdatedAtForFallsBackToMTime(t *testing.T) {
	isolateConfig(t)
	dir := t.TempDir()
	p := filepath.Join(dir, "tool-x")
	if err := os.WriteFile(p, []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	want := time.Now().Add(-72 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(p, want, want); err != nil {
		t.Fatal(err)
	}
	got := updatedAtFor("tool-x", "1.0.0", p)
	if got.IsZero() || got.Sub(want).Abs() > time.Second {
		t.Errorf("updatedAtFor = %v, want ≈ %v", got, want)
	}

	// 有匹配版本的记录时，以记录为准
	recordUpdate("tool-y", "2.0.0")
	if at := updatedAtFor("tool-y", "2.0.0", ""); at.IsZero() {
		t.Error("有记录时不应当退回文件时间")
	}
}

func TestUpdatedText(t *testing.T) {
	now := time.Date(2026, 11, 18, 21, 0, 0, 0, time.Local)
	cases := []struct {
		ago  time.Duration
		want string
	}{
		{30 * time.Second, "刚刚更新"},
		{5 * time.Minute, "5 分钟前更新"},
		{3 * time.Hour, "3 小时前更新"},
		{3 * 24 * time.Hour, "3 天前更新"},
		{40 * 24 * time.Hour, "2026-10-09 更新"},
	}
	for _, c := range cases {
		if got := updatedText(now.Add(-c.ago), now); got != c.want {
			t.Errorf("updatedText(-%v) = %q, want %q", c.ago, got, c.want)
		}
	}
	if got := updatedText(time.Time{}, now); got != "" {
		t.Errorf("没有时间时应当返回空，got %q", got)
	}
}

// 卡片第一行右边显示更新时间
func TestCardShowsUpdatedTime(t *testing.T) {
	isolateConfig(t)
	m := newTUIModel()
	m.width = 120
	m.localReady = true
	st := toolState{
		Tool: tool{Key: "kube-token", Desc: "粘贴新 token"}, Path: "/tmp/kube-token",
		Installed: "1.0.0", Latest: "1.0.0", UpdatedAt: time.Now().Add(-72 * time.Hour),
	}
	card := plainText(m.renderCard(st, false, m.cardCols()))
	if !strings.Contains(card, "3 天前更新") {
		t.Errorf("卡片里没有更新时间:\n%s", card)
	}
}
