package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmbeddedSkill(t *testing.T) {
	if !strings.HasPrefix(embeddedSkill, "---\n") {
		t.Fatal("SKILL.md 缺少 frontmatter")
	}
	if !strings.Contains(embeddedSkill, "name: hmck") {
		t.Fatal("SKILL.md 的 name 不是 hmck")
	}
	if !strings.Contains(embeddedSkill, "\ndescription: ") {
		t.Fatal("SKILL.md 缺少 description")
	}
	if !strings.Contains(embeddedSkill, "hmck add") {
		t.Fatal("SKILL.md 没写怎么新增工具")
	}
}

func TestInstallSkill(t *testing.T) {
	root := filepath.Join(t.TempDir(), "skills")
	path := filepath.Join(root, "hmck", "SKILL.md")

	if skillInstalled(root) {
		t.Fatal("还没装就报告已安装")
	}

	got, changed, fresh, err := installSkill(root)
	if err != nil {
		t.Fatalf("首次安装失败: %v", err)
	}
	if got != path {
		t.Fatalf("路径不对: %s", got)
	}
	if !changed || !fresh {
		t.Fatalf("首次安装应 changed+fresh，得到 changed=%v fresh=%v", changed, fresh)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != embeddedSkill {
		t.Fatalf("写入内容不对: %v", err)
	}
	if !skillInstalled(root) {
		t.Fatal("装完却检测不到")
	}

	if _, changed, fresh, err := installSkill(root); err != nil || changed || fresh {
		t.Fatalf("重复安装应无变化，得到 changed=%v fresh=%v err=%v", changed, fresh, err)
	}

	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, changed, fresh, err = installSkill(root)
	if err != nil {
		t.Fatalf("更新失败: %v", err)
	}
	if !changed || fresh {
		t.Fatalf("更新应 changed 且非 fresh，得到 changed=%v fresh=%v", changed, fresh)
	}
	if data, _ := os.ReadFile(path); string(data) != embeddedSkill {
		t.Fatal("更新后内容没变")
	}
}

func TestRefreshToolSkillsSkip(t *testing.T) {
	done, failed := refreshToolSkills(true)
	if len(done) != 0 || len(failed) != 0 {
		t.Errorf("skip 时应什么都不做，得到 done=%v failed=%v", done, failed)
	}
}
