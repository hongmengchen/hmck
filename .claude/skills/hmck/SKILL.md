---
name: hmck
description: 管理这台机器上的一组命令行小工具（kube-token、npm-token、glab 及以后新增的，也包括 hmck 自己），看已装/最新版本和登录状态、一键安装或升级、直接启动、卸载，并给每个工具装 agent skill。当用户说「hmck」「升级我的小工具」「hmck 怎么升级自己」「看看有没有新版本」「装一下 kube-token / npm-token / glab」「glab 登没登录」「卸载某个工具 / 卸载 hmck」「自动更新」「新增一个工具让 hmck 管理」「给 agent 配置 skill」时使用。包含 hmck 的 CLI/TUI 用法、信息页、卸载、自我升级、自动下载/安装开关、自定义工具接入方式与安全约束。
---

# hmck

`hmck` 是这台机器上的工具管理器：内置 kube-token、npm-token、glab 以及它自己，也能把以后的新工具加进来。
它知道每个工具的已安装版本、最新版本、安装路径，能安装、升级、直接启动、卸载，
还会顺手把各工具的 agent skill 刷新到全局目录；对 glab 这类工具，它还能读出登录状态。

**agent 一律走 CLI，不要启动 TUI。** `hmck`（无参数）会进全屏 TUI，需要真终端；
CLI 的每个动作都有对应命令，输出也可以 `--json`。

## 铁律

1. **不要回显任何 token。** hmck 本身不碰密钥，但它管理的工具会；调用它们时输出里只会有掩码。
   `hmck info glab` 只跑 `glab auth status`，输出里也只有主机、账号、凭证来源，不会打印 token。
2. **不要擅自改用户的真实配置。** `~/.npmrc`、`~/.kube/config` 的写入交给 kube-token /
   npm-token 自己做（它们会自动备份、复验、必要时还原），agent 不要手改文件。
3. **升级和卸载只走 hmck。** 不要用 `go install`、手动下载二进制或改 PATH 来「修好」某个工具，
   也不要手动 `rm` 来卸载；`hmck upgrade` 会校验 checksum 并原子替换，`hmck uninstall` 只删 hmck 装出来的那份。
4. **不确定用户要不要装/升级/卸载时先问。** 这些动作会写或删用户的 `~/.local/bin`
   （Windows 是 `%USERPROFILE%\.local\bin`，或工具当前所在位置），属于会改系统的动作；看版本、列清单、看信息则随便跑。

## 1. 看现状

```bash
hmck list              # 纯文本表格：NAME / 已安装 / 最新 / 状态
hmck list --json       # 给脚本/agent 解析
hmck info glab         # 一个工具的详情，包括 glab 的登录状态（--json 给脚本）
hmck version
```

`hmck info <tool>` 是「这个工具现在怎么样」：先一行总结（`✓ 已登录 alice@git.example.com`、
`○ 还没登录`、`✓ 已是最新`、`可升级 1.2.3`），再列字段。glab 这类能读登录状态的工具，
字段是：主机、账号、凭证来源（系统钥匙串 / 配置文件 / 环境变量）、传输（`git ssh · API https`）、
REST API、GraphQL、其他登录过的主机；之后每个工具都有版本、最新、位置、更新时间、
PATH 上的其他副本和仓库。只跑 `glab auth status`，不会读 token 本身。

`list --json` 每条形如：

```json
{
  "key": "npm-token",
  "name": "npm-token",
  "installed": "0.1.0",
  "latest": "0.1.1",
  "path": "~/.local/bin/npm-token",
  "status": "可升级 0.1.1",
  "upgradable": true,
  "updated_at": "2026-11-15T21:12:03+08:00"
}
```

`installed` 为空表示没装；`latest` 为空表示没查到（`error` 里写原因，常见是网络或 API 限流）。
`updated_at` 是上一次版本更新时间：hmck 装/升级成功时记进 `state.json`；不是 hmck 装的，用二进制文件的修改时间。
版本检测：已装版本读 `<tool> version`；最新版看 GitHub `releases/latest` 的跳转（GitLab 工具看
`releases/permalink/latest`），失败再退回 API。

## 2. 安装 / 升级 / 启动

```bash
hmck install npm-token          # 没装就装最新版
hmck install npm-token --force  # 已是最新也重装
hmck upgrade                    # 升级所有可升级的，包括 hmck 自己
hmck upgrade hmck               # 只升级 hmck 自己
hmck upgrade npm-token          # 只升一个
hmck run npm-token --help       # 直接启动工具，后面的参数原样传给工具
```

`hmck` 自己也在 `registry` 里（`Self: true`）：已装版本用编译进二进制的版本号，
目标路径是当前正在运行的可执行文件，和别的工具走同一套下载、checksum 校验、原子替换。
`hmck run hmck` 会被拒绝——直接运行 `hmck` 即可。

