# 变更记录

更早的版本见 [GitHub Releases](https://github.com/hongmengchen/hmck/releases)。

## [0.5.0] - 2026-11-18

### 变更

- 打开 TUI 先列出本地工具，最新版改为后台查询并逐张卡片刷新，不再停在「正在问 GitHub 最新版本」
- 安装目录统一为 `~/.local/bin`（Windows 为 `%USERPROFILE%\.local\bin`）
- 新增旧安装位置自动迁移：把原来的二进制搬到统一目录，清理用户 PATH 里的旧目录，配置和 agent skill 保持不动

### 修复

- 安装位置查询在旧目录和统一目录都有副本时，保留版本较新的那一份

## [0.4.2] - 2026-11-18

### 变更

- kube-token 注册表补上 Windows ARM64，并启用 `checksums.txt` 校验
- 安装脚本的默认目录统一为 `~/.local/bin`（root 为 `/usr/local/bin`）
- 增加 push / PR CI，发版前跑 gofmt、vet、test

## [0.4.1] - 2026-11-18

### 新增

- hmck 自己纳入托管列表，可自我升级
- TUI 增加「自动下载最新版 / 自动安装最新版」开关
