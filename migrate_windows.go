//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
// 只有确实需要改的时候才起 PowerShell，避免拖慢启动。
func cleanUserPath(target string, legacy []string) {
	current := os.Getenv("PATH")
	need := !pathHas(current, target)
	for _, dir := range legacy {
		if pathHas(current, dir) {
			need = true
			break
		}
	}
	if !need {
		return
	}

	// 让本次运行里启动的子进程立刻用新目录
	os.Setenv("PATH", target+string(os.PathListSeparator)+current)

	drop := append([]string{target}, legacy...)
	quoted := make([]string, len(drop))
	for i, d := range drop {
		quoted[i] = psQuote(d)
	}
	script := strings.Join([]string{
		"$target = " + psQuote(target),
		"$drop = @(" + strings.Join(quoted, ",") + ") | ForEach-Object { $_.TrimEnd('\\') }",
		"$p = [Environment]::GetEnvironmentVariable('Path','User')",
		"$parts = @()",
		"if ($p) { $parts = @($p -split ';' | Where-Object { $_.Trim() -ne '' }) }",
		"$parts = @($parts | Where-Object { $drop -notcontains $_.TrimEnd('\\') })",
		"$new = @($target) + $parts",
		"[Environment]::SetEnvironmentVariable('Path', ($new -join ';'), 'User')",
	}, "; ")
	_ = exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script).Run()
}

func psQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func pathHas(pathEnv, dir string) bool {
	want := strings.TrimRight(dir, `\/`)
	for _, p := range strings.Split(pathEnv, string(os.PathListSeparator)) {
		if p == "" {
			continue
		}
		if strings.EqualFold(strings.TrimRight(p, `\/`), want) {
			return true
		}
	}
	return false
}