- 安装目录统一为 `~/.local/bin`（Windows 是 `%USERPROFILE%\.local\bin`）；工具已经在 PATH 里就在原位置升级。旧版 Windows 装在 `%LOCALAPPDATA%\<tool>` 的，第一次运行新版 hmck 会自动搬到统一目录并清理旧 PATH 项，配置和 skill 不动。
- 升级：下载到同目录临时文件，有 `checksums.txt` 就校验 SHA-256，再原子 rename；
  校验或下载失败不会动原文件。
- 装完/升级完会跑工具声明的 `post_install`（目前是 `skill --quiet`），agent 的说明跟着更新。
- `hmck run` 会透传退出码，脚本里可以直接用。

TUI（只有用户自己在终端里用）：`↑↓` 选择，`enter` 启动（没装则安装；选到 hmck 自己是升级），
`u` 升级选中的，`U` 一键升级所有可升级的，`i` 看信息页，`g` 看更新日志，`d` 卸载，
`s` 装/刷新 agent skill，`r` 重新检查，`q` 退出。
卡片上版本号前的图标就是状态：`✓` 已是最新、`↑` 可更新（`旧 → 新`）、`↓` 已下载待安装、
`○` 未安装、`!` 查不到最新版或没有对应平台的发布。第一行右边是上次版本更新时间。
每张卡片左边是该工具自己的招牌，第三行是仓库、登录状态（glab 这类）和最近一版的第一条说明
（启动时抓 `CHANGELOG.md`，失败就只显示仓库名）。

glab 在 brew / apt 装的人多，hmck 只认自己目录里的那一份（`OwnDir`）：PATH 上别人装的副本
不接管、不删，卡片上注明「PATH 上已有一份」并显示那份的版本号。装不装、卸不卸都看用户的意思。

## 卸载

```bash
hmck uninstall glab               # 删掉 hmck 装的那一份
hmck uninstall glab --keep-skill  # 二进制删掉，agent skill 留着
```

删的是：二进制（`~/.local/bin/<tool>`）、带 `SKILL.md` 的 `~/.claude/skills/<tool>/`、
只属于它的预下载缓存、它的更新时间记录。不删：PATH 上别的副本（结果里说明「不是 hmck 装的」）、
自定义工具的配置（要一起删用 `hmck rm`）、`~/.npmrc` / kubeconfig 这类工具自己的配置。

hmck 自己也能卸（TUI 里选到 hmck 按 `d`，或 `hmck uninstall hmck`）：删的是正在运行的
那个可执行文件，Windows 上删不掉就先改名成 `.old`。卸载自己后进程还活着，TUI 结果页
按任意键就退出。没有 hmck 装的那份时（比如只有 brew 的 glab），命令返回非零并说明
「hmck 目录里没有 <tool>」，不会假装删掉了。

CLI 的退出码：0 成功，1 没删成功（会写原因），2 参数不对。

## 看更新日志

```bash
hmck changelog kube-token             # 最近 5 个版本的说明
hmck changelog kube-token --versions 2
```

输出是纯文本：`vX.Y.Z  日期  当前/最新`，下面按「新增 / 变更 / 修复 / 安全」小节列条目。
数据优先读仓库 `main` 分支的 `CHANGELOG.md`，没有就退到 release notes（GitHub 工具用 GitHub API，
GitLab 工具如 glab 用 GitLab API）；
带 `GH_TOKEN` / `GITHUB_TOKEN` 时会附上，避免 API 限流。用户问「最近改了什么」「这个版本有什么新东西」
时用它，不要去猜。

列表上方有两个开关，按 `a` / `A` 切换并立即写进配置：

| 开关 | 键 | 行为 |
| --- | --- | --- |
| 自动下载最新版 | `a` | 每次检查完，把可更新的版本下载并校验到缓存，卡片显示「已下载，按 u 安装」 |
| 自动安装最新版 | `A` | 检查完直接下载并替换（隐含自动下载；关掉自动下载会一起关掉） |

配置写在 `hmck/config.json`（和 `tools.json` 同目录）。自动行为只在 TUI 里生效，
`hmck list --json` 等脚本用途不会偷偷改动系统。

## 3. 让 agent 读懂这些工具

```bash
hmck skill              # 写 hmck 自己的 skill，并刷新所有已安装工具的 skill
hmck skill --print      # 只看 hmck 的 SKILL.md
hmck skill --no-tools   # 只装 hmck 自己的
```

写入位置：`~/.claude/skills/<tool>/SKILL.md`（可用 `-dir` 换根目录）。内容随二进制走，
工具升级时由新二进制自己刷新，所以不会出现「文档比程序旧」。

## 4. 以后新增一个工具

**首选：不用重新编译 hmck**，直接加一条本机配置：

```bash
hmck add owner/repo                  # 自动推断 key、平台、有没有 checksums.txt
hmck add owner/repo --key my-tool --desc "一句话说明" --skill
hmck add https://github.com/owner/repo
hmck rm my-tool                      # 只删自定义工具
```

