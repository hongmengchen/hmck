package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// uninstallSandbox 把 hmck 眼里的家目录、安装目录、缓存目录全换到临时目录，
// 也在临时目录里放一个假的 hmck 当“正在运行的自己”，卸载测试才碰不到真实文件
type uninstallSandbox struct {
	home, bin, cache string
}

func newUninstallSandbox(t *testing.T) uninstallSandbox {
	t.Helper()
	sb := uninstallSandbox{home: t.TempDir(), bin: t.TempDir(), cache: t.TempDir()}

	oldHome, oldExe, oldCache := userHomeDir, runningExe, userCacheDir
	userHomeDir = func() (string, error) { return sb.home, nil }
	runningExe = func() (string, error) { return filepath.Join(sb.bin, "hmck"), nil }
	userCacheDir = func() (string, error) { return sb.cache, nil }
	t.Cleanup(func() { userHomeDir, runningExe, userCacheDir = oldHome, oldExe, oldCache })

	// 历史记录走 os.UserConfigDir，只认 $HOME，也得指到临时目录
	t.Setenv("HOME", sb.home)
	t.Setenv("PATH", t.TempDir()) // 不让真实 PATH 上的同名程序混进 foreignCopies
	resetHistory()
	t.Cleanup(resetHistory)
	return sb
}

// writeFile 建好上级目录再写文件
func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// 卸载要删掉：二进制、skill、只属于它的预下载缓存、更新时间记录
func TestUninstallRemovesWhatItInstalled(t *testing.T) {
	sb := newUninstallSandbox(t)
	tl := tool{Key: "kube-token", Binary: "kube-token", Skill: true}
	bin := filepath.Join(sb.bin, binaryName(tl, runtime.GOOS))
	writeFile(t, bin, "bin")

	skillDir := filepath.Join(sb.home, ".claude", "skills", tl.Key)
	writeFile(t, filepath.Join(skillDir, "SKILL.md"), "# skill")

	downloads := filepath.Join(sb.cache, "hmck", "downloads")
	mine := filepath.Join(downloads, "kube-token_1.2.0_darwin_arm64")
	other := filepath.Join(downloads, "npm-token_1.0.0_darwin_arm64")
	writeFile(t, mine, "new")
	writeFile(t, other, "keep")

	recordUpdate(tl.Key, "1.2.0")

	res := uninstallTool(tl, false)
	if res.Err != "" {
		t.Fatalf("卸载失败: %s", res.Err)
	}
	if exists(bin) {
		t.Error("二进制应该删掉")
	}
	if exists(skillDir) {
		t.Error("skill 目录应该删掉")
	}
	if exists(mine) {
		t.Error("属于这个工具的缓存应该删掉")
	}
	if !exists(other) {
		t.Error("别的工具的缓存不该动")
	}
	if _, ok := historyUpdatedAt(tl.Key, "1.2.0"); ok {
		t.Error("更新时间记录应该一起清掉")
	}
	if got := strings.Join(res.Removed, "\n"); !strings.Contains(got, "二进制") ||
		!strings.Contains(got, "skill") || !strings.Contains(got, "预下载缓存") {
		t.Errorf("结果里该说清删了什么，得到:\n%s", got)
	}
}

// --keep-skill 时 skill 留着，结果里也要说明是特意留的
func TestUninstallKeepsSkill(t *testing.T) {
	sb := newUninstallSandbox(t)
	tl := tool{Key: "npm-token", Binary: "npm-token", Skill: true}
	writeFile(t, filepath.Join(sb.bin, binaryName(tl, runtime.GOOS)), "bin")
	skillDir := filepath.Join(sb.home, ".claude", "skills", tl.Key)
	writeFile(t, filepath.Join(skillDir, "SKILL.md"), "# skill")

	res := uninstallTool(tl, true)
	if res.Err != "" {
		t.Fatalf("卸载失败: %s", res.Err)
	}
	if !exists(filepath.Join(skillDir, "SKILL.md")) {
		t.Error("--keep-skill 时 skill 应该留着")
	}
	if got := strings.Join(res.Kept, "\n"); !strings.Contains(got, "--keep-skill") {
		t.Errorf("结果里该说明 skill 是特意留的，得到:\n%s", got)
	}
}

