# hmck

一个入口管理我自己的命令行小工具：看版本、安装、升级、直接启动。

目前托管：

| 工具 | 作用 |
| --- | --- |
| [kube-token](https://github.com/hongmengchen/kube-token) | 粘贴新 token 替换 kubeconfig 里的旧值 |
| [npm-token](https://github.com/hongmengchen/npm-token) | 检查并更新 `~/.npmrc` 里的 registry token |

```
$ hmck
hmck  我的命令行小工具
  2 个工具

│ kube-token   0.4.2                   ✓ 已是最新
│ 粘贴新 token 替换 kubeconfig 里的旧值
│ ~/.local/bin/kube-token
│
│ npm-token   0.1.0 → 0.1.1            ↑ 可升级
│ 检查并更新 ~/.npmrc 里的 registry token
│ ~/.local/bin/npm-token

  ↑↓ 选择  ·  enter 启动/安装  ·  u 升级  ·  r 刷新  ·  q 退出
```

`enter` 直接进对应工具的 TUI，用完退出就回到 hmck；`u` 升级选中的工具；没装的按 `enter` 就装。

## 安装

macOS / Linux：

```bash
curl -fsSL https://raw.githubusercontent.com/hongmengchen/hmck/main/install.sh | sh
```

Windows（PowerShell）：

```powershell
irm https://raw.githubusercontent.com/hongmengchen/hmck/main/install.ps1 | iex
```

有 Go 环境可以直接 `go install github.com/hongmengchen/hmck@latest`。

## 用法

```bash
hmck                      # 打开管理界面
hmck list                 # 纯文本表格；--json 给脚本用
hmck run npm-token        # 直接启动，后面的参数原样传过去
hmck install npm-token    # 安装最新版
hmck upgrade              # 升级所有可升级的工具
hmck upgrade npm-token    # 只升一个
hmck upgrade --force      # 已是最新也重装一遍
```

版本检测：已安装版本读 `<tool> version`；最新版看 GitHub `releases/latest` 的跳转（不消耗 API 限流），失败再退回 API。

安装位置：工具已经在 PATH 里就原地升级；没装就装到 hmck 旁边（通常 `~/.local/bin`）。

升级安全：下载到同目录临时文件，有 `checksums.txt` 就校验 SHA-256，再原子 rename 覆盖；校验或下载失败不会动原文件。

## 加一个新工具

在 [tools.go](tools.go) 的 `registry` 里加一条即可，约定发布资产名为 `<key>_<goos>_<goarch>[.exe]`：

```go
{
    Key:       "my-tool",
    Desc:      "一句话说明",
    Repo:      "you/my-tool",
    Binary:    "my-tool",
    Platforms: []string{"darwin/arm64", "linux/amd64", "windows/amd64"},
    Checksums: true, // 发布里有 checksums.txt 就打开
},
```

CLI、TUI、升级逻辑都会自动带上它。

## 开发

```bash
go test ./...
go build -o hmck .
```
