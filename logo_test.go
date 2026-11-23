package main

import (
	"image/color"
	"math"
	"strings"
	"testing"
)

// 每个内置工具的招牌都由 docs/logos 里的 SVG 渲染出来：logoWidth 格宽、logoRowsN 行高，一幅图一个样
func TestBuiltinLogosRenderFromSVG(t *testing.T) {
	seen := map[string]string{}
	for _, key := range []string{"hmck", "kube-token", "npm-token", "glab"} {
		rows := toolLogo(tool{Key: key})
		if len(rows) != logoRowsN {
			t.Fatalf("%s 的招牌应有 %d 行，got %d", key, logoRowsN, len(rows))
		}
		for i, row := range rows {
			plain := plainText(row)
			if n := len([]rune(plain)); n != logoWidth {
				t.Errorf("%s 第 %d 行应有 %d 格，got %d: %q", key, i, logoWidth, n, plain)
			}
			if !hasInk(plain) {
				t.Errorf("%s 第 %d 行没有实心像素: %q", key, i, plain)
			}
			for _, r := range plain {
				if !isQuadRune(r) {
					t.Errorf("%s 第 %d 行有不是方块字符的字: %q", key, i, r)
					break
				}
			}
		}
		full := plainText(strings.Join(rows, "\n"))
		if other, ok := seen[full]; ok {
			t.Errorf("%s 和 %s 的招牌一模一样", key, other)
		}
		seen[full] = key
	}
}

// 招牌用的是 SVG 里的真颜色：hmck 的箭头是绿色的，glab 的狸猫是橙红的
func TestLogoKeepsSVGColors(t *testing.T) {
	cases := []struct {
		key   string
		match func(color.RGBA) bool
		why   string
	}{
		{"hmck", func(c color.RGBA) bool { return c.G > 150 && c.R < 120 && c.B < 160 }, "箭头应该是绿色"},
		{"glab", func(c color.RGBA) bool { return c.R > 200 && c.G < 140 && c.B < 90 }, "狸猫应该是橙红色"},
	}
	for _, c := range cases {
		raw, err := logoSVGs.ReadFile("docs/logos/" + c.key + ".svg")
		if err != nil {
			t.Fatal(err)
		}
		img, err := parseSVG(raw)
		if err != nil {
			t.Fatalf("%s: %v", c.key, err)
		}
		found := false
		for _, cell := range flatCells(logoCells(rasterizeLogo(img))) {
			if cell.hasFG && c.match(cell.fg) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: %s", c.key, c.why)
		}
	}
}

// 自定义工具没有 SVG，退回方框：非 ASCII 首字母不该塞进去（会撑破方框）
func TestLogoFallsBackToMark(t *testing.T) {
	if got := plainText(strings.Join(toolLogo(tool{Key: "中文工具"}), "")); !strings.Contains(got, "│ * │") {
		t.Errorf("自定义工具应该用方框招牌，got %q", got)
	}
	if got := plainText(strings.Join(toolLogo(tool{Key: "my-tool"}), "")); !strings.Contains(got, "│ M │") {
		t.Errorf("自定义工具应该用首字母，got %q", got)
	}
}

// 圆角矩形的四个角是弧，不是直角
func TestRoundedRectPoints(t *testing.T) {
	pts := roundedRectPoints(0, 0, 10, 10, 2, 2)
	for _, p := range pts {
		if p.x < -1e-9 || p.x > 10+1e-9 || p.y < -1e-9 || p.y > 10+1e-9 {
			t.Errorf("圆角矩形的点跑到框外: %v", p)
		}
	}
	// 左上角是圆心在 (2,2) 的圆弧，不是直角：那里的点必须落在弧上
	for _, p := range pts {
		if p.x >= 2 || p.y >= 2 {
			continue
		}
		if d := math.Hypot(p.x-2, p.y-2); math.Abs(d-2) > 1e-9 {
			t.Errorf("左上角应该是半径 2 的圆弧，点 %v 到圆心的距离是 %v", p, d)
		}
	}
}

func TestParsePathData(t *testing.T) {
	subs := parsePathData("M0 0 L10 0 L10 10 Z M20 20 h-5 v5")
	if len(subs) != 2 {
		t.Fatalf("两个子路径，got %d", len(subs))
	}
	if len(subs[0].pts) != 4 {
		t.Errorf("Z 之后应该回到起点，got %d 个点", len(subs[0].pts))
	}
	if last := subs[0].pts[len(subs[0].pts)-1]; last.x != 0 || last.y != 0 {
		t.Errorf("闭合点应回到起点，got %v", last)
	}
	if last := subs[1].pts[len(subs[1].pts)-1]; last.x != 15 || last.y != 25 {
		t.Errorf("相对坐标起点应在 (20,20)，got %v", last)
	}
}

// 曲线算出来的点不该背离控制点的包围盒太远
func TestCurveIsFlattenedInsideControlBox(t *testing.T) {
	subs := parsePathData("M0 0 C0 10 10 10 10 0")
	if len(subs) != 1 || len(subs[0].pts) < 8 {
		t.Fatalf("三次曲线应该被切成多段，got %d", len(subs[0].pts))
	}
	for _, p := range subs[0].pts {
		if p.x < -1e-9 || p.x > 10+1e-9 || p.y < -1e-9 || p.y > 7.5+1e-9 {
			t.Errorf("曲线点跑到控制点包围盒外: %v", p)
		}
	}
}

func TestParseColor(t *testing.T) {
	if c, ok := parseColor("#FC6D26"); !ok || c.R != 0xfc || c.G != 0x6d || c.B != 0x26 {
		t.Errorf("六位十六进制没解析对: %v %v", c, ok)
	}
	if c, ok := parseColor("#f80"); !ok || c.R != 0xff || c.G != 0x88 || c.B != 0 {
		t.Errorf("三位十六进制没解析对: %v %v", c, ok)
	}
	if _, ok := parseColor("none"); ok {
		t.Error("none 不该算有颜色")
	}
}

func TestParseCSS(t *testing.T) {
	css := parseCSS(".cls-1 {\n fill: #fca326;\n}\n.cls-2 { fill: none; stroke: #123456 }")
	if css["cls-1"].fill != "#fca326" {
		t.Errorf("class 的填充色没读到: %+v", css["cls-1"])
	}
	if css["cls-2"].stroke != "#123456" {
		t.Errorf("class 的描边色没读到: %+v", css["cls-2"])
	}
}

// 招牌的每一格都是 2×2 的实心方块字符（四分之一/半/整块），不是点阵
func isQuadRune(r rune) bool {
	for _, q := range quadRunes {
		if r == q {
			return true
		}
	}
	return false
}

func hasInk(s string) bool {
	for _, r := range s {
		if r != ' ' {
			return true
		}
	}
	return false
}

func flatCells(rows [][]logoCell) []logoCell {
	var out []logoCell
	for _, row := range rows {
		out = append(out, row...)
	}
	return out
}
