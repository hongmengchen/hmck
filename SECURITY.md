# 安全策略

## 支持范围

只为最新一个 release 提供安全修复。报告前请先运行 `hmck upgrade hmck` 确认问题仍然存在。

## 报告问题

请通过 GitHub 的私密渠道报告，不要开公开 issue：

https://github.com/hongmengchen/hmck/security/advisories/new

报告里请写清版本（`hmck version`）、系统/终端、复现步骤和影响。**不要粘贴真实 token、配置文件、公司内部仓库地址或用户名**；用 `owner/repo`、`git.example.com`、`alice` 这类假值代替。

这是个人项目，会在力所能及的范围内尽快回复，通常不超过 7 天。

## 程序的安全边界

- 只从各工具 GitHub Releases 的 `releases/latest` 解析版本和资产，不执行 release 里的脚本
- 下载先落到缓存目录，有 `checksums.txt` 就校验 SHA-256，再复制到目标目录并原子 rename；失败不会动原文件
- 内置工具的下载地址固定为公开仓库；`hmck add` 加的自定义工具会按你填的 `owner/repo` 下载，添加前请自行确认来源
- 没有遥测；自动下载/安装开关只在你手动打开后生效
