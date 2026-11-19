package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
)

// changeSection 是更新日志里的一个小节（新增 / 变更 / 修复 …）
type changeSection struct {
	Title string
	Items []string
}

// changeEntry 是更新日志里的一个版本
type changeEntry struct {
	Version  string
	Date     string // YYYY-MM-DD，没有就空
	Sections []changeSection
}

// summary 取第一条说明，列表卡片上用来预览「最新版本改了什么」
func (e changeEntry) summary() string {
	for _, s := range e.Sections {
		for _, it := range s.Items {
			if strings.TrimSpace(it) != "" {
				return strings.TrimSpace(it)
			}
		}
	}
	return ""
}

// changelogVersionLimit 是默认看几个版本；TUI 和 CLI 都用它
const changelogVersionLimit = 5

// 更新日志优先读仓库里的 CHANGELOG.md（内容最全），没有再退到 release notes。
// 测试里会替换成 httptest 地址。
var (
	changelogRawURL = func(repo string) string {
		return "https://raw.githubusercontent.com/" + repo + "/main/CHANGELOG.md"
	}
	releasesListURL = func(repo string) string {
		return "https://api.github.com/repos/" + repo + "/releases?per_page=20"
	}
)

// fetchChangelog 拿一个仓库最近的几个版本说明
func fetchChangelog(repo string, limit int) ([]changeEntry, error) {
	if limit <= 0 {
		limit = changelogVersionLimit
	}
	rawErr := error(nil)
	if entries, err := changelogFromRepo(repo); err == nil && len(entries) > 0 {
		return trimEntries(entries, limit), nil
	} else if err != nil {
		rawErr = err
	}

	relErr := error(nil)
	if entries, err := changelogFromReleases(repo); err == nil && len(entries) > 0 {
		return trimEntries(entries, limit), nil
	} else if err != nil {
		relErr = err
	}

	switch {
	case rawErr != nil:
		return nil, rawErr
	case relErr != nil:
		return nil, relErr
	default:
		return nil, fmt.Errorf("%s 还没有更新日志", repo)
	}
}

func trimEntries(entries []changeEntry, limit int) []changeEntry {
	if len(entries) > limit {
		return entries[:limit]
	}
	return entries
}

// githubAuth 有 GH_TOKEN / GITHUB_TOKEN 就带上，免得撞 API 限流
func githubAuth(req *http.Request) {
	req.Header.Set("User-Agent", "hmck/"+version)
	for _, name := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		if tok := strings.TrimSpace(os.Getenv(name)); tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
			return
		}
	}
}

