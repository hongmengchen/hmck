---
name: hmck
description: 管理这台机器上的一组命令行小工具（kube-token、npm-token、argo-token、glab、kubectl、argocd 及以后新增的，也包括 hmck 自己），看已装/最新版本、登录状态、集群连通性、Argo token 还能不能用、一键安装或升级、直接启动、卸载，并给每个工具装 agent skill。当用户说「hmck」「升级我的小工具」「hmck 怎么升级自己」「看看有没有新版本」「装一下 kube-token / npm-token / glab / kubectl」「glab 登没登录」「kubectl 连不连得上 / 当前 context 是什么」「Argo token 还有效吗 / argocd 登录状态」「卸载某个工具 / 卸载 hmck」「自动更新」「多端同步配置 / hmck sync」「新增一个工具让 hmck 管理」「hmck 都能装什么 / 工具目录」「给 agent 配置 skill」时使用。包含 hmck 的 CLI/TUI 用法、信息页、卸载、自我升级、自动下载/安装开关、自定义工具接入方式与安全约束。
---

# hmck

`hmck` 是这台机器上的工具管理器：内置 kube-token、npm-token、argo-token、glab、kubectl、argocd 以及它自己，也能把以后的新工具加进来。
列表按三栏排：**官方 CLI**（glab、kubectl、argocd，hmck 帮你看版本、装、升级）、
**自己的工具**（kube-token、npm-token、argo-token、hmck，除了版本还能直接打开用）、
**自己加的**（本机 `hmck add` 进来的）；`hmck list --json` 每条带 `kind`（`official` / `own` / `added`）。
它知道每个工具的已安装版本、最新版本、安装路径，能安装、升级、直接启动、卸载，
还会顺手把各工具的 agent skill 刷新到全局目录；对 glab 这类工具能读出登录状态，对 kubectl 能读出当前 context 和集群连通性，对 argo-token 能拿官方 argocd CLI 验已有 token 还能不能用。

**agent 一律走 CLI，不要启动 TUI。** `hmck`（无参数）会进全屏 TUI，需要真终端；
CLI 的每个动作都有对应命令，输出也可以 `--json`。

## 铁律

1. **不要回显任何 token。** hmck 本身不碰密钥，但它管理的工具会；调用它们时输出里只会有掩码。
   `hmck info glab` 只跑 `glab auth status`，输出里也只有主机、账号、凭证来源，不会打印 token；
   `hmck info kubectl` 只跑 `kubectl config view --minify` 和 `kubectl version -o json`，不读 kubeconfig 里的证书和 token；
   `hmck info argo-token` 只跑 `argocd account get-user-info`（只读），验证时 token 只走子进程的
   `ARGOCD_AUTH_TOKEN` 环境变量（不进参数、不打印），`--config` 指向临时空路径，不碰 `argocd login` 的 context。
2. **不要擅自改用户的真实配置。** `~/.npmrc`、`~/.kube/config` 的写入交给 kube-token /
   npm-token 自己做（它们会自动备份、复验、必要时还原），agent 不要手改文件；
   argo-token 的 token 在 `~/.argocd/token`（0600），配置和 token 都交给它的 TUI / `set` 写，agent 不要 `cat`。
   `hmck sync` 把配置加密同步到 GitHub 私有仓库，本机 `sync.json`（0600）里是派生后的密钥，
   同样不要读、不要打印；同步密码只从用户手里拿，必要时用 `--passphrase-stdin` 传，别写进命令行参数或文件。
3. **升级和卸载只走 hmck。** 不要用 `go install`、手动下载二进制或改 PATH 来「修好」某个工具，
   也不要手动 `rm` 来卸载；`hmck upgrade` 会校验 checksum 并原子替换，`hmck uninstall` 只删 hmck 装出来的那份，
   且要敲工具名确认——非交互环境只有用户明确同意后才加 `--yes`。
4. **不确定用户要不要装/升级/卸载时先问。** 这些动作会写或删用户的 `~/.local/bin`
   （Windows 是 `%USERPROFILE%\.local\bin`，或工具当前所在位置），属于会改系统的动作；看版本、列清单、看信息则随便跑。

## 1. 看现状

```bash
hmck list              # 纯文本表格：NAME / 已安装 / 最新 / 状态
hmck list --json       # 给脚本/agent 解析
hmck catalog           # 在线工具目录：hmck 能装什么，带本机状态
hmck catalog --json    # 给脚本/agent 解析
hmck info glab         # 一个工具的详情，包括 glab 的登录状态（--json 给脚本）
hmck info kubectl      # 当前 context、API server、服务端版本、连不连得上
hmck info argo-token   # Argo 服务器、token 还在不在、官方 argocd CLI 说它还能不能用
hmck version
```

