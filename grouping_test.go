package main

import (
	"strings"
	"testing"
)

// 列表分三栏：官方 CLI 在前、自家工具在后、本机自己加的在最后；
// 同一栏里保留注册表的先后，不按字母重排
func TestToolsSortedByGroup(t *testing.T) {
	isolateConfig(t)
	if _, err := addCustomTool(tool{Key: "my-tool", Repo: "me/my-tool", Platforms: []string{"linux/amd64"}}, false); err != nil {
		t.Fatal(err)
	}
	tools, err := allTools()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, tl := range tools {
		got = append(got, toolKind(tl)+":"+tl.Key)
	}
	want := []string{
		"official:glab", "official:kubectl", "official:argocd",
		"own:kube-token", "own:npm-token", "own:argo-token", "own:hmck",
		"added:my-tool",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("工具顺序 = %v，想要 %v", got, want)
	}
}

// argo-token 是自家工具：跟 kube-token 一样在 GitHub 发版、带 checksums 和 skill
func TestArgoTokenIsBuiltinOwnTool(t *testing.T) {
	tl, ok := toolByKey("argo-token")
	if !ok {
		t.Fatal("内置工具里没有 argo-token")
	}
	if tl.Repo != "hongmengchen/argo-token" || tl.Binary != "argo-token" {
		t.Errorf("argo-token 定义不对: %+v", tl)
	}
	if !tl.Checksums || !tl.Skill || toolKind(tl) != kindOwn {
		t.Errorf("argo-token 应该带校验、skill，归到自家工具: %+v", tl)
	}
	if a := assetName(tl, "darwin", "arm64", "v0.1.0"); a != "argo-token_darwin_arm64" {
		t.Errorf("资产名 = %q", a)
	}
}

func TestEmbeddedCatalogHasArgoToken(t *testing.T) {
	tools, _, err := parseCatalog(catalogEmbedded)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range tools {
		if tl.Key == "argo-token" {
			if tl.Repo != "hongmengchen/argo-token" {
				t.Errorf("目录里的 argo-token 不对: %+v", tl)
			}
			return
		}
	}
	t.Errorf("内置目录里没有 argo-token: %v", catalogToolKeys(tools))
}

// 列表页把三栏的标题摆出来，顺序是 官方 CLI → 自己的工具 → 自己加的
func TestListViewShowsGroups(t *testing.T) {
	isolateConfig(t)
	m := newTUIModel()
	m.width, m.height = 100, 40
	m.localReady = true
	m.pending = map[string]bool{}
	m.states = []toolState{
		{Tool: tool{Key: "glab", Kind: kindOfficial}, Path: "/tmp/glab", Installed: "1.130.0", Latest: "1.130.0"},
		{Tool: tool{Key: "kube-token", Kind: kindOwn}, Path: "/tmp/kube-token", Installed: "0.8.0", Latest: "0.8.0"},
		{Tool: tool{Key: "fn", Repo: "me/fn", Custom: true}},
	}

	got := plainText(m.viewList())
	official := strings.Index(got, "官方 CLI")
	own := strings.Index(got, "自己的工具")
	added := strings.Index(got, "自己加的")
	if official < 0 || own < 0 || added < 0 {
		t.Fatalf("列表页缺少分栏标题:\n%s", got)
	}
	if !(official < own && own < added) {
		t.Errorf("分栏顺序不对: 官方 %d / 自家 %d / 自加 %d", official, own, added)
	}
	if !strings.Contains(got, "· 1 个") {
		t.Errorf("分栏标题应带数量:\n%s", got)
	}
}

// 命令行 list --json 的每条也带 kind，脚本能按栏筛
func TestListItemKind(t *testing.T) {
	cases := map[string]tool{
		"official": {Key: "glab", Kind: kindOfficial},
		"own":      {Key: "kube-token", Kind: kindOwn},
		"added":    {Key: "mine", Repo: "me/mine", Custom: true},
	}
	for want, tl := range cases {
		if got := toListItem(toolState{Tool: tl}).Kind; got != want {
			t.Errorf("%s 的 kind = %q", tl.Key, got)
		}
	}
}
