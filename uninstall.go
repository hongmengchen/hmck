package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// uninstallResult 是一次卸载干了什么：删了哪些、留了哪些。
// 每一项都已经写成给人看的一句话，CLI 和 TUI 原样打印。
type uninstallResult struct {
	Tool    tool
	Removed []string // 删掉的东西
	Kept    []string // 留着没动的东西，以及为什么留
	Err     string
}

// managedCopy 是 hmck 自己装出来的那一份。
// hmck 自己就是正在运行的这个可执行文件；别的工具只认 hmck 安装目录里的那份——
// PATH 上别人装的副本（brew 的 glab、从别处拷来的 kube-token）不归 hmck 删。
func managedCopy(t tool) string {
	if t.Self {
		if exe, err := runningExe(); err == nil {
			return realPath(exe)
		}
		return ""
	}
	if p, ok := managedPath(t); ok {
		return realPath(p)
	}
	return ""
}

// uninstallTool 删掉 hmck 装的二进制、配套的 skill 和预下载缓存。
// keepSkill 为真时留着 skill；PATH 上别人的副本和自定义工具配置一律不动。
func uninstallTool(t tool, keepSkill bool) uninstallResult {
	res := uninstallResult{Tool: t}

	bin := managedCopy(t)
	if bin == "" {
		res.Err = "hmck 目录里没有 " + t.Key
		for _, p := range foreignCopies(t, "") {
			res.Kept = append(res.Kept, "PATH 上的 "+shortenHome(p)+"（不是 hmck 装的）")
		}
		return res
	}
	if err := removeBinary(bin); err != nil {
		res.Err = "删不掉 " + shortenHome(bin) + ": " + err.Error()
		return res
	}
	res.Removed = append(res.Removed, "二进制 "+shortenHome(bin))

	if t.Skill || t.Self {
		res.removeSkill(t, keepSkill)
	}
	if n := removeCached(t); n > 0 {
		res.Removed = append(res.Removed, fmt.Sprintf("预下载缓存 %d 个文件", n))
	}
	// 删完再看 PATH 上还剩什么：brew 装的 glab 之类是别人的，留着
	for _, p := range foreignCopies(t, "") {
		res.Kept = append(res.Kept, "PATH 上的 "+shortenHome(p)+"（不是 hmck 装的）")
	}
	if t.Custom {
		res.Kept = append(res.Kept, "自定义工具配置还在（要一起删用 hmck rm "+t.Key+"）")
	}
	removeUpdate(t.Key)
	return res
}

// removeSkill 删掉 `<skills>/<key>`。里面得有 SKILL.md 才动手，
// 免得把同名的别的目录误删了
func (res *uninstallResult) removeSkill(t tool, keep bool) {
	root, err := skillRoot()
	if err != nil {
		return
	}
	dir := filepath.Join(root, t.Key)
	if !fileExists(filepath.Join(dir, "SKILL.md")) {
		return
	}
	if keep {
		res.Kept = append(res.Kept, "skill "+shortenHome(dir)+"（--keep-skill）")
		return
	}
	if err := os.RemoveAll(dir); err != nil {
		res.Kept = append(res.Kept, "skill 没删掉: "+err.Error())
		return
	}
	res.Removed = append(res.Removed, "skill "+shortenHome(dir))
}

// removeBinary 删掉二进制。正在运行的程序在 Windows 上删不掉，
// 就改名成 .old——重启后它不会再碍事，下次安装也不受影响
func removeBinary(path string) error {
	err := os.Remove(path)
	if err == nil || os.IsNotExist(err) {
		return nil
	}
	old := path + ".old"
	os.Remove(old)
	if rerr := os.Rename(path, old); rerr != nil {
		return err
	}
	return nil
}

// removeCached 清掉预下载缓存里属于这个工具的文件，返回删了几个
func removeCached(t tool) int {
	n := 0
	for _, p := range cachedFiles(t) {
		if os.Remove(p) == nil {
			n++
		}
	}
	return n
}

// cachedFiles 是预下载缓存里属于这个工具的文件
func cachedFiles(t tool) []string {
	root, err := userCacheDir()
	if err != nil {
		return nil
	}
	dir := filepath.Join(root, "hmck", "downloads")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), t.Key+"_") {
			continue
		}
		out = append(out, filepath.Join(dir, e.Name()))
	}
	return out
}

// uninstallPreview 是确认页上的清单：删什么、留什么，按 y 之前先看明白
func uninstallPreview(st toolState) []infoField {
	var out []infoField
	if bin := managedCopy(st.Tool); bin != "" {
		out = append(out, infoField{Key: "删除", Value: shortenHome(bin), Kind: "warn"})
	}
	if st.Tool.Skill || st.Tool.Self {
		if root, err := skillRoot(); err == nil {
			dir := filepath.Join(root, st.Tool.Key)
			if fileExists(filepath.Join(dir, "SKILL.md")) {
				out = append(out, infoField{Key: "删除", Value: "skill " + shortenHome(dir), Kind: "warn"})
			}
		}
	}
	if n := len(cachedFiles(st.Tool)); n > 0 {
		out = append(out, infoField{Key: "删除", Value: fmt.Sprintf("预下载缓存 %d 个文件", n), Kind: "warn"})
	}
	for _, p := range st.Foreign {
		out = append(out, infoField{Key: "保留", Value: shortenHome(p) + "（不是 hmck 装的）"})
	}
	if st.Tool.Custom {
		out = append(out, infoField{Key: "保留", Value: "自定义工具配置（要一起删用 hmck rm）"})
	}
	return out
}

// lines 把结果翻成结果页上的几行
func (r uninstallResult) lines() []resultLine {
	var out []resultLine
	if r.Err != "" {
		out = append(out, resultLine{text: r.Tool.Key + " 没卸载: " + r.Err, ok: false})
	} else {
		out = append(out, resultLine{text: r.Tool.Key + " 已卸载", ok: true})
	}
	for _, s := range r.Removed {
		out = append(out, resultLine{text: "  删掉 " + s, ok: true})
	}
	for _, s := range r.Kept {
		out = append(out, resultLine{text: "  留着 " + s, ok: true})
	}
	if r.Tool.Self && r.Err == "" {
		out = append(out, resultLine{text: "  hmck 自己已经删了，按任意键退出", ok: true})
	}
	return out
}
