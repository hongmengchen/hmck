# 变更记录

更早的版本见 [GitHub Releases](https://github.com/hongmengchen/hmck/releases)。

## [0.4.2] - 2026-11-18

### 变更

- kube-token 注册表补上 Windows ARM64，并启用 `checksums.txt` 校验
- 安装脚本的默认目录统一为 `~/.local/bin`（root 为 `/usr/local/bin`）
- 增加 push / PR CI，发版前跑 gofmt、vet、test

## [0.4.1] - 2026-11-18

### 新增

- hmck 自己纳入托管列表，可自我升级
- TUI 增加「自动下载最新版 / 自动安装最新版」开关
