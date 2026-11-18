package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

var legacyDirsForTest []string // 测试里替换

func legacyDirs() []string {
	if legacyDirsForTest != nil {
		return legacyDirsForTest
	}
	return legacyInstallDirs()
}

// migrateInstallLocations 把旧安装位置里的二进制搬到统一目录（~/.local/bin），
// 并清理 Windows 用户 PATH。只搬程序本体：配置在 %AppData% / ~/.npmrc，本来就不受安装位置影响。
// 迁移失败只影响这次搬家，不影响工具继续使用。
func migrateInstallLocations() {
	if os.Getenv("HMCK_NO_MIGRATE") != "" {
		return
	}
	legacy := legacyDirs()
	if len(legacy) == 0 {
		return
	}
	target, err := defaultBinDir()
	if err != nil {
		return
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return
	}

	running, _ := os.Executable()
	if resolved, err := filepath.EvalSymlinks(running); err == nil {
		running = resolved
	}

	tools, _ := allTools()
	for _, t := range tools {
		name := binaryName(t, runtime.GOOS)
		dst := filepath.Join(target, name)
		for _, dir := range legacy {
			src := filepath.Join(dir, name)
			if sameFile(src, dst) || !fileExists(src) {
				continue
			}
			_ = moveBinary(src, dst, running)
		}
		for _, dir := range legacy {
			// 旧版升级时留下的 .old，能删就删，好让旧目录整个消失
			_ = os.Remove(filepath.Join(dir, name+".old"))
		}
	}

	cleanUserPath(target, legacy)
	for _, dir := range legacy {
		_ = os.Remove(dir) // 空了才删得掉
	}
}

func sameFile(a, b string) bool {
	aa, err := filepath.Abs(a)
	if err != nil {
		return false
	}
	bb, err := filepath.Abs(b)
	if err != nil {
		return false
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(aa, bb)
	}
	return aa == bb
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// moveBinary 把旧位置的二进制挪到统一目录。
// Windows 上正在运行的 exe 既不能覆盖也不能删除，所以遇到“正在运行的自己”只复制，旧文件留到下次启动再清。
func moveBinary(src, dst, running string) error {
	if sameFile(src, running) {
		if !fileExists(dst) {
			return copyBinary(src, dst)
		}
		// 统一目录已有一份：只有旧位置这份更新时才覆盖它
		if isNewer(version, installedVersion(dst)) {
			return copyBinary(src, dst)
		}
		return nil
	}
	if sameFile(dst, running) {
		// 统一目录里的这份正在运行，删掉旧位置的就够了
		return removeFile(src)
	}
	if fileExists(dst) {
		sv, dv := installedVersion(src), installedVersion(dst)
		if sv == "" || (dv != "" && !isNewer(sv, dv)) {
			// 统一目录里的不比旧的差，旧副本直接清掉
			return removeFile(src)
		}
	}
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	if err := copyBinary(src, dst); err != nil {
		return err
	}
	return removeFile(src)
}

func copyBinary(src, dst string) error {
	tmp, err := copyToTemp(src, filepath.Dir(dst))
	if err != nil {
		return err
	}
	if err := installBinary(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func removeFile(p string) error {
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
