---
name: hmck
description: 管理这台机器上的一组命令行小工具（kube-token、npm-token 及以后新增的，也包括 hmck 自己），看已装/最新版本、一键安装或升级、直接启动，并给每个工具装 agent skill。当用户说「hmck」「升级我的小工具」「hmck 怎么升级自己」「看看有没有新版本」「装一下 kube-token / npm-token」「自动更新」「新增一个工具让 hmck 管理」「给 agent 配置 skill」时使用。包含 hmck 的 CLI/TUI 用法、自我升级、自动下载/安装开关、自定义工具接入方式与安全约束。
---

# hmck

`hmck` 是这台机器上的工具管理器：内置 kube-token、npm-token、以及它自己，也能把以后的新工具加进来。
它知道每个工具的已安装版本、GitHub 最新版本、安装路径，能安装、升级、直接启动，
还会顺手把各工具的 agent skill 刷新到全局目录。

**agent 一律走 CLI，不要启动 TUI。** `hmck`（无参数）会进全屏 TUI，需要真终端；
CLI 的每个动作都有对应命令，输出也可以 `--json`。

## 铁律

1. **不要回显任何 token。** hmck 本身不碰密钥，但它管理的工具会；调用它们时输出里只会有掩码。
2. **不要擅自改用户的真实配置。** `~/.npmrc`、`~/.kube/config` 的写入交给 kube-token /
   npm-token 自己做（它们会自动备份、复验、必要时还原），agent 不要手改文件。
3. **升级只走 hmck。** 不要用 `go install`、手动下载二进制或改 PATH 来「修好」某个工具；
   `hmck upgrade` 会校验 checksum 并原子替换，路径也保持一致。
4. **不确定用户要不要装/升级时先问。** 安装和升级会写用户的 `~/.local/bin`
   （Windows 是 `%USERPROFILE%\.local\bin`，或工具当前所在位置），属于会改系统的动作；看版本、列清单则随便跑。

## 1. 看现状

```bash
hmck list              # 纯文本表格：NAME / 已安装 / 最新 / 状态
hmck list --json       # 给脚本/agent 解析
hmck version
```

`--json` 每条形如：

```json
{
  "key": "npm-token",
  "name": "npm-token",
  "installed": "0.1.0",
  "latest": "0.1.1",
  "path": "~/.local/bin/npm-token",
  "status": "可升级 0.1.1",
  "upgradable": true
}
```

`installed` 为空表示没装；`latest` 为空表示没查到（`error` 里写原因，常见是网络或 API 限流）。
版本检测：已装版本读 `<tool> version`；最新版看 GitHub `releases/latest` 的跳转，失败再退回 API。

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
`u` 升级选中的，`U` 一键升级所有可升级的，`s` 装/刷新 agent skill，`r` 重新检查，`q` 退出。
卡片上版本号前的图标就是状态：`✓` 已是最新、`↑` 可更新（`旧 → 新`）、`↓` 已下载待安装、
`○` 未安装、`!` 查不到最新版或没有对应平台的发布。

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
| macOS | `~/Library/Application Support/hmck/tools.json`、`config.json` |
| Linux | `~/.config/hmck/tools.json`、`config.json` |
| Windows | `%AppData%\hmck\tools.json`、`config.json` |

加完立刻可以 `hmck install <key>` / `hmck upgrade` / `hmck list` / TUI 里管理。

**工具仓库需要满足的约定：**

1. release tag 形如 `v1.2.3`，资产名 `<key>_<goos>_<goarch>`（Windows 加 `.exe`），
   如 `my-tool_linux_arm64`、`my-tool_windows_amd64.exe`。
2. 可选但推荐：发布 `checksums.txt`（`sha256sum * > checksums.txt`），hmck 会校验。
3. 可选：实现 `<tool> skill [--quiet] [--print] [--dir DIR]` 子命令，并在仓库里放
   `.claude/skills/<key>/SKILL.md` 用 `//go:embed` 打进二进制；这样 `hmck skill`
   和安装后的自动刷新都能覆盖它。`hmck add` 时加 `--skill` 标记。
4. 新工具的版本号要能被 `<tool> version` 输出（最后一个字段当版本号）。

**备选：写进内置列表。** 在 `tools.go` 的 `registry` 加一条，发新版 hmck。
内置工具 key 优先于自定义配置，不能被 `hmck add` 覆盖、也不能 `hmck rm`。

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
