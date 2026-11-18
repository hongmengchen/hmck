package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// updateRecord 记住某个工具最后一次被 hmck 装上/升级的时间和版本。
// 记的是「版本更新时间」，不是每次打开 TUI 的时间。
type updateRecord struct {
	Version   string    `json:"version"`
	UpdatedAt time.Time `json:"updated_at"`
}

func historyPath() (string, error) {
	dir, err := userConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "hmck", "state.json"), nil
}

var (
	historyMu   sync.Mutex
	historyData map[string]updateRecord
	historyRead bool
)

// withHistory 在锁里访问记录；第一次访问才读文件，读坏了当空的，不影响 TUI
func withHistory(fn func(map[string]updateRecord)) {
	historyMu.Lock()
	defer historyMu.Unlock()
	if !historyRead {
		historyData = readHistory()
		historyRead = true
	}
	fn(historyData)
}

func readHistory() map[string]updateRecord {
	out := map[string]updateRecord{}
	path, err := historyPath()
	if err != nil {
		return out
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	_ = json.Unmarshal(data, &out) // 格式坏了就从空记录开始
	return out
}

// resetHistory 清掉进程内的缓存，让下一次访问重新按当前配置目录读（测试用）
func resetHistory() {
	historyMu.Lock()
	defer historyMu.Unlock()
	historyData = nil
	historyRead = false
}

// historyUpdatedAt 只有记录的版本和当前装的一致才算数，
// 免得手动换过版本之后还显示旧的时间
func historyUpdatedAt(key, version string) (time.Time, bool) {
	var at time.Time
	ok := false
	withHistory(func(m map[string]updateRecord) {
		rec, found := m[key]
		if found && rec.Version == version && !rec.UpdatedAt.IsZero() {
			at, ok = rec.UpdatedAt, true
		}
	})
	return at, ok
}

// recordUpdate 在 hmck 自己完成安装/升级后调用；写盘失败不影响升级结果
func recordUpdate(key, version string) {
	if key == "" || version == "" {
		return
	}
	var snapshot map[string]updateRecord
	withHistory(func(m map[string]updateRecord) {
		m[key] = updateRecord{Version: version, UpdatedAt: time.Now()}
		snapshot = make(map[string]updateRecord, len(m))
		for k, v := range m {
			snapshot[k] = v
		}
	})
	_ = writeHistory(snapshot)
}

func writeHistory(m map[string]updateRecord) error {
	path, err := historyPath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".state-*.json")
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

// updatedAtFor 记录里没有这个版本时，退回二进制文件的修改时间：
// 手动装、从别处拷来的工具也能显示出大概什么时候换的
func updatedAtFor(key, version, path string) time.Time {
	if at, ok := historyUpdatedAt(key, version); ok {
		return at
	}
	if path != "" {
		if fi, err := os.Stat(path); err == nil && !fi.IsDir() {
			return fi.ModTime()
		}
	}
	return time.Time{}
}

// updatedText 把时间写成一眼能读的短句；超过一个月就给日期
func updatedText(at, now time.Time) string {
	if at.IsZero() {
		return ""
	}
	d := now.Sub(at)
	switch {
	case d < time.Minute:
		return "刚刚更新"
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟前更新", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d 小时前更新", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%d 天前更新", int(d.Hours()/24))
	default:
		return at.Format("2006-01-02") + " 更新"
	}
}
