//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	winreg "golang.org/x/sys/windows/registry"
)

// legacyInstallDirs 是旧版 install.ps1 用过的安装目录（%LOCALAPPDATA%\<tool>）
func legacyInstallDirs() []string {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		return nil
	}
	return []string{
		filepath.Join(base, "hmck"),
		filepath.Join(base, "npm-token"),
		filepath.Join(base, "kube-token"),
	}
}

// isLegacyPath 判断某个路径是不是旧安装目录里的文件
func isLegacyPath(p string) bool {
	for _, dir := range legacyInstallDirs() {
		if filepath.Dir(p) == dir {
			return true
		}
	}
	return false
}

// cleanUserPath 把统一目录放到用户 PATH 最前面，并去掉旧目录。
// 直接读写 HKCU\Environment，不启动 PowerShell，避免拖慢每次启动。
func cleanUserPath(target string, legacy []string) {
	current := os.Getenv("PATH")
	need := !pathHas(current, target)
	if !need {
		for _, dir := range legacy {
			if pathHas(current, dir) {
				need = true
				break
			}
		}
	}
	if !need {
		return
	}

	// 让本次运行里启动的子进程立刻用新目录
	os.Setenv("PATH", target+string(os.PathListSeparator)+current)

	if err := updateUserPath(target, legacy); err != nil {
		return
	}
	broadcastEnvChange() // 通知资源管理器刷新环境，新开的终端才能拿到新 PATH
}

func updateUserPath(target string, legacy []string) error {
	k, err := winreg.OpenKey(winreg.CURRENT_USER, `Environment`, winreg.QUERY_VALUE|winreg.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()

	cur, typ, err := k.GetStringValue("Path")
	if err != nil && err != winreg.ErrNotExist {
		return err
	}
	drop := make(map[string]bool, len(legacy)+1)
	drop[normalizeDir(target)] = true
	for _, dir := range legacy {
		drop[normalizeDir(dir)] = true
	}

	var kept []string
	for _, part := range strings.Split(cur, ";") {
		if strings.TrimSpace(part) == "" || drop[normalizeDir(part)] {
			continue
		}
		kept = append(kept, part)
	}
	newPath := strings.Join(append([]string{target}, kept...), ";")
	if newPath == cur {
		return nil
	}
	if typ == winreg.EXPAND_SZ {
		return k.SetExpandStringValue("Path", newPath)
	}
	return k.SetStringValue("Path", newPath)
}

func normalizeDir(p string) string {
	return strings.ToLower(strings.TrimRight(strings.TrimSpace(p), `\/`))
}

func pathHas(pathEnv, dir string) bool {
	want := normalizeDir(dir)
	for _, p := range strings.Split(pathEnv, string(os.PathListSeparator)) {
		if p == "" {
			continue
		}
		if normalizeDir(p) == want {
			return true
		}
	}
	return false
}

var (
	user32DLL          = windows.NewLazySystemDLL("user32.dll")
	sendMessageTimeout = user32DLL.NewProc("SendMessageTimeoutW")
)

// broadcastEnvChange 广播 WM_SETTINGCHANGE，让新开的程序拿到更新后的用户 PATH
func broadcastEnvChange() {
	const (
		hwndBroadcast   = 0xffff
		wmSettingChange = 0x001a
		smtoAbortIfHung = 0x0002
	)
	env, err := windows.UTF16PtrFromString("Environment")
	if err != nil {
		return
	}
	var result uintptr
	_, _, _ = sendMessageTimeout.Call(
		hwndBroadcast, wmSettingChange, 0,
		uintptr(unsafe.Pointer(env)),
		smtoAbortIfHung, 1000, uintptr(unsafe.Pointer(&result)),
	)
}