// hmck 目录里没有、PATH 上只有别人装的：不删，说清楚为什么不删
func TestUninstallLeavesForeignCopy(t *testing.T) {
	newUninstallSandbox(t)
	otherDir := t.TempDir()
	t.Setenv("PATH", otherDir)
	tl := tool{Key: "glab", Binary: "glab", OwnDir: true}
	foreign := filepath.Join(otherDir, binaryName(tl, runtime.GOOS))
	writeFile(t, foreign, "brew glab")

	res := uninstallTool(tl, false)
	if res.Err == "" {
		t.Error("没有 hmck 装的那份时应该报错而不是假装删了")
	}
	if !exists(foreign) {
		t.Error("PATH 上别人装的 glab 不该被删")
	}
	if got := strings.Join(res.Kept, "\n"); !strings.Contains(got, "不是 hmck 装的") {
		t.Errorf("结果里该说明 PATH 上那份留着，得到:\n%s", got)
	}
}

// hmck 自己也卸得掉，而且删的就是“正在运行的这个文件”
func TestUninstallSelf(t *testing.T) {
	sb := newUninstallSandbox(t)
	self := tool{Key: "hmck", Binary: "hmck", Self: true}
	exe := filepath.Join(sb.bin, binaryName(self, runtime.GOOS))
	writeFile(t, exe, "hmck")
	skillDir := filepath.Join(sb.home, ".claude", "skills", "hmck")
	writeFile(t, filepath.Join(skillDir, "SKILL.md"), "# skill")

	res := uninstallTool(self, false)
	if res.Err != "" {
		t.Fatalf("卸载自己失败: %s", res.Err)
	}
	if exists(exe) {
		t.Error("正在运行的 hmck 文件应该删掉")
	}
	if exists(skillDir) {
		t.Error("hmck 的 skill 也应该一起删掉")
	}
	lines := res.lines()
	if got := lines[len(lines)-1].text; !strings.Contains(got, "按任意键退出") {
		t.Errorf("卸载自己后该提示退出，得到 %q", got)
	}
}

// 自定义工具的配置不归卸载管，要删得用 hmck rm
func TestUninstallKeepsCustomConfig(t *testing.T) {
	sb := newUninstallSandbox(t)
	tl := tool{Key: "mytool", Binary: "mytool", Custom: true}
	writeFile(t, filepath.Join(sb.bin, binaryName(tl, runtime.GOOS)), "bin")

	res := uninstallTool(tl, false)
	if res.Err != "" {
		t.Fatalf("卸载失败: %s", res.Err)
	}
	if got := strings.Join(res.Kept, "\n"); !strings.Contains(got, "hmck rm mytool") {
		t.Errorf("结果里该指向 hmck rm，得到:\n%s", got)
	}
}

// 确认页上「删」和「留」都得摆出来，别让人敲名字之前看不见
func TestUninstallPreview(t *testing.T) {
	sb := newUninstallSandbox(t)
	tl := tool{Key: "kube-token", Binary: "kube-token"}
	writeFile(t, filepath.Join(sb.bin, binaryName(tl, runtime.GOOS)), "bin")

	st := toolState{Tool: tl, Foreign: []string{filepath.Join(sb.home, "elsewhere", "kube-token")}}
	fields := uninstallPreview(st)
	var del, keep int
	for _, f := range fields {
		switch f.Key {
		case "删除":
			del++
			if f.Kind != "warn" {
				t.Errorf("删除项该标成 warn，得到 %q", f.Kind)
			}
		case "保留":
			keep++
		}
	}
	if del == 0 || keep == 0 {
		t.Errorf("清单里该同时有删除和保留，得到 %+v", fields)
	}
}