`hmck info <tool>` 是「这个工具现在怎么样」：先一行总结（`✓ 已登录 alice@git.example.com`、
`✓ 已连通 eks-prod`、`✓ token 可用（alice）`、`○ 还没登录`、`✓ 已是最新`、`可升级 1.2.3`），再列字段。glab 这类能读登录状态的工具，
字段是：主机、账号、凭证来源（系统钥匙串 / 配置文件 / 环境变量）、传输（`git ssh · API https`）、
REST API、GraphQL、其他登录过的主机；kubectl 的字段是：当前 context、集群、API server、
服务端版本、连通性，连不上时还有一句说明（token 过期 / 超时 / 地址不通）；
argo-token 的字段是：服务器、账号、签发方、token 文件（只报路径和有没有，不报内容）、grpc-web、
验证结果——`✓ token 可用` / `! token 不可用`（服务器拒了，重新运行 argo-token）/ `验证没通过：连接超时 / 连不上服务器` /
`没跑：本机没有 argocd CLI`（`hmck install argocd` 之后就能验）；
之后每个工具都有版本、最新、位置、更新时间、PATH 上的其他副本和仓库。
hmck 只跑 `glab auth status`、`kubectl config view --minify`、`kubectl version -o json` 和
`argocd account get-user-info`，不会读 token、证书本身。

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
  "updated_at": "2026-11-15T21:12:03+08:00",
  "kind": "own"
}
```

`installed` 为空表示没装；`latest` 为空表示没查到（`error` 里写原因，常见是网络或 API 限流）。
`updated_at` 是上一次版本更新时间：hmck 装/升级成功时记进 `state.json`；不是 hmck 装的，用二进制文件的修改时间。
版本检测：已装版本读 `<tool> version`（kubectl 用 `version --client`，不连集群）；最新版看 GitHub `releases/latest` 的跳转（GitLab 工具看
`releases/permalink/latest`），失败再退回 API。kubectl 走 `dl.k8s.io`：最新版读 `stable.txt`，资产按平台分目录下载，
校验的是资产旁边同名的 `.sha256`。

`hmck catalog` 看的是**在线工具目录**（`hongmengchen/hmck` 仓库的 `docs/catalog.json`）：hmck 能装的所有工具，
每条带本机状态（版本号 / 未添加 / 未安装 / 本平台没有 / PATH 上已有一份）。它只查本机，不联网查版本。
线上读不到时用编进程序的内置副本，stderr 会说清原因；`HMCK_CATALOG_URL` 可以换地址。
`catalog --json` 每条形如 `{"key","name","desc","repo","host","platforms","managed","installed","foreign","available","source"}`。

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

TUI（只有用户自己在终端里用）：列表按「官方 CLI / 自己的工具 / 自己加的」三栏排，每栏一个标题带个数；
`↑↓` 选择（跨栏照常选），`enter` 启动（没装则安装；选到 hmck 自己是升级），
`n` 打开工具目录（每行一个 hmck 能装的工具，右边是本机状态：版本号 / 未添加 / 未安装 / 本平台没有，
`enter` 加入并安装，已加入的直接装或升级，`r` 重新拉目录；目录里按 `a` 才是按仓库新增，填仓库、key、说明），
`u` 升级选中的，`U` 一键升级所有可升级的，`i` 看信息页，`g` 看更新日志，`d` 卸载，
`s` 装/刷新 agent skill，`r` 重新检查，`q` 退出。
卡片上版本号前的图标就是状态：`✓` 已是最新、`↑` 可更新（`旧 → 新`）、`↓` 已下载待安装、
`○` 未安装、`!` 查不到最新版或没有对应平台的发布。第一行右边是上次版本更新时间。
每张卡片左边是该工具自己的招牌：`docs/logos/*.svg`（glab 是 GitLab 官方 press kit 的狸猫）
编进程序，运行时光栅成 12×6 像素、折成 6 列 × 3 行的象限字符（`▘▀▖▌█` 这类实心方块），
颜色取 SVG 原色（argo-token 是琥珀色钥匙）；造型是照终端网格手摆的像素画（画布 24 单位宽、24 单位高，一个取样点
2 单位宽 4 单位高），不是缩小的普通矢量图。自定义工具没有 SVG，退回方块字首字母。招牌右边三行依次是名称和版本、作用 / 装在哪、仓库 / 登录状态 / 最近一版
的第一条说明（启动时抓 `CHANGELOG.md`，失败就只显示仓库名）。

glab 在 brew / apt 装的人多、kubectl 常跟着 Docker Desktop 来，hmck 只认自己目录里的那一份（`OwnDir`）：
PATH 上别人装的副本不接管、不删，卡片上注明「PATH 上已有一份」并显示那份的版本号，`enter` 就是启动那一份。
hmck 自己那份优先（在 PATH 前面），没有也不另装——两个 glab 并存会把人和 agent 一直在用的
那份顶掉。这种工具按 `u` 只说明它归谁管、该用谁升级。装不装、卸不卸都看用户的意思。
状态照样读：glab 看登录，kubectl 看集群连通性。

## 卸载

```bash
hmck uninstall glab               # 删掉 hmck 装的那一份
hmck uninstall glab --keep-skill  # 二进制删掉，agent skill 留着
hmck uninstall glab --yes         # 不再确认，直接删（脚本 / agent 用）
```

卸载要二次确认，免得手滑删掉：TUI 里把工具名敲一遍再按 `enter`；CLI 先打印要删的清单，
再输入工具名回车，敲错或直接回车都算取消。**agent 在非交互环境里跑 `hmck uninstall` 不会停下来问，
而是直接拒绝**；确实要删（用户已明确同意）就加 `--yes`，不要绕道手动 `rm`。

删的是：二进制（`~/.local/bin/<tool>`）、带 `SKILL.md` 的 `~/.claude/skills/<tool>/`、
只属于它的预下载缓存、它的更新时间记录。不删：PATH 上别的副本（结果里说明「不是 hmck 装的」）、
自定义工具的配置（要一起删用 `hmck rm`）、`~/.npmrc` / kubeconfig 这类工具自己的配置。

hmck 自己也能卸（TUI 里选到 hmck 按 `d`，或 `hmck uninstall hmck`）：删的是正在运行的
那个可执行文件，Windows 上删不掉就先改名成 `.old`。卸载自己后进程还活着，TUI 结果页
按任意键就退出。没有 hmck 装的那份时（比如只有 brew 的 glab），命令返回非零并说明
「hmck 目录里没有 <tool>」，不会假装删掉了。

CLI 的退出码：0 成功，1 没删成功或取消（会写原因），2 参数不对（包括非交互时没加 `--yes`）。

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

## 多端同步

```bash
hmck sync                    # 状态：仓库、是否已记住密钥、上次推送/拉取、设备、同步清单
hmck sync --json             # 给脚本/agent 解析（含 device_id、devices）
hmck sync push               # 加密推到 GitHub 私有仓库；第一次自动建 <用户名>/hmck-config
hmck sync pull               # 另一台机器拉下来（--dry-run 逐文件预览文本差异）
hmck sync add ~/.npmrc       # 额外同步一个文件；hmck sync rm 去掉
hmck sync off                # 本机忘掉仓库和密钥，远端数据不动
```

TUI 列表上按 `y` 就是同一套能力的同步页：看状态、推送/拉取/预览、刷新设备列表、管理各工具同步、强制覆盖、
`sync add` 的路径、忘掉本机密钥；本机没记住密钥时密码直接在页面里掩码输入（首次推送输两遍），
只用于这一次请求。逐工具开关在首次连接前也能设置；偏好只保存在本机 `sync.json`，关闭后本机双向跳过该工具，
远端已有数据保留。agent 仍然走上面的 CLI，不要启动 TUI。
`hmck sync pull --dry-run` 和 TUI 预览逐文件显示文本差异，不写本机文件或同步状态；TUI 预览页可滚动。
常见凭据文件及含疑似凭据的文件不展示差异内容，二进制文件也不展示内容；终端控制字符会转义。
本机冲突会提示普通 pull 将拒绝覆盖，需要明确加 `--force` 才会应用。
设备表：每台机器有自己的固定 `device_id`（sync.json 里的 `device_id`），主机名、系统、最近一次
push/pull 时间跟着加密数据走；`hmck sync --json` 的 `devices` 就是它，本机那条的 id 等于
`device_id`。设备表只说明谁参与同步，不代表该设备现在在线。
登录借 gh CLI（`gh auth token`），没登录先让用户跑 `gh auth login`；hmck 不保存 GitHub 凭据。
本机没装或没登录 gh 时，同步页最前面会先给「安装 GitHub CLI」「登录 GitHub CLI」——安装走
winget / scoop / choco / Homebrew / apt / dnf / pacman，没有包管理器就打开 cli.github.com，
`r` 重新检测。这是给人在 TUI 里用的，agent 不要替用户装软件或登录。
加密是 PBKDF2-HMAC-SHA256（60 万次）+ AES-256-GCM，GitHub 上只有密文。
同步内容：hmck 的 `config.json` / `tools.json`，各工具声明的配置（kube-token、npm-token、
argo-token 的配置和 `~/.argocd/token`），以及用户 `sync add` 的路径。
跨系统只带数据本身：配置里的路径字段（argo-token 的 `token_file`、npm-token 的 `file`）
推上去写成 `home:…` / `config:…`，拉下来再展开成本机路径；纯本机的东西（npm-token 的
`updated_at`、argo-token 生成的 `~/.argocd/env`）不同步。

- **保存后自动推**：`hmck run <工具>` 退出后、TUI 改完配置或增删工具后自动推一次，没变就跳过；
  失败只提示，不影响原命令退出码。同步没开启时静默。
- **拉取是手动的**，agent 不要自动 `pull`：它会覆盖本机文件。本机有未推改动时 pull 会拒绝，
  `--force` 才覆盖——这属于改用户配置，先问。
- push 会把远端独有的文件合进来（同名以本机为准），远端被别的设备推过新版本时会要求先 pull；
  `--force` 同理要先问。
- 非交互环境输密码用 `--passphrase-stdin`（从 stdin 读一行），不要把它放进参数。

## 3. 让 agent 读懂这些工具

```bash
hmck skill              # 写 hmck 自己的 skill，并刷新所有已安装工具的 skill
hmck skill --print      # 只看 hmck 的 SKILL.md
hmck skill --no-tools   # 只装 hmck 自己的
```

写入位置：`~/.claude/skills/<tool>/SKILL.md`（可用 `-dir` 换根目录）。内容随二进制走，
工具升级时由新二进制自己刷新，所以不会出现「文档比程序旧」。

## 4. 以后新增一个工具

**第一选择：加进线上工具目录，所有人都能用，不用发新版 hmck。** 在 hmck 仓库的
`docs/catalog.json`（`main` 分支）的 `tools` 里加一条——hmck 打开目录页或跑 `hmck catalog`
时现拉这份文件：

```json
{"key": "my-tool", "name": "my-tool", "desc": "一句话说明", "repo": "owner/my-tool",
 "platforms": ["darwin/arm64", "linux/amd64", "windows/amd64"], "checksums": true}
```

`key`、`repo` 必填；其余可选：`name`、`desc`、`host`（`github` 默认、`gitlab`、`kubernetes`）、
`platforms`（不写按六个常见平台记）、`checksums`、`checksums_file`（校验文件的名字，默认
`checksums.txt`；argo-cd 这类用 `cli_checksums.txt`）、`asset`（资产名模板，`{ver}`/`{goos}`/`{goarch}`/`{ext}`）、
`archive`、`bin_path`、`skill`、`post_install`、`version_args`。
单条写坏只跳过那一条并计数，不影响整份目录；但内置工具的 key 一律以程序里的定义为准，
目录里怎么写都改不了它从哪装、怎么校验。线上拉不到（离线、GitHub 不通）时 hmck 退回编进程序的
同一份内置副本并在页面上注明；`HMCK_CATALOG_URL` 可指向自己的镜像。

**只给这台机器用：写进本机配置。** 用户自己在终端里时，工具目录页按 `a`
就能填仓库、key、说明，`enter` 存进配置并接着安装；让 agent 代做时用：

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
只接管自己装的那份用 `OwnDir`；能读状态的用 `Status`，再看 `StatusSource` 选怎么读
（不写是 glab 那种登录状态，`kubectl` 看集群，`argocd` 拿官方 CLI 验 token，如 argo-token）；
`Kind` 决定列表归到哪一栏：别人的官方 CLI 写 `kindOfficial`，自己的小工具写 `kindOwn`
（kube-token / npm-token / argo-token / hmck），本机 `hmck add` 的自动算 `kindAdded`。

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
| glab 显示「读不出登录状态」 | glab 版本太旧，没有 `auth status`；升级 PATH 上那份（`brew upgrade glab` 之类），别另装一个 |
| 卸载说「hmck 目录里没有 xx」 | 只有 PATH 上别人装的那份（brew 等），不归 hmck 删；结果里会列出它在哪 |
| 卸载 hmck 后命令还在 | shell 缓存了路径；`hash -r` 或新开终端 |