配置存在：

| 系统 | 路径 |
| --- | --- |
| macOS | `~/Library/Application Support/hmck/tools.json`、`config.json`、`state.json` |
| Linux | `~/.config/hmck/tools.json`、`config.json`、`state.json` |
| Windows | `%AppData%\hmck\tools.json`、`config.json`、`state.json` |

`state.json` 只记每个工具上一次版本更新的版本号和时间（`{"<key>": {"version": "...", "updated_at": "..."}}`），
删了不影响使用，下次升级会重新写。

加完立刻可以 `hmck install <key>` / `hmck upgrade` / `hmck list` / TUI 里管理。

**工具仓库需要满足的约定：**

1. release tag 形如 `v1.2.3`，资产名 `<key>_<goos>_<goarch>`（Windows 加 `.exe`），
   如 `my-tool_linux_arm64`、`my-tool_windows_amd64.exe`。
2. 可选但推荐：发布 `checksums.txt`（`sha256sum * > checksums.txt`），hmck 会校验。
3. 可选：实现 `<tool> skill [--quiet] [--print] [--dir DIR]` 子命令，并在仓库里放
   `.claude/skills/<key>/SKILL.md` 用 `//go:embed` 打进二进制；这样 `hmck skill`
   和安装后的自动刷新都能覆盖它。`hmck add` 时加 `--skill` 标记。
4. `<tool> version` 的输出里要有一个像版本号的词（`1.2.3` / `v1.2.3`），hmck 找第一个；
   都不像才退回取最后一个字段。（glab 的输出是 `glab 1.130.0 (9b430a11f)`，取最后一个会拿到 commit 号。）

**备选：写进内置列表。** 在 `tools.go` 的 `registry` 加一条，发新版 hmck。
内置工具 key 优先于自定义配置，不能被 `hmck add` 覆盖、也不能 `hmck rm`。
发布在 GitLab 的加 `Host: "gitlab"`；资产名不规范用 `Asset` 模板、压缩包开 `Archive`；
只接管自己装的那份用 `OwnDir`；能读登录状态的用 `Status`（目前只有 glab）。

## 5. 在本仓库里改代码时

- 版本号在 `main.go` 的 `var version`；打 tag（`git tag vX.Y.Z && git push origin vX.Y.Z`）
  触发 `.github/workflows` 构建六平台二进制并发布。
- 改完跑 `gofmt -l .`、`go vet ./...`、`go test ./...`、`go build -o hmck .`。
- TUI 测试可以写 pty 脚本（Bubble Tea 需要真终端），单元测试不要碰网络：
  `latestPageURL` / `releaseAssetURL` / `releaseAPIURL` 都可以换成 `httptest` 地址。
- 不要往仓库里写公司内部信息（真实 Vault/registry 地址、角色、用户名、token）。
  示例统一用 `git.example.com`、`owner/repo`、`alice` 这类假值。

## 6. 常见问题

| 现象 | 原因 / 处理 |
| --- | --- |
| `查不到最新版` / API 限流 | 网络或 GitHub 限流；稍后 `hmck list` 重查 |
| `xxx 不可写（程序在运行？）` | 目标目录权限不足；换 `-b` 目录安装或关闭正在运行的程序 |
| `校验失败` | 下载内容与 checksums.txt 不符，原文件未动；重试或检查代理 |
| `没有 xx/yy 的发布` | 该工具没发这个平台，或 `hmck add` 推断的平台和实际资产名不一致 |
| `自定义工具配置有问题` | `tools.json` 不是合法 JSON / 缺 repo；按提示修或删掉该条 |
| `开关没保存` | `config.json` 所在目录不可写或文件不是合法 JSON；按提示修 |
| 自动下载了但没装 | 这是「自动下载」；按 `u` / `U` 安装，或打开「自动安装」 |
| hmck 升级后还是旧版本 | 正在运行的是旧进程；退出重开，或 `hmck version` 确认磁盘上的新版本 |
| 装了却找不到命令 | 安装目录不在 PATH；hmck 仍能启动它，用户重开终端或把目录加进 PATH |
| 工具没有 skill | 版本太旧（还没实现 `skill` 子命令）；先 `hmck upgrade <tool>` |
| glab 显示「还没登录」 | 在终端里跑 `glab auth login`；`GITLAB_TOKEN` 这类环境变量只在当前 shell 里算数 |
| glab 显示「读不出登录状态」 | glab 版本太旧，没有 `auth status`；`hmck install glab` 装自己那份 |
| 卸载说「hmck 目录里没有 xx」 | 只有 PATH 上别人装的那份（brew 等），不归 hmck 删；结果里会列出它在哪 |
| 卸载 hmck 后命令还在 | shell 缓存了路径；`hash -r` 或新开终端 |
