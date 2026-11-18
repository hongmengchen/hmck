# hmck

一个入口管理我自己的命令行小工具：看版本、安装、升级、直接启动，顺手把 agent skill 装好。

目前托管：

| 工具 | 作用 |
| --- | --- |
| [kube-token](https://github.com/hongmengchen/kube-token) | 粘贴新 token 替换 kubeconfig 里的旧值 |
| [npm-token](https://github.com/hongmengchen/npm-token) | 检查并更新 `~/.npmrc` 里的 registry token |

以后新增的工具不用改代码：`hmck add owner/repo` 存进本机配置，立刻就能装、能升级。

```
$ hmck
hmck  我的命令行小工具
  2 个工具 · 1 个可更新 · 检查于 15:42:07

│ kube-token   0.4.2 · 最新 0.4.2      ✓ 已是最新
│ 粘贴新 token 替换 kubeconfig 里的旧值
│ ~/.local/bin/kube-token
│
│ npm-token   0.1.0 → 0.1.1            ↑ 可更新
│ 检查并更新 ~/.npmrc 里的 registry token
│ ~/.local/bin/npm-token

  ↑↓ 选择  ·  enter 启动/安装  ·  u 升级  ·  U 一键全部更新  ·  s 装 skill  ·  r 重新检查  ·  q 退出
```

每个工具都同时显示已装版本和 GitHub 上的最新版本，能升的直接标 `0.1.0 → 0.1.1`。

`enter` 直接进对应工具的 TUI，用完退出就回到 hmck；`u` 升级选中的工具；`U` 把所有可更新的工具按顺序升一遍，结果页逐个列出 `旧版 → 新版`；没装的按 `enter` 就装；`s` 安装/刷新 agent skill；`r` 重新查一遍版本。

## 安装

macOS / Linux：

```bash
curl -fsSL https://raw.githubusercontent.com/hongmengchen/hmck/main/install.sh | sh
```

Windows（PowerShell）：

```powershell
irm https://raw.githubusercontent.com/hongmengchen/hmck/main/install.ps1 | iex
```

Windows 的 Git Bash / MSYS 里也能直接用上面的 `install.sh`（会装成 `hmck.exe`）；
想在 PowerShell / CMD 里也能用，优先选 `install.ps1`。

有 Go 环境可以直接 `go install github.com/hongmengchen/hmck@latest`。

安装完会自动跑一次 `hmck skill`，把 agent 用法说明写到 `~/.claude/skills/`。

## 用法

```bash
hmck                      # 打开管理界面
hmck list                 # 纯文本表格；--json 给脚本用
hmck run npm-token        # 直接启动，后面的参数原样传过去
hmck install npm-token    # 安装最新版
hmck upgrade              # 升级所有可升级的工具
hmck upgrade npm-token    # 只升一个
hmck upgrade --force      # 已是最新也重装一遍
hmck skill                # 给 hmck 和已安装的工具装 agent skill
```

版本检测：已安装版本读 `<tool> version`；最新版看 GitHub `releases/latest` 的跳转（不消耗 API 限流），失败再退回 API。

安装位置：工具已经在 PATH 里就原地升级；没装就装到 hmck 旁边（通常 `~/.local/bin`）。

升级安全：下载到同目录临时文件，有 `checksums.txt` 就校验 SHA-256，再原子 rename 覆盖；校验或下载失败不会动原文件。

## 新增一个工具

**首选：写进本机配置，不用重新编译 hmck。**

```bash
hmck add owner/my-tool          # 自动推断 key、平台、有没有 checksums.txt
hmck add https://github.com/owner/my-tool --desc "一句话说明"
hmck add owner/my-tool --key my-tool --platforms darwin/arm64,linux/amd64 --checksums --skill
hmck install my-tool            # 加完就能装
hmck rm my-tool                 # 只删配置，不删已装的二进制
```

配置位置：

| 系统 | 路径 |
| --- | --- |
| macOS | `~/Library/Application Support/hmck/tools.json` |
| Linux | `~/.config/hmck/tools.json` |
| Windows | `%AppData%\hmck\tools.json` |

`hmck add` 会去读最新 release，从资产名里推断支持的平台；读不到（没发版、网络不通、API 限流）
就先按六个常见平台记录，装的时候真实平台不支持会明确报错。内置工具的 key 不能被覆盖。

**约定**（新仓库照着做就能被 hmck 管理）：

1. tag 形如 `v1.2.3`，资产名 `<key>_<goos>_<goarch>[.exe]`，例如
   `my-tool_linux_arm64`、`my-tool_windows_amd64.exe`；
2. 可选但推荐发 `checksums.txt`（`sha256sum * > checksums.txt`），hmck 会校验；
3. 可选 `<tool> skill [--quiet] [--print] [--dir DIR]` 子命令 + 仓库里的
   `.claude/skills/<key>/SKILL.md`，这样 `hmck skill` 和安装后的自动刷新都能覆盖它
   （`hmck add` 时加 `--skill`）；
4. `<tool> version` 输出的最后一个字段要能当版本号。

**备选：内置。** 在 [tools.go](tools.go) 的 `registry` 里加一条，发新版 hmck：

```go
{
    Key:       "my-tool",
    Desc:      "一句话说明",
    Repo:      "you/my-tool",
    Binary:    "my-tool",
    Platforms: []string{"darwin/arm64", "linux/amd64", "windows/amd64"},
    Checksums: true, // 发布里有 checksums.txt 就打开
    Skill:     true, // 支持 <tool> skill --quiet
},
```

## Agent skill

```bash
hmck skill              # hmck 自己的 + 所有已安装工具的 skill
hmck skill --print      # 只看 hmck 的 SKILL.md
hmck skill --no-tools   # 只装 hmck 自己的
```

写到 `~/.claude/skills/<tool>/SKILL.md`（`-dir` 可换根目录）。内容随二进制走：升级工具时
由新二进制自己刷新，agent 不会读到过期文档。每个工具的 skill 里都写了 CLI 用法、退出码、
回滚方式和「不要回显 token / 不要手改真实配置」这类约束。

## 开发

```bash
go test ./...
go build -o hmck .
```
