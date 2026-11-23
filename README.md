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
| [argo-token](https://github.com/hongmengchen/argo-token) | 配置 Argo CD 服务器和只读 token |
| [glab](https://gitlab.com/gitlab-org/cli) | GitLab 官方 CLI：MR、issue、流水线都在终端里；装了就能看登录状态 |
| [kubectl](https://kubernetes.io/docs/reference/kubectl/) | Kubernetes 命令行（`dl.k8s.io` 发版）：看当前 context、API server、连不连得上 |
| [argocd](https://github.com/argoproj/argo-cd) | Argo CD 命令行：应用、同步、回滚都在终端里 |
| [hmck](https://github.com/hongmengchen/hmck) | 它自己——`hmck upgrade` 会把自己也升到最新 |

能装什么由一份线上目录说了算：hmck 仓库里的 [`docs/catalog.json`](docs/catalog.json)，加个工具只改这份 JSON，不用发新版 hmck。TUI 里按 `n` 打开[工具目录](#工具目录)，挑一个 `enter` 就加入并开装；目录里没有的，还能现场按仓库添加。

列表按三栏排：**官方 CLI**（glab、kubectl、argocd，hmck 帮你看版本、装、升级）、**自己的工具**（kube-token、npm-token、argo-token、hmck，除了版本还能直接打开用）、**自己加的**（本机 `hmck add` 进来的）。`hmck list --json` 的每条也带 `kind`（`official` / `own` / `added`）。

```
$ hmck
▜▙      hmck  我的命令行小工具
  ▌     7 个工具 · 2 个可更新
▟▛ ▗▄▖  a 自动下载最新版 [开]    A 自动安装最新版 [开]

  官方 CLI · 3 个
▌ ▗▙  ▟▖  glab        ↑ 1.130.0 → 1.131.0                                             3 天前更新
▌ ▟▛  ▜▙     GitLab 命令行：MR、issue、流水线都在终端里                        ~/.local/bin/glab
▌ ▝▀  ▀▘  gitlab-org/cli
  ────────────────────────────────────────────────────────────────────────────────────────────────
│ ▌▌▛▌▛▘  kubectl     ✓ 1.37.2                                                        1 天前更新
│ ▛▖▛▌▀▌     Kubernetes 命令行：Pod、日志、集群都在终端里                   ~/.local/bin/kubectl
│ ▘▘▀▘▀▘  kubernetes/kubernetes
  ────────────────────────────────────────────────────────────────────────────────────────────────
│ ▗▟▛▜▙▖  argocd      ✓ 3.6.2                                                       2 小时前更新
│   ▙▟       Argo CD 命令行：应用、同步、回滚都在终端里                      ~/.local/bin/argocd
│ ▝▜▌▌▛▘  argoproj/argo-cd

  自己的工具 · 4 个
│  ▟▀▀▙   kube-token  ✓ 0.8.0                                                      10 小时前更新
│ ▐▌  ▐▌     粘贴新 token 替换 kubeconfig 里的旧值                       ~/.local/bin/kube-token
│  ▜▄▄▛   hongmengchen/kube-token
  ────────────────────────────────────────────────────────────────────────────────────────────────
│   ▛▜    npm-token   ✓ 0.5.0                                                       3 小时前更新
│   ▛▜       检查并更新 ~/.npmrc 里的 registry token                      ~/.local/bin/npm-token
│         hongmengchen/npm-token
  ────────────────────────────────────────────────────────────────────────────────────────────────
│ ▗▄      argo-token  ✓ 0.1.0                                                       1 分钟前更新
│ ▌▐▄▄▄▖     配置 Argo CD 服务器和只读 token                             ~/.local/bin/argo-token
│ ▝▀  ▘▘  hongmengchen/argo-token
  ────────────────────────────────────────────────────────────────────────────────────────────────
│ ▜▙      hmck        ↑ 0.10.6 → 0.10.7                                             1 分钟前更新
│   ▌        管理这些命令行小工具，包括它自己                                  ~/.local/bin/hmck
│ ▟▛ ▗▄▖  hongmengchen/hmck

  ↑↓ 选择  ·  enter 启动/安装  ·  n 工具目录  ·  u 升级  ·  U 全部更新  ·  i 信息  ·  g 更新日志
  d 卸载  ·  s 装 skill  ·  r 重新检查  ·  q 退出
```

打开后先列出本地工具（版本、安装位置），最新版在后台查询；查到哪个就刷新哪张卡片，查询期间那一格显示转圈，不用对着一个空白的加载页等网络。每一栏有个标题，栏内每个工具占一块，中间用淡线隔开：左边是该工具自己的招牌，右边三行分别是「名称和版本」「它干什么 / 装在哪」「仓库和最近一版改了什么」。

招牌是 [`docs/logos`](docs/logos) 里各工具自己的 SVG（glab 用的是 GitLab 官方 press kit 的狸猫）编进程序后，运行时光栅成 12×6 个像素、再折成 6 列 × 3 行的象限字符（`▘▀▖▌▛█` 这些实心方块），颜色取 SVG 里的原色：hmck 是绿色箭头（`>` 当提示符，右边一条下划线，就是 `hmck` 三个字母里那个横）、kube-token 是蓝色六边形框里一颗核心、npm-token 是红挂锁、argo-token 是琥珀色的钥匙、argocd 是橙色圆角块里一个白 A。造型是照着终端网格手摆的像素画，不是把普通矢量图缩下来——缩下来的图在这个尺寸上会糊成一块。终端颜色少的时候由 lipgloss 自己降级，不依赖图片文件、字体或网络；自定义工具和读不出 SVG 的工具，还是退回方块字首字母。

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

`enter` 直接进对应工具的 TUI，用完退出就回到 hmck；选到 hmck 自己时 `enter` 是升级它；`n` 打开工具目录，挑一个 `enter` 加入并安装（已加入的直接装/升级），目录里按 `a` 才是按仓库新增，填仓库（`owner/name` 或 GitHub 链接）、key 和说明，`enter` 存进配置并直接开始安装；`u` 升级选中的工具；`U` 把所有可更新的工具（包括 hmck 自己）按顺序升一遍，结果页逐个列出 `旧版 → 新版`；没装的按 `enter` 就装；`i` 看当前状况（glab 的登录状态、kubectl 的集群状态在这一页）；`g` 看选中工具的更新日志；`y` 打开[多端同步](#多端同步)页；`d` 卸载；`s` 安装/刷新 agent skill；`r` 重新查一遍版本。

终端不高时列表自动换成**紧凑版**：一个工具一行，招牌和分隔线收起来，标题、同步状态和按键提示始终留在屏幕上。工具多到一屏还放不下时，只画光标附近那几行，上下注明「上面/下面还有 N 行」。

### 更新日志

`g` 打开一页版本说明：左边是所选工具的招牌，右边标出「当前 / 最新」，下面是最近 5 个版本的小节和条目（`↑↓` 滚动、`pgup/pgdn` 翻页、`r` 重新拉取、`esc` 返回）。内容优先读仓库里的 `CHANGELOG.md`（`main` 分支，最全），没有这个文件就退到 release notes（GitHub 工具读 GitHub API，glab 这类 GitLab 工具读 GitLab API）；带 `GH_TOKEN` / `GITHUB_TOKEN` 时请求会带上，免得撞 API 限流。

命令行里也能看，适合脚本和 agent：

```bash
hmck changelog kube-token            # 最近 5 个版本
hmck changelog kube-token --versions 2
```

### 自动下载 / 自动安装

两个开关用 `a` / `A` 切换，立即生效并写进配置：

| 开关 | 键 | 行为 |
| --- | --- | --- |
| 自动下载最新版 | `a` | 检查完把新版下载并校验到缓存，卡片显示「已下载，按 u 安装」 |
| 自动安装最新版 | `A` | 检查完直接下载并替换；隐含自动下载，关掉自动下载会一起关 |

自动行为只在 TUI 里跑，`hmck list --json` 这类脚本用法不会偷偷改系统。开关存在和 `tools.json` 同目录的 `config.json`。

### 信息页

`i` 打开一页当前状况，最要紧的排最上面（`esc` 返回）。装了 glab 时第一行就是登录状态：

- `✓ 已登录 alice@git.example.com`——跟着是主机、账号、凭证从哪来（系统钥匙串 / 配置文件 / 环境变量）、git 和 API 各用什么协议、REST 与 GraphQL 的地址；
- `○ 还没登录`——提示在终端里跑 `glab auth login`；
- 登录过多台主机时，其余主机用「其他主机」一行列出来。

装了 kubectl 时第一行是集群状态（只读，不打印 kubeconfig 里的证书和 token）：

- `✓ 已连通 Sought`——跟着是当前 context、集群、API server 地址、服务端版本；
- `! Sought 连不上`——说明是 token 过期、超时还是地址不通（token 过期会提示用 `kube-token` 换新）；
- `○ 没有当前 context`——提示在终端里跑 `kubectl config use-context`。

再往下是每个工具都有的：已装版本、最新版、装在哪、上次更新时间、PATH 上的其他副本、仓库。hmck 只跑 `glab auth status` 和 `kubectl config view` / `kubectl version`，不读 token、证书本身。

```bash
hmck info glab               # 终端里看同一份；--json 给脚本用
hmck info kubectl            # 当前 context、API server、服务端版本、连通性
```

### 卸载

列表里按 `d`（信息页里也行）先进确认页：摊开「删什么、留什么」，再把工具名敲一遍、按 `enter` 才动手，`esc` 算了。敲错名字不会删，只会提示重敲。所有工具都能卸，hmck 自己也能——卸载自己时进程还在，看完结果页按任意键退出。

hmck 只删自己装出来的那一份：

- 二进制（`~/.local/bin/<tool>`；hmck 自己删的是正在运行的那个文件，Windows 上先改名成 `.old`）；
- agent skill（只删带 `SKILL.md` 的 `~/.claude/skills/<tool>/`，`--keep-skill` 可以留着）；
- 只属于它的预下载缓存；
- 它的版本更新时间记录。

不动的：PATH 上别人装的同名程序（brew 的 glab 之类，确认页里列出来并注明「不是 hmck 装的」）、自定义工具的配置（要删用 `hmck rm`）、`~/.npmrc`、kubeconfig 这些工具自己的配置。

```bash
hmck uninstall glab              # 先摊开要删的清单，再敲一遍工具名
hmck uninstall glab --keep-skill # 二进制删掉，skill 留着
hmck uninstall glab --yes        # 不再确认，直接删（脚本 / agent 用）
```

命令行和 TUI 一样要二次确认：打印清单后，输入工具名回车才删，敲错或直接回车都算取消。
stdin 不是终端时（脚本、agent）不会停下来问，也不删，要删必须显式加 `--yes`。

## 工具目录

hmck 能装什么，由仓库里的 [`docs/catalog.json`](docs/catalog.json) 说了算——这份文件在 `main` 分支上，
hmck 每次打开目录页或跑 `hmck catalog` 时现拉，所以加一个工具只用提交这份 JSON，不用等新版 hmck。

TUI 里按 `n`：

- 每行一个工具，右边是本机的状态：版本号、`未添加`、`未安装`、本平台没有包、或 `PATH 上 1.130.0`
- `enter` 就是加入并安装；已经加入过的直接走安装/升级
- `a` 打开按仓库添加的表单，目录里没有的工具走这条
- `r` 重新拉一遍目录，`esc` 回列表

命令行里看同一份（只查本机，不联网）：

```bash
hmck catalog                 # 表格：工具、本机状态、说明、仓库
hmck catalog --json          # 给脚本和 agent 用
```

**目录读不到怎么办**：同一份 JSON 也编进了程序，线上拉不到（离线、GitHub 不通、文件被删）就退回这份
内置副本，并在页面上注明是「内置副本」和拉不到的原因；`HMCK_CATALOG_URL` 可以把地址指到自己的镜像（内网或测试环境）。

**加一条**：在 `tools` 数组里加一项，字段和 `hmck add` 认的一样——`key`、`name`、`desc`、`repo` 必填，
其余可省（`platforms` 不写就按六个常见平台记）：

| 字段 | 作用 |
| --- | --- |
| `key` | 程序名，也是 `hmck install <key>` 用的名字；小写字母、数字、`-` |
| `repo` | `owner/name`；GitLab 工具写 `owner/project` |
| `host` | `github`（默认）、`gitlab`、`kubernetes`（走 `dl.k8s.io`） |
| `name` / `desc` | 列表里显示的名字和一句话说明 |
| `platforms` | `goos/goarch` 列表，本机不在里面就标「本平台没有」，不去下载 |
| `checksums` | 发布里带校验文件时为 `true` |
| `checksums_file` | 校验文件的名字，默认 `checksums.txt`；argo-cd 用 `cli_checksums.txt` |
| `asset` / `archive` | 资产名模板（`{ver}`、`{goos}`、`{goarch}`、`{ext}`），以及要不要解压 |
| `bin_path` | 压缩包里二进制的路径 |
| `skill` / `post_install` | 装完要不要配 agent skill、跑什么后置命令 |
| `version_args` | 问已装版本时传的参数，默认 `version` |

写坏了不会连累整份目录：单条不合法（key 写错、仓库写错、`host` 不认识、跟前面重复）会被跳过并计数，
页面上提示「目录里有 N 条读不懂」。内置工具的 key 一律以程序里的定义为准——目录里写 `glab` 也改不了它从哪装、怎么校验。

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
hmck catalog              # 在线工具目录，带本机状态；--json 给脚本用
hmck run npm-token        # 直接启动，后面的参数原样传过去
hmck install npm-token    # 安装最新版
hmck upgrade              # 升级所有可升级的工具（包括 hmck 自己）
hmck upgrade hmck         # 只升级 hmck 自己
hmck upgrade npm-token    # 只升一个
hmck upgrade --force      # 已是最新也重装一遍
hmck changelog npm-token  # 看最近几个版本改了什么（--versions N）
hmck info glab            # 版本、位置，glab 还有登录状态（--json 给脚本）
hmck info kubectl         # 当前 context、API server、服务端版本、连不连得上
hmck uninstall npm-token  # 卸掉 hmck 装的那份（先敲工具名确认；--yes 跳过）
hmck skill                # 给 hmck 和已安装的工具装 agent skill
hmck sync                 # 多端同步状态（GitHub 私有仓库，加密）
hmck sync push            # 把本机配置加密后推上去；第一次会建仓库、设密码
hmck sync pull            # 换台机器，用同一个密码拉下来
hmck sync add ~/.npmrc    # 额外同步一个文件
```

版本检测：已安装版本读 `<tool> version`（kubectl 用 `version --client`，不连集群）；最新版看 GitHub `releases/latest` 的跳转（不消耗 API 限流），失败再退回 API。kubectl 走 `dl.k8s.io`：最新版读 `stable.txt`，资产按平台分目录下载，校验的是资产旁边同名的 `.sha256`。

安装位置：统一放在 `~/.local/bin`（Windows 是 `%USERPROFILE%\.local\bin`）；hmck 自己装在系统目录（如 `/usr/local/bin`）时，新工具跟着它放在同一目录。工具已经在 PATH 里就在原位置升级。内置工具的 release 都带校验文件（`checksums.txt`，argo-cd 是 `cli_checksums.txt`），安装和升级会校验 SHA-256。

glab 和 kubectl 是个例外（brew / apt 装的 glab、Docker Desktop 自带的 kubectl 太多）：hmck 只认自己目录里的那一份，不接管 PATH 上别人装的副本——卡片上会注明「PATH 上已有一份」，`hmck uninstall glab` 也只删自己那份。按 `enter` 启动的就是那一份（hmck 自己装过的话它排在前面，没有也不另装第二个副本——两个 glab 并存会把人和 agent 一直在用的那份顶掉）；按 `u` 只说明它归谁管、该用谁升级。状态照样读得出来：glab 看登录，kubectl 看集群。卸不卸由你按 `d` 决定。

从旧版升级（Windows 以前装在 `%LOCALAPPDATA%\<tool>`）时，第一次运行新版 hmck 会把二进制搬到统一目录，并把用户 PATH 里的旧目录换成新目录。配置不在安装目录里，`%AppData%\hmck`、`%AppData%\kube-token`、`~/.npmrc` 和 `~/.claude/skills` 都原样保留，不需要重新配置。

升级安全：先下载到缓存目录（`~/Library/Caches/hmck/downloads` 或 `~/.cache/hmck/downloads`），
有 `checksums.txt` 就校验 SHA-256，再复制到目标目录、原子 rename 覆盖；校验或下载失败不会动原文件。
同一个版本下载过一次，之后安装直接复用缓存。

hmck 自己也在托管列表里：已装版本是编译进二进制的版本号，升级路径就是当前正在运行的可执行文件；
替换后旧进程仍是旧版本，退出重开（或 `hmck version`）即可确认。

## 多端同步

`hmck sync` 把本机的工具配置放进一个 **GitHub 私有仓库**，换台机器用同一个密码拉下来。
仓库里只有密文：PBKDF2-HMAC-SHA256（60 万次）从密码派生密钥，AES-256-GCM 加密，
GitHub（以及任何拿到仓库的人）看到的是一个 `.enc` 文件。

**登录借 gh CLI**：hmck 用 `gh auth token` 拿 token，不另外保存 GitHub 凭据。
没装或没登录就先 `gh auth login`。

```bash
hmck sync push            # 第一次：自动建 <你的用户名>/hmck-config（私有）、设置同步密码
hmck sync                 # 看状态：仓库、上次推送/拉取、会同步哪些文件
hmck sync pull            # 另一台机器：输同一个密码，把配置写回来
hmck sync pull --dry-run  # 只看会改哪些文件，不动本机
hmck sync add ~/.npmrc    # 额外同步一个文件（config:… / home:… 也行）
hmck sync rm ~/.npmrc     # 不再同步它
hmck sync off             # 本机忘掉仓库和密码（远端数据不动）
```

**TUI 里也能同步**：列表按 `y` 打开同步页。上半页是本机状态（仓库、密钥记没记住、会同步
多少个文件、上次推送/拉取），下半页是能做的事：推送、拉取、预览远端会改哪些文件、强制推送/拉取、
添加要同步的路径、忘掉本机密钥（按两次 `enter` 才执行）。本机还没记住密钥时，同步密码直接在
页面里输入（掩码显示；首次推送输两遍），只用于这一次请求，不写盘、不进命令行参数、不上传。
拉取成功后列表和开关会从磁盘重新读一遍，不用退出重开。

**默认同步哪些文件**：hmck 自己的 `config.json`、`tools.json`，加上每个工具声明的配置
（kube-token 的 `config.yaml`，npm-token 的 `config.json`，argo-token 的 `config.json`、
`~/.argocd/token`、`~/.argocd/env`）。本机没有的文件跳过；手工加的路径存在 `sync.json` 里一起走。
单文件最大 2 MiB，整包最大 8 MiB。

**什么时候推**：保存之后自动推，不用惦记——`hmck run <工具>` 退出后、TUI 里改完配置或
新增/删除工具后都会顺手推一次；配置没变就跳过。同步没开启时什么都不做，推送失败只提示，不影响命令本身的退出码。
**拉取是手动的**：另一台机器上 `hmck sync pull` 一次。拉之前本机有没推上去的改动会先提醒，
确认覆盖加 `--force`。

**多台机器不会互相抹配置**：push 会先解开远端、把别的设备独有的文件合进来再上传；
同名文件以本机为准。远端被别的设备推过新版本时，会提示先 `pull`，确实要覆盖再加 `--force`。
从远端写回的文件一律 0600。

同步状态和密钥存在配置目录的 `sync.json`（0600，不进同步）：记住的是派生后的密钥而不是密码，
所以后面 push/pull 不用重复输入；新设备仍然需要同一个密码。忘了密码、或另一台设备换过密码时，
`hmck sync off` 后重新 `pull` 或 `push` 即可。测试用的仓库地址可以用 `HMCK_GITHUB_API` 指走。

## 新增一个工具

**想让装了 hmck 的人都能用：加进[工具目录](#工具目录)。** 在 `docs/catalog.json` 的 `tools` 里加一条，提交到 `main` 就行，
下次谁按 `n` 就能看到，不用发新版 hmck。

**只给自己用：写进本机配置，不用碰代码。**

TUI 里在工具目录页按 `a` 就能填：仓库必填，key 不填按仓库名取，说明可空；`enter` 存进配置，读完发布信息就接着装。
命令行的话：

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
4. `<tool> version` 的输出里要有一个像版本号的词（`1.2.3` / `v1.2.3`），
   hmck 按样子找第一个，不要求是最后一个字段；都没有才退回取最后一个字段。

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

发布在 GitLab 的加 `Host: "gitlab"`（最新版、下载、更新日志都走 GitLab API，见 glab 那条）；
`kubectl` 那条用的是内置的 `Host: "kubernetes"`（最新版读 `dl.k8s.io/release/stable.txt`，资产和 `.sha256` 也在 `dl.k8s.io`），自定义工具一般用不到；
资产名不是 `<key>_<goos>_<goarch>` 的用 `Asset` 模板（`{ver}` `{goos}` `{goarch}` `{archive}` 等），
发压缩包的开 `Archive`；只想管自己装的那份、不接管 PATH 上别人装的（brew 一类）就开 `OwnDir`。

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