func changelogFromRepo(repo string) ([]changeEntry, error) {
	req, err := http.NewRequest(http.MethodGet, changelogRawURL(repo), nil)
	if err != nil {
		return nil, err
	}
	githubAuth(req)
	resp, err := apiClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("读 CHANGELOG.md 失败: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, fmt.Errorf("%s 没有 CHANGELOG.md", repo)
	default:
		return nil, fmt.Errorf("读 CHANGELOG.md 失败: %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	return parseChangelog(string(data)), nil
}

// changelogFromReleases 是兜底：仓库没有 CHANGELOG.md 时看 release notes
func changelogFromReleases(repo string) ([]changeEntry, error) {
	req, err := http.NewRequest(http.MethodGet, releasesListURL(repo), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	githubAuth(req)
	resp, err := apiClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("读 release 说明失败: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusForbidden, http.StatusTooManyRequests:
		return nil, fmt.Errorf("GitHub API 限流了，稍后再试")
	default:
		return nil, fmt.Errorf("读 release 说明失败: %s", resp.Status)
	}

	var rels []struct {
		TagName     string `json:"tag_name"`
		PublishedAt string `json:"published_at"`
		Body        string `json:"body"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&rels); err != nil {
		return nil, err
	}
	var entries []changeEntry
	for _, r := range rels {
		ver := strings.TrimPrefix(strings.TrimSpace(r.TagName), "v")
		if ver == "" {
			continue
		}
		date := ""
		if len(r.PublishedAt) >= 10 {
			date = r.PublishedAt[:10]
		}
		entries = append(entries, changeEntry{Version: ver, Date: date, Sections: sectionsFromMarkdown(r.Body)})
	}
	return entries, nil
}

// sectionsFromMarkdown 把任意 markdown 说明拆成小节；release notes 通常没有
// 版本标题，所以整段就是内容
func sectionsFromMarkdown(body string) []changeSection {
	secs := []changeSection{{}}
	for _, raw := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		if title, ok := sectionHeading(trimmed); ok {
			secs = append(secs, changeSection{Title: title})
			continue
		}
		if item, ok := bulletText(raw); ok {
			last := &secs[len(secs)-1]
			last.Items = append(last.Items, item)
			continue
		}
		// 普通段落：当前小节还没条目就当第一条，否则接在上一条后面
		last := &secs[len(secs)-1]
		text := cleanMarkdown(trimmed)
		if len(last.Items) > 0 {
			last.Items[len(last.Items)-1] += " " + text
		} else {
			last.Items = append(last.Items, text)
		}
	}
	out := secs[:0]
	for _, s := range secs {
		if len(s.Items) > 0 {
			out = append(out, s)
		}
	}
	return out
}

var (
	mdDate  = regexp.MustCompile(`\d{4}-\d{2}-\d{2}`)
	mdImage = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)`)
	mdLink  = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
)

// parseChangelog 解析 Keep a Changelog 风格的 CHANGELOG.md：
// `## [0.8.0] - 2026-11-18` 起一个版本，`### 新增` 起一个小节，`- ` 是一条说明
func parseChangelog(text string) []changeEntry {
	var entries []changeEntry
	started := false

	for _, raw := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(raw)

		if ver, date, ok := changelogHeading(trimmed); ok {
			entries = append(entries, changeEntry{Version: ver, Date: date})
			started = true
			continue
		}
		if !started {
			continue // 文件开头的说明不属于任何版本
		}
		if title, ok := sectionHeading(trimmed); ok {
			last := &entries[len(entries)-1]
			last.Sections = append(last.Sections, changeSection{Title: title})
			continue
		}
		if trimmed == "" {
			continue
		}
		last := &entries[len(entries)-1]
		if item, ok := bulletText(raw); ok {
			if len(last.Sections) == 0 {
				last.Sections = append(last.Sections, changeSection{})
			}
			sec := &last.Sections[len(last.Sections)-1]
			sec.Items = append(sec.Items, item)
			continue
		}
		// 折行的说明接在上一条后面，别当成新条目
		if len(last.Sections) > 0 {
			sec := &last.Sections[len(last.Sections)-1]
			if len(sec.Items) > 0 {
				sec.Items[len(sec.Items)-1] += " " + cleanMarkdown(trimmed)
			}
		}
	}

	// 只有标题、没有内容的版本（Unreleased 之类）不显示
	out := entries[:0]
	for _, e := range entries {
		if len(e.Sections) > 0 {
			out = append(out, e)
		}
	}
	return out
}

// changelogHeading 认 `## [0.8.0] - 2026-11-18`、`## v0.8.0`、`## 0.8.0 (2026-11-18)`
func changelogHeading(line string) (ver, date string, ok bool) {
	if !strings.HasPrefix(line, "## ") {
		return "", "", false
	}
	s := strings.TrimSpace(strings.TrimPrefix(line, "##"))
	s = strings.TrimPrefix(s, "[")

	end := strings.IndexAny(s, "] \t")
	if end < 0 {
		end = len(s)
	}
	ver = strings.TrimSpace(s[:end])
	ver = strings.TrimPrefix(ver, "v")
	if !looksLikeVersion(ver) {
		return "", "", false
	}

	rest := ""
	if end < len(s) {
		rest = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s[end:]), "]"))
	}
	return ver, mdDate.FindString(rest), true
}

func looksLikeVersion(s string) bool {
	if s == "" || s[0] < '0' || s[0] > '9' {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case r == '.', r == '-', r == '+':
		default:
			return false
		}
	}
	return true
}

func sectionHeading(line string) (string, bool) {
	if !strings.HasPrefix(line, "### ") {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(line, "###")), true
}

func bulletText(line string) (string, bool) {
	s := strings.TrimLeft(line, " \t")
	for _, p := range []string{"- ", "* ", "+ "} {
		if strings.HasPrefix(s, p) {
			text := cleanMarkdown(strings.TrimSpace(strings.TrimPrefix(s, p)))
			if text == "" {
				return "", false
			}
			return text, true
		}
	}
	return "", false
}

// cleanMarkdown 去掉终端里看不懂的标记，只留文字
func cleanMarkdown(s string) string {
	s = mdImage.ReplaceAllString(s, "")
	s = mdLink.ReplaceAllString(s, "$1")
	s = strings.ReplaceAll(s, "**", "")
	s = strings.ReplaceAll(s, "__", "")
	s = strings.ReplaceAll(s, "`", "")
	s = strings.ReplaceAll(s, "<br>", " ")
	return strings.TrimSpace(s)
}

// formatChangelogText 是 CLI 的纯文本输出：版本 → 小节 → 条目
func formatChangelogText(key string, entries []changeEntry, current string) string {
	var b strings.Builder
	head := key + " 更新日志"
	if current != "" {
		head += "（当前 " + current + "）"
	}
	b.WriteString(head + "\n")

	for i, e := range entries {
		b.WriteString("\n" + "v" + e.Version)
		if e.Date != "" {
			b.WriteString("  " + e.Date)
		}
		switch {
		case i == 0 && e.Version == current:
			b.WriteString("  当前 · 最新")
		case i == 0:
			b.WriteString("  最新")
		case e.Version == current:
			b.WriteString("  当前")
		}
		b.WriteString("\n")
		for _, s := range e.Sections {
			if s.Title != "" {
				b.WriteString("  " + s.Title + "\n")
			}
			for _, it := range s.Items {
				b.WriteString("    - " + it + "\n")
			}
		}
	}
	return b.String()
}
