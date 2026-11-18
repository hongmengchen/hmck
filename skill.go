package main

import (
	"context"
	_ "embed"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// agent skill 随二进制走：装好/升级好程序，agent 就能读到和当前版本配套的用法说明
//
//go:embed all:.claude/skills/hmck/SKILL.md
var embeddedSkill string

// skillRoot 是 agent 扫描的全局目录，默认 ~/.claude/skills
func skillRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "skills"), nil
}

func skillFile(root string) string {
	return filepath.Join(root, "hmck", "SKILL.md")
}

// installSkill 写入或更新 hmck 自己的 skill。
// changed 表示这次真的写了文件；fresh 表示之前没有、是首次安装
func installSkill(root string) (path string, changed, fresh bool, err error) {
	if root == "" {
		if root, err = skillRoot(); err != nil {
			return "", false, false, err
		}
	}
	path = skillFile(root)

	old, readErr := os.ReadFile(path)
	if readErr == nil && string(old) == embeddedSkill {
		return path, false, false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return path, false, false, fmt.Errorf("创建目录失败: %w", err)
	}
	if err := os.WriteFile(path, []byte(embeddedSkill), 0o644); err != nil {
		return path, false, false, fmt.Errorf("写入失败: %w", err)
	}
	return path, true, readErr != nil, nil
}

func skillInstalled(root string) bool {
	if root == "" {
		r, err := skillRoot()
		if err != nil {
			return false
		}
		root = r
	}
	_, err := os.Stat(skillFile(root))
	return err == nil
}

// refreshToolSkills 让每个已安装、且声明支持 skill 的工具刷新自己的说明。
// 老版本没有 skill 子命令时会失败，只记下来，不影响整体结果。
func refreshToolSkills(skip bool) (done, failed []string) {
	if skip {
		return nil, nil
	}
	tools, err := allTools()
	if err != nil {
		failed = append(failed, "读取自定义工具配置")
	}
	for _, t := range tools {
		if !t.Skill {
			continue
		}
		path, ok := findTool(t)
		if !ok {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		cmd := exec.CommandContext(ctx, path, "skill", "--quiet")
		cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
		err := cmd.Run()
		cancel()
		if err != nil {
			failed = append(failed, t.Key)
			continue
		}
		done = append(done, t.Key)
	}
	return done, failed
}

// runSkill 实现 `hmck skill`：装 hmck 自己的 skill，并让已安装的工具刷新各自的 skill
func runSkill(args []string) int {
	fs := flag.NewFlagSet("skill", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `用法: hmck skill [选项]

把 hmck 的 agent skill 写进 ~/.claude/skills/hmck/SKILL.md，
并让已安装的工具（kube-token / npm-token / 支持 skill 的自定义工具）刷新自己的 skill。

选项:
  -dir <dir>    skills 根目录（默认 ~/.claude/skills）
  -print        只把 hmck 的 SKILL.md 打到 stdout，不写文件
  -no-tools     只装 hmck 自己的，不动其它工具
  -quiet        成功时不打印
`)
	}
	dir := fs.String("dir", "", "skills 根目录（默认 ~/.claude/skills）")
	printOnly := fs.Bool("print", false, "只输出内容，不写文件")
	noTools := fs.Bool("no-tools", false, "只装 hmck 自己的 skill")
	quiet := fs.Bool("quiet", false, "成功时不打印")
	if err := fs.Parse(args); err != nil {
		return 1
	}

	if *printOnly {
		fmt.Print(embeddedSkill)
		return 0
	}

	path, changed, fresh, err := installSkill(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: %v\n", err)
		return 1
	}

	done, failed := refreshToolSkills(*noTools)

	if *quiet {
		return 0
	}
	switch {
	case fresh:
		fmt.Printf("skill 已安装: %s\n", shortenHome(path))
	case changed:
		fmt.Printf("skill 已更新: %s\n", shortenHome(path))
	default:
		fmt.Printf("skill 已是最新: %s\n", shortenHome(path))
	}
	if len(done) > 0 {
		fmt.Printf("已刷新    %s 的 skill\n", strings.Join(done, "、"))
	} else if !*noTools {
		fmt.Println("其它工具还没装（或还没升级到带 skill 的版本），装好后跑 hmck skill 即可")
	}
	if len(failed) > 0 {
		fmt.Fprintf(os.Stderr, "这些工具的 skill 没刷新成功（可能版本太旧）: %s\n", strings.Join(failed, "、"))
		fmt.Fprintf(os.Stderr, "先 hmck upgrade <tool> 再试\n")
	}
	fmt.Println("agent 在新会话里会读到它（内容跟当前程序版本一致，升级 hmck 时会一起刷新）")
	return 0
}
