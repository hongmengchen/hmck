# hmck 安装脚本（Windows）
#
#   irm https://raw.githubusercontent.com/hongmengchen/hmck/main/install.ps1 | iex
#
# 可选参数：
#   -Version v0.1.0   装指定版本（默认最新）
#   -BinDir C:\path   装到指定目录（默认 %USERPROFILE%\.local\bin，和 npm-token、kube-token 相同）
param(
    [string]$Version = "",
    [string]$BinDir = ""
)

$ErrorActionPreference = "Stop"
try {
    [Console]::OutputEncoding = [System.Text.Encoding]::UTF8
    $OutputEncoding = [System.Text.Encoding]::UTF8
} catch { }

$repo = "hongmengchen/hmck"
$bin = "hmck"

function Fail($msg) {
    Write-Host "错误: $msg" -ForegroundColor Red
    exit 1
}

switch ($env:PROCESSOR_ARCHITECTURE) {
    "AMD64" { $arch = "amd64" }
    "ARM64" { $arch = "arm64" }
    "x86" { Fail "不支持 32 位 Windows" }
    default { Fail "不支持的 CPU 架构: $($env:PROCESSOR_ARCHITECTURE)" }
}

if ($Version) {
    $tag = $Version.TrimStart("v")
    $base = "https://github.com/$repo/releases/download/v$tag"
    $label = "v$tag"
} else {
    $base = "https://github.com/$repo/releases/latest/download"
    $label = "最新版"
}

$asset = "${bin}_windows_${arch}.exe"
$tmp = Join-Path $env:TEMP ("hmck-" + [guid]::NewGuid().ToString("N") + ".exe")

Write-Host "下载 $bin ($label, windows $arch)…"
try {
    Invoke-WebRequest -Uri "$base/$asset" -OutFile $tmp -UseBasicParsing
} catch {
    Fail "下载失败: $base/$asset`n$($_.Exception.Message)"
}

# 有 checksums.txt 就校验
$sums = "$tmp.sha256"
try {
    Invoke-WebRequest -Uri "$base/checksums.txt" -OutFile $sums -UseBasicParsing
    $line = Select-String -Path $sums -Pattern ("\s+" + [regex]::Escape($asset) + "$") | Select-Object -First 1
    if ($line) {
        $want = ($line.Line -split "\s+")[0]
        $got = (Get-FileHash -Algorithm SHA256 -Path $tmp).Hash
        if ($got -ne $want.ToUpper()) {
            Remove-Item -Force $tmp -ErrorAction SilentlyContinue
            Fail "校验失败：下载到的文件与 checksums.txt 不一致"
        }
    }
} catch { }
Remove-Item -Force $sums -ErrorAction SilentlyContinue

if (-not $BinDir) {
    $BinDir = Join-Path $HOME ".local\bin"
}
New-Item -ItemType Directory -Force -Path $BinDir | Out-Null

$target = Join-Path $BinDir "$bin.exe"
try {
    Move-Item -Force -Path $tmp -Destination $target
} catch {
    Remove-Item -Force $tmp -ErrorAction SilentlyContinue
    Fail "写入 $target 失败（程序是否正在运行？）`n$($_.Exception.Message)"
}

Write-Host "已安装: $target"

# 用户 PATH：统一目录放最前面；旧版 %LOCALAPPDATA%\<tool> 的项去掉
$legacy = @()
if ($env:LOCALAPPDATA) {
    $legacy = @("hmck", "npm-token", "kube-token") | ForEach-Object { (Join-Path $env:LOCALAPPDATA $_).TrimEnd("\") }
}
$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
$parts = @()
if ($userPath) { $parts = @($userPath -split ";" | Where-Object { $_.Trim() -ne "" }) }
$keep = @($parts | Where-Object { $legacy -notcontains $_.TrimEnd("\") -and $_.TrimEnd("\") -ne $BinDir.TrimEnd("\") })
$newPath = (@($BinDir) + $keep) -join ";"
if ($newPath -ne $userPath) {
    [Environment]::SetEnvironmentVariable("Path", $newPath, "User")
    Write-Host "已把 $BinDir 放到用户 PATH 最前面（新开终端生效）"
}
$env:Path = $BinDir + ";" + $env:Path

# 跑一次新版 hmck：它会把旧目录里的工具搬到统一目录，配置（%AppData% 等）原样保留
try { & $target version } catch { }

# 顺手把 agent skill 写到 ~/.claude/skills（失败不影响安装）
try { & $target skill --quiet 2>$null | Out-Null } catch { }

Write-Host "`n运行 $bin 打开管理界面。" -ForegroundColor Green
