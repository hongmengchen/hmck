package main

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxSyncPreviewDiffLines = 200
	maxSyncPreviewDiffCells = 500_000
)

type syncPreviewFile struct {
	Name      string
	Status    string
	Diff      []string
	Sensitive bool
	Binary    bool
}

var (
	syncPreviewSecretKey   = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:api[_-]?key|access[_-]?key|auth(?:orization)?|client[_-]?secret|credential(?:s)?|password|passwd|private[_-]?key|refresh[_-]?token|secret|token)(?:[^a-z0-9]*)(?:[:=])`)
	syncPreviewKnownSecret = regexp.MustCompile(`(?i)\b(?:gh[pousr]_[a-z0-9_]{20,}|xox[baprs]-[a-z0-9-]{10,}|akia[a-z0-9]{16}|eyj[a-z0-9_-]{10,}\.[a-z0-9_-]{10,}\.[a-z0-9_-]{10,})\b`)
)

func makeSyncPreviewFile(name string, old, remote []byte, exists bool) syncPreviewFile {
	f := syncPreviewFile{Name: name}
	if !exists {
		f.Status = "新增"
	} else if bytes.Equal(old, remote) {
		f.Status = "没变化"
		return f
	} else {
		f.Status = "变更"
	}

	if syncPreviewSensitivePath(name) || syncPreviewSensitiveData(old) || syncPreviewSensitiveData(remote) {
		f.Sensitive = true
		return f
	}
	if !utf8.Valid(old) || !utf8.Valid(remote) {
		f.Binary = true
		return f
	}
	f.Diff = syncPreviewDiff(old, remote)
	return f
}

// Known credential locations are hidden wholesale. For other files, a credential-like
// key/value or recognizable token makes the entire file diff opaque, not just one line.
func syncPreviewSensitivePath(name string) bool {
	path := strings.ToLower(strings.ReplaceAll(name, "\\", "/"))
	for _, marker := range []string{
		".argocd/", ".kube/config", ".ssh/", ".aws/", ".npmrc", ".netrc",
	} {
		if strings.Contains(path, marker) {
			return true
		}
	}
	base := path
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	for _, marker := range []string{"token", "credential", "secret", "private-key", "private_key"} {
		if strings.Contains(base, marker) {
			return true
		}
	}
	return base == "id_rsa" || base == "id_ed25519" || strings.HasSuffix(base, ".pem") || strings.HasSuffix(base, ".p12") || strings.HasSuffix(base, ".pfx")
}

func syncPreviewSensitiveData(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	if bytes.Contains(data, []byte("PRIVATE KEY-----")) || syncPreviewSecretKey.Match(data) || syncPreviewKnownSecret.Match(data) {
		return true
	}
	return false
}

func syncPreviewDiff(old, remote []byte) []string {
	oldLines, newLines := splitSyncPreviewLines(old), splitSyncPreviewLines(remote)
	rows, cols := len(oldLines)+1, len(newLines)+1
	if rows > maxSyncPreviewDiffCells/cols {
		return syncPreviewCoarseDiff(oldLines, newLines)
	}

	// The product bound keeps the dynamic-programming table small even for a file
	// containing many short lines. Very large diffs use a bounded whole-file view.
	dp := make([]uint32, rows*cols)
	for i := len(oldLines) - 1; i >= 0; i-- {
		for j := len(newLines) - 1; j >= 0; j-- {
			at := i*cols + j
			switch {
			case oldLines[i] == newLines[j]:
				dp[at] = dp[(i+1)*cols+j+1] + 1
			case dp[(i+1)*cols+j] >= dp[i*cols+j+1]:
				dp[at] = dp[(i+1)*cols+j]
			default:
				dp[at] = dp[i*cols+j+1]
			}
		}
	}

	var out []string
	appendLine := func(sign string, line string, total *int) {
		*total = *total + 1
		if len(out) < maxSyncPreviewDiffLines {
			out = append(out, sign+sanitizeSyncPreviewLine(line))
		}
	}
	total := 0
	for i, j := 0, 0; i < len(oldLines) || j < len(newLines); {
		switch {
		case i < len(oldLines) && j < len(newLines) && oldLines[i] == newLines[j]:
			i++
			j++
		case i < len(oldLines) && (j == len(newLines) || dp[(i+1)*cols+j] >= dp[i*cols+j+1]):
			appendLine("- ", oldLines[i], &total)
			i++
		default:
			appendLine("+ ", newLines[j], &total)
			j++
		}
	}
	if total > len(out) {
		out = append(out, fmt.Sprintf("… 还有 %d 行差异未显示", total-len(out)))
	}
	return out
}

func splitSyncPreviewLines(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	return strings.Split(string(data), "\n")
}

func syncPreviewCoarseDiff(oldLines, newLines []string) []string {
	var out []string
	total := len(oldLines) + len(newLines)
	for _, line := range oldLines {
		if len(out) == maxSyncPreviewDiffLines {
			break
		}
		out = append(out, "- "+sanitizeSyncPreviewLine(line))
	}
	for _, line := range newLines {
		if len(out) == maxSyncPreviewDiffLines {
			break
		}
		out = append(out, "+ "+sanitizeSyncPreviewLine(line))
	}
	if total > len(out) {
		out = append(out, fmt.Sprintf("… 还有 %d 行差异未显示（文件较大）", total-len(out)))
	}
	return out
}

func sanitizeSyncPreviewLine(line string) string {
	var b strings.Builder
	for _, r := range line {
		if unicode.IsControl(r) {
			if r <= 0xffff {
				fmt.Fprintf(&b, "\\u%04x", r)
			} else {
				fmt.Fprintf(&b, "\\U%08x", r)
			}
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func syncPreviewLines(files []syncPreviewFile) []string {
	var lines []string
	for _, f := range files {
		lines = append(lines, fmt.Sprintf("  %s  [%s]", f.Name, f.Status))
		switch {
		case f.Sensitive:
			lines = append(lines, "    内容疑似包含凭据，已整体隐藏")
		case f.Binary:
			lines = append(lines, "    二进制内容不展示")
		case len(f.Diff) == 0 && f.Status != "没变化":
			lines = append(lines, "    （没有可显示的文本行差异）")
		default:
			for _, line := range f.Diff {
				lines = append(lines, "    "+line)
			}
		}
	}
	if len(lines) == 0 {
		return []string{"  远端没有可预览的文件"}
	}
	return lines
}

func renderSyncPreview(files []syncPreviewFile) string {
	var b strings.Builder
	for _, line := range syncPreviewLines(files) {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}
