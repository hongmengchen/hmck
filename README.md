# hmck

<p align="center"><img src="docs/logo.svg" width="96" height="96" alt="hmck 标志"></p>
<p align="center">方框是装好的程序，箭头是升到新版本。</p>

[![CI](https://github.com/hongmengchen/hmck/actions/workflows/ci.yml/badge.svg)](https://github.com/hongmengchen/hmck/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/hongmengchen/hmck)](https://github.com/hongmengchen/hmck/releases)
[![License](https://img.shields.io/github/license/hongmengchen/hmck)](LICENSE)

一个入口管理我自己的命令行小工具：看版本、安装、升级、直接启动，顺手把 agent skill 装好。

目前托管：

| 工具 | 作用 |
| --- | --- |
| [kube-token](https://github.com/hongmengchen/kube-token) | 粘贴新 token 替换 kubeconfig 里的旧值 |
| [npm-token](https://github.com/hongmengchen/npm-token) | 检查并更新 `~/.npmrc` 里的 registry token |
| [hmck](https://github.com/hongmengchen/hmck) | 它自己——`hmck upgrade` 会把自己也升到最新 |

以后新增的工具不用改代码：`hmck add owner/repo` 存进本机配置，立刻就能装、能升级。

```
$ hmck
╭───╮  hmck  我的命令行小工具
│ ↑ │  3 个工具 · 1 个可更新 · 检查于 15:42:07
╰───╯  a 自动下载最新版 [关]    A 自动安装最新版 [关]

▌ ╱───╲  kube-token  ✓ 0.8.0                                        10 小时前更新
▌ │ K │     粘贴新 token 替换 kubeconfig 里的旧值                    ~/.local/bin/kube-token
▌ ╲───╱  hongmengchen/kube-token  ·  v0.8.0  Vault 登录缓存：第一次按 v 输入密码…
  ──────────────────────────────────────────────────────────────────────────
│ ┌───┐  npm-token   ✓ 0.5.0                                         3 小时前更新
│ │ N │     检查并更新 ~/.npmrc 里的 registry token                  ~/.local/bin/npm-token
│ └───┘  hongmengchen/npm-token  ·  v0.5.0  npm-token 有了自己的标志…
  ──────────────────────────────────────────────────────────────────────────
│ ╭───╮  hmck        ↑ 0.8.0 → 0.9.0                                    刚刚更新
│ │ ↑ │     管理这些命令行小工具，包括它自己                             ~/.local/bin/hmck
│ ╰───╯  hongmengchen/hmck  ·  v0.9.0  列表里每个工具都带自己的招牌…

  ↑↓ 选择  ·  enter 启动/安装  ·  u 升级  ·  U 全部更新  ·  g 更新日志  ·  s 装 skill
  r 重新检查  ·  q 退出
```

打开后先列出本地工具（版本、安装位置），最新版在后台查询；查到哪个就刷新哪张卡片，查询期间那一格显示转圈，不用对着一个空白的加载页等网络。左上角的招牌和仓库里的 [`docs/logo.svg`](docs/logo.svg) 是同一套几何（方框 + 向上的箭头，也用纯文字画着，不依赖图片和字体）。每个工具占一块，中间用淡线隔开：左边是该工具自己的招牌（kube-token 的六边形 + K、npm-token 的方框 + N、hmck 的方框 + ↑），右边三行分别是「名称和版本」「它干什么 / 装在哪」「仓库和最近一版改了什么」。

第三行的更新日志预览是启动时顺手从各仓库的 `CHANGELOG.md` 抓回来的（没有就退到 release notes），失败也不影响列表。自定义工具用同款方框放名字首字母。

状态只用一个图标表示，不再和版本号重复写「最新」：

| 图标 | 含义 |
| --- | --- |
| `✓` | 已是最新 |
| `↑` | 可更新，显示成 `旧版 → 新版`（新版标黄） |
| `↓` | 新版已下载好，按 `u` 安装 |
| `○` | 没装，按 `enter` 安装 |
| `!` | 查不到最新版，或这台机器没有对应平台的发布 |

第一行最右边是**上一次版本更新时间**：hmck 自己装上/升级成功时记进 `state.json`（和 `config.json` 同目录），显示成「刚刚更新 / 3 小时前更新 / 12 天前更新」，超过一个月给日期。不是 hmck 装的（手动替换、从别处拷过来的），就用二进制文件的修改时间兜底，所以每个装好的工具都有时间可看。

`enter` 直接进对应工具的 TUI，用完退出就回到 hmck；选到 hmck 自己时 `enter` 是升级它；`u` 升级选中的工具；`U` 把所有可更新的工具（包括 hmck 自己）按顺序升一遍，结果页逐个列出 `旧版 → 新版`；没装的按 `enter` 就装；`g` 看选中工具的更新日志；`s` 安装/刷新 agent skill；`r` 重新查一遍版本。

### 更新日志

`g` 打开一页版本说明：左边是所选工具的招牌，右边标出「当前 / 最新」，下面是最近 5 个版本的小节和条目（`↑↓` 滚动、`pgup/pgdn` 翻页、`r` 重新拉取、`esc` 返回）。内容优先读仓库里的 `CHANGELOG.md`（`main` 分支，最全），没有这个文件就退到 GitHub 的 release notes；带 `GH_TOKEN` / `GITHUB_TOKEN` 时请求会带上，免得撞 API 限流。

命令行里也能看，适合脚本和 agent：

```bash
hmck changelog kube-token            # 最近 5 个版本
hmck changelog kube-token --versions 2
```

两个开关用 `a` / `A` 切换，立即生效并写进配置：

| 开关 | 键 | 行为 |
| --- | --- | --- |
| 自动下载最新版 | `a` | 检查完把新版下载并校验到缓存，卡片显示「已下载，按 u 安装」 |
| 自动安装最新版 | `A` | 检查完直接下载并替换；隐含自动下载，关掉自动下载会一起关 |

自动行为只在 TUI 里跑，`hmck list --json` 这类脚本用法不会偷偷改系统。开关存在和 `tools.json` 同目录的 `config.json`。

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

macOS / Linux 默认装到 `~/.local/bin`（root 下是 `/usr/local/bin`）；Windows 装到 `%USERPROFILE%\.local\bin`（Git Bash 里就是 `~/.local/bin`），和 npm-token、kube-token 同一个目录。安装脚本会从 release 的 `checksums.txt` 校验 SHA-256。

安装完会自动跑一次 `hmck skill`，把 agent 用法说明写到 `~/.claude/skills/`。

## 用法

```bash
hmck                      # 打开管理界面
hmck list                 # 纯文本表格；--json 给脚本用
hmck run npm-token        # 直接启动，后面的参数原样传过去
hmck install npm-token    # 安装最新版
hmck upgrade              # 升级所有可升级的工具（包括 hmck 自己）
hmck upgrade hmck         # 只升级 hmck 自己
hmck upgrade npm-token    # 只升一个
hmck upgrade --force      # 已是最新也重装一遍
hmck changelog npm-token  # 看最近几个版本改了什么（--versions N）
hmck skill                # 给 hmck 和已安装的工具装 agent skill
```

版本检测：已安装版本读 `<tool> version`；最新版看 GitHub `releases/latest` 的跳转（不消耗 API 限流），失败再退回 API。

安装位置：统一放在 `~/.local/bin`（Windows 是 `%USERPROFILE%\.local\bin`）；hmck 自己装在系统目录（如 `/usr/local/bin`）时，新工具跟着它放在同一目录。工具已经在 PATH 里就在原位置升级。内置工具的 release 都带 `checksums.txt`，安装和升级会校验 SHA-256。

从旧版升级（Windows 以前装在 `%LOCALAPPDATA%\<tool>`）时，第一次运行新版 hmck 会把二进制搬到统一目录，并把用户 PATH 里的旧目录换成新目录。配置不在安装目录里，`%AppData%\hmck`、`%AppData%\kube-token`、`~/.npmrc` 和 `~/.claude/skills` 都原样保留，不需要重新配置。

升级安全：先下载到缓存目录（`~/Library/Caches/hmck/downloads` 或 `~/.cache/hmck/downloads`），
有 `checksums.txt` 就校验 SHA-256，再复制到目标目录、原子 rename 覆盖；校验或下载失败不会动原文件。
同一个版本下载过一次，之后安装直接复用缓存。

hmck 自己也在托管列表里：已装版本是编译进二进制的版本号，升级路径就是当前正在运行的可执行文件；
替换后旧进程仍是旧版本，退出重开（或 `hmck version`）即可确认。

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
| macOS | `~/Library/Application Support/hmck/tools.json`、`config.json` |
| Linux | `~/.config/hmck/tools.json`、`config.json` |
| Windows | `%AppData%\hmck\tools.json`、`config.json` |

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
gofmt -l .
go vet ./...
go test ./...
go build -o hmck .
```

push / PR 会跑 CI，发版前也会先跑测试。

## 参与

- [CHANGELOG](CHANGELOG.md) · [CONTRIBUTING](CONTRIBUTING.md)
- 安全问题请按 [SECURITY](SECURITY.md) 走私密报告，不要公开贴真实配置或内部地址

## License

本项目采用 [MIT](LICENSE) 协议，Copyright (c) 2026 chenliang。
