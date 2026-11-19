package main

import (
	"bytes"
	"embed"
	"encoding/xml"
	"fmt"
	"image/color"
	"io"
	"math"
	"strconv"
	"strings"
	"sync"

	"github.com/charmbracelet/lipgloss"
)

// 招牌用的就是各工具仓库里的真 SVG（glab 是 GitLab 官方的狸猫）。
// 终端放不下图片，所以把 SVG 光栅化成 16×16 点阵，再折成盲文字符：
// 一个字符格是 2 列 × 4 行点，一块招牌正好 8 列 × 4 行，颜色走真实色值，
// 终端颜色少的时候由 lipgloss 自己降级。
//
//go:embed docs/logos/*.svg
var logoSVGs embed.FS

const (
	logoDots  = 16 // 光栅边长（点）
	logoSS    = 4  // 每个点取 4×4 个采样，边缘就不毛糙
	logoWidth = logoDots / 2
	logoRowsN = logoDots / 4

	// 细线在 16 点阵上会细到看不见：统一撑到这个宽度（单位：点）
	minStrokeDots = 1.4
	// 一个点里混了这么多背景色就算「有墨」
	inkDist = 48.0
)

// toolLogo 取工具的招牌：内置工具把 docs/logos/<key>.svg 渲染成盲文点阵；
// 自定义工具、或者 SVG 读不出来时，退回手画的方框和首字母
func toolLogo(t tool) []string {
	key := strings.ToLower(strings.TrimSpace(t.Key))
	if rows, ok := logoCache.Load(key); ok {
		return rows.([]string)
	}
	rows := svgLogo(key)
	if rows == nil {
		rows = markLogo(t)
	}
	logoCache.Store(key, rows)
	return rows
}

var logoCache sync.Map // tool key -> []string

func resetLogoCache() { logoCache = sync.Map{} }

// svgLogo 把一张内嵌的 SVG 渲染成 8×4 个带颜色的字符格
func svgLogo(key string) []string {
	raw, err := logoSVGs.ReadFile("docs/logos/" + key + ".svg")
	if err != nil {
		return nil
	}
	img, err := parseSVG(raw)
	if err != nil {
		return nil
	}
	pix := rasterizeLogo(img)
	if pix == nil {
		return nil
	}
	return styleLogoCells(logoCells(pix))
}

// markLogo 是没有 SVG 时的退路：方框里放首字母。
// 中文等宽字符会撑破方框，只有 ASCII 字母数字才放进去
func markLogo(t tool) []string {
	if mark, ok := toolMarks[strings.ToLower(t.Key)]; ok {
		return styleMark(mark)
	}
	ch := "*"
	key := strings.TrimSpace(t.Key)
	if key != "" {
		r := []rune(strings.ToUpper(key))[0]
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			ch = string(r)
		}
	}
	return styleMark([]string{"┌───┐", "│ " + ch + " │", "└───┘"})
}

func styleMark(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = styles.Accent.Render(l)
	}
	return out
}

// ---------- SVG：只认这套招牌用得着的子集 ----------

type logoPoint struct{ x, y float64 }

type logoSub struct {
	pts        []logoPoint
	minX, minY float64
	maxX, maxY float64
}

type logoShape struct {
	subs      []logoSub
	fill      color.RGBA
	stroke    color.RGBA
	strokeW   float64
	hasFill   bool
	hasStroke bool

	minX, minY, maxX, maxY float64 // 整条形状的包围盒
}

type logoImage struct {
	minX, minY, w, h float64
	shapes           []logoShape
}

// parseSVG 读 viewBox、<style> 里的 class 颜色，以及 rect / path 两种形状。
// 顺序按文档顺序排，后面的盖前面的；group 和 transform 不支持——
// 这几个招牌 Visio 都很简单，不需要
func parseSVG(src []byte) (*logoImage, error) {
	dec := xml.NewDecoder(bytes.NewReader(src))
	type element struct {
		name  string
		attrs map[string]string
	}
	var elems []element
	var css strings.Builder

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if start.Name.Local == "style" {
			var s string
			if err := dec.DecodeElement(&s, &start); err != nil {
				return nil, err
			}
			css.WriteString(s)
			continue
		}
		el := element{name: start.Name.Local, attrs: make(map[string]string, len(start.Attr))}
		for _, a := range start.Attr {
			el.attrs[a.Name.Local] = a.Value
		}
		elems = append(elems, el)
	}

	classes := parseCSS(css.String())
	img := &logoImage{w: 100, h: 100}
	for _, el := range elems {
		switch el.name {
		case "svg":
			if vb, ok := el.attrs["viewBox"]; ok {
				if f := parseFloats(vb); len(f) == 4 && f[2] > 0 && f[3] > 0 {
					img.minX, img.minY, img.w, img.h = f[0], f[1], f[2], f[3]
				}
			}
		case "rect":
			if sh, ok := rectShape(el.attrs, classes); ok {
				img.shapes = append(img.shapes, sh)
			}
		case "path":
			if sh, ok := pathShape(el.attrs, classes); ok {
				img.shapes = append(img.shapes, sh)
			}
		}
	}
	if len(img.shapes) == 0 {
		return nil, fmt.Errorf("SVG 里没有能画的形状")
	}
	return img, nil
}

type cssStyle struct{ fill, stroke string }

// parseCSS 认 `.cls-1 { fill: #fca326; }` 这种最朴素的写法
func parseCSS(s string) map[string]cssStyle {
	out := map[string]cssStyle{}
	for strings.Contains(s, "/*") {
		i := strings.Index(s, "/*")
		j := strings.Index(s[i:], "*/")
		if j < 0 {
			break
		}
		s = s[:i] + s[i+j+2:]
	}
	for _, block := range strings.Split(s, "}") {
		name, body, ok := strings.Cut(block, "{")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		if !strings.HasPrefix(name, ".") {
			continue
		}
		var st cssStyle
		for _, decl := range strings.Split(body, ";") {
			k, v, ok := strings.Cut(decl, ":")
			if !ok {
				continue
			}
			switch strings.TrimSpace(k) {
			case "fill":
				st.fill = strings.TrimSpace(v)
			case "stroke":
				st.stroke = strings.TrimSpace(v)
			}
		}
		out[strings.TrimPrefix(name, ".")] = st
	}
	return out
}

func attrOrClass(attrs map[string]string, classes map[string]cssStyle, name string) string {
	if v, ok := attrs[name]; ok {
		return v
	}
	if st, ok := classes[attrs["class"]]; ok {
		switch name {
		case "fill":
			return st.fill
		case "stroke":
			return st.stroke
		}
	}
	return ""
}

func parseColor(s string) (color.RGBA, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "", "none", "transparent":
		return color.RGBA{}, false
	case "black":
		return color.RGBA{A: 0xff}, true
	case "white":
		return color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}, true
	}
	if !strings.HasPrefix(s, "#") {
		return color.RGBA{}, false
	}
	h := s[1:]
	if len(h) == 3 {
		h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
	}
	if len(h) != 6 {
		return color.RGBA{}, false
	}
	v, err := strconv.ParseUint(h, 16, 32)
	if err != nil {
		return color.RGBA{}, false
	}
	return color.RGBA{R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v), A: 0xff}, true
}

func fnum(s string) float64 {
	v, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return v
}

func parseFloats(s string) []float64 {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == ' ' || r == ',' || r == '\t' || r == '\n' || r == '\r'
	})
	out := make([]float64, 0, len(fields))
	for _, f := range fields {
		v, err := strconv.ParseFloat(f, 64)
		if err != nil {
			return nil
		}
		out = append(out, v)
	}
	return out
}

func newShape(subs []logoSub) logoShape {
	sh := logoShape{subs: subs}
	for i, sub := range subs {
		if i == 0 {
			sh.minX, sh.minY, sh.maxX, sh.maxY = sub.minX, sub.minY, sub.maxX, sub.maxY
			continue
		}
		sh.minX = math.Min(sh.minX, sub.minX)
		sh.minY = math.Min(sh.minY, sub.minY)
		sh.maxX = math.Max(sh.maxX, sub.maxX)
		sh.maxY = math.Max(sh.maxY, sub.maxY)
	}
	return sh
}

// rectShape 把矩形（带圆角时用弧线近似）变成一条闭合折线
func rectShape(attrs map[string]string, classes map[string]cssStyle) (logoShape, bool) {
	x, y := fnum(attrs["x"]), fnum(attrs["y"])
	w, h := fnum(attrs["width"]), fnum(attrs["height"])
	if w <= 0 || h <= 0 {
		return logoShape{}, false
	}
	rx, hasRX := attrs["rx"]
	ry, hasRY := attrs["ry"]
	rxv, ryv := 0.0, 0.0
	if hasRX {
		rxv = fnum(rx)
	}
	if hasRY {
		ryv = fnum(ry)
	}
	if !hasRX && hasRY {
		rxv = ryv
	}
	if hasRX && !hasRY {
		ryv = rxv
	}
	rxv = math.Min(math.Max(rxv, 0), w/2)
	ryv = math.Min(math.Max(ryv, 0), h/2)

	sub := newSub(roundedRectPoints(x, y, w, h, rxv, ryv))
	sh := newShape([]logoSub{sub})
	applyPaint(&sh, attrs, classes)
	return sh, true
}

func pathShape(attrs map[string]string, classes map[string]cssStyle) (logoShape, bool) {
	subs := parsePathData(attrs["d"])
	if len(subs) == 0 {
		return logoShape{}, false
	}
	sh := newShape(subs)
	applyPaint(&sh, attrs, classes)
	return sh, true
}

// applyPaint 按属性 / class 决定填充和描边。SVG 默认填充是黑色，
// 只有 stroke 的直线路径面积为 0，这个默认值不会画出东西
func applyPaint(sh *logoShape, attrs map[string]string, classes map[string]cssStyle) {
	fillAttr := attrOrClass(attrs, classes, "fill")
	if fillAttr == "" {
		fillAttr = "#000000"
	}
	sh.fill, sh.hasFill = parseColor(fillAttr)
	sh.stroke, sh.hasStroke = parseColor(attrOrClass(attrs, classes, "stroke"))
	sh.strokeW = 1
	if v, ok := attrs["stroke-width"]; ok {
		sh.strokeW = fnum(v)
	}
}

func newSub(pts []logoPoint) logoSub {
	sub := logoSub{pts: pts, minX: math.Inf(1), minY: math.Inf(1), maxX: math.Inf(-1), maxY: math.Inf(-1)}
	for _, p := range pts {
		sub.minX = math.Min(sub.minX, p.x)
		sub.minY = math.Min(sub.minY, p.y)
		sub.maxX = math.Max(sub.maxX, p.x)
		sub.maxY = math.Max(sub.maxY, p.y)
	}
	return sub
}

// roundedRectPoints 用每段 6 条小线段画圆角，16 点阵上足够圆
func roundedRectPoints(x, y, w, h, rx, ry float64) []logoPoint {
	if rx <= 0 || ry <= 0 {
		return []logoPoint{{x, y}, {x + w, y}, {x + w, y + h}, {x, y + h}}
	}
	var pts []logoPoint
	arc := func(cx, cy, a0, a1 float64) {
		const steps = 6
		for i := 0; i <= steps; i++ {
			a := a0 + (a1-a0)*float64(i)/steps
			pts = append(pts, logoPoint{cx + rx*math.Cos(a), cy + ry*math.Sin(a)})
		}
	}
	// 从左上角圆弧的起点开始，顺时针一圈
	pts = append(pts, logoPoint{x + rx, y})
	pts = append(pts, logoPoint{x + w - rx, y})
	arc(x+w-rx, y+ry, -math.Pi/2, 0)
	pts = append(pts, logoPoint{x + w, y + h - ry})
	arc(x+w-rx, y+h-ry, 0, math.Pi/2)
	pts = append(pts, logoPoint{x + rx, y + h})
	arc(x+rx, y+h-ry, math.Pi/2, math.Pi)
	pts = append(pts, logoPoint{x, y + ry})
	arc(x+rx, y+ry, math.Pi, math.Pi*1.5)
	return pts
}

// ---------- path 的 d 属性 ----------

type pathLexer struct {
	s string
	i int
}

func (p *pathLexer) skip() {
	for p.i < len(p.s) {
		switch p.s[p.i] {
		case ' ', ',', '\t', '\n', '\r':
			p.i++
		default:
			return
		}
	}
}

func (p *pathLexer) hasNumber() bool {
	p.skip()
	if p.i >= len(p.s) {
		return false
	}
	c := p.s[p.i]
	return c == '-' || c == '+' || c == '.' || (c >= '0' && c <= '9')
}

func (p *pathLexer) number() (float64, bool) {
	if !p.hasNumber() {
		return 0, false
	}
	start := p.i
	i := p.i
	if p.s[i] == '+' || p.s[i] == '-' {
		i++
	}
	for i < len(p.s) && p.s[i] >= '0' && p.s[i] <= '9' {
		i++
	}
	if i < len(p.s) && p.s[i] == '.' {
		i++
		for i < len(p.s) && p.s[i] >= '0' && p.s[i] <= '9' {
			i++
		}
	}
	if i < len(p.s) && (p.s[i] == 'e' || p.s[i] == 'E') {
		j := i + 1
		if j < len(p.s) && (p.s[j] == '+' || p.s[j] == '-') {
			j++
		}
		if j < len(p.s) && p.s[j] >= '0' && p.s[j] <= '9' {
			for j < len(p.s) && p.s[j] >= '0' && p.s[j] <= '9' {
				j++
			}
			i = j
		}
	}
	v, err := strconv.ParseFloat(p.s[start:i], 64)
	if err != nil {
		return 0, false
	}
	p.i = i
	return v, true
}

func isPathCmd(c byte) bool {
	switch c {
	case 'M', 'm', 'L', 'l', 'H', 'h', 'V', 'v', 'C', 'c', 'S', 's', 'Q', 'q', 'T', 't', 'A', 'a', 'Z', 'z':
		return true
	}
	return false
}

// parsePathData 把 d 拆成若干条折线。曲线按弦长切成小段，
// 圆弧（A）直接连到终点——这几个招牌里没有圆弧
func parsePathData(d string) []logoSub {
	p := &pathLexer{s: d}
	var subs []logoSub
	var cur []logoPoint
	var pt, start, ctrl logoPoint
	var prev byte
	flush := func() {
		if len(cur) > 1 {
			subs = append(subs, newSub(cur))
		}
		cur = nil
	}
	for {
		p.skip()
		if p.i >= len(p.s) {
			break
		}
		cmd := p.s[p.i]
		if !isPathCmd(cmd) {
			// 认不出的东西跳过一个字符，别把整张图废掉
			p.i++
			continue
		}
		p.i++
		rel := cmd >= 'a' && cmd <= 'z'
		switch cmd {
		case 'M', 'm':
			first := true
			for p.hasNumber() {
				nx, _ := p.number()
				ny, _ := p.number()
				if rel {
					nx += pt.x
					ny += pt.y
				}
				pt = logoPoint{nx, ny}
				if first {
					flush()
					start = pt
					first = false
				}
				cur = append(cur, pt)
			}
			prev = cmd
			continue
		case 'L', 'l':
			for p.hasNumber() {
				nx, _ := p.number()
				ny, _ := p.number()
				if rel {
					nx += pt.x
					ny += pt.y
				}
				pt = logoPoint{nx, ny}
				cur = append(cur, pt)
			}
		case 'H', 'h':
			for p.hasNumber() {
				nx, _ := p.number()
				if rel {
					nx += pt.x
				}
				pt.x = nx
				cur = append(cur, pt)
			}
		case 'V', 'v':
			for p.hasNumber() {
				ny, _ := p.number()
				if rel {
					ny += pt.y
				}
				pt.y = ny
				cur = append(cur, pt)
			}
		case 'C', 'c':
			for p.hasNumber() {
				nx1, _ := p.number()
				ny1, _ := p.number()
				nx2, _ := p.number()
				ny2, _ := p.number()
				nx, _ := p.number()
				ny, _ := p.number()
				c1 := logoPoint{nx1, ny1}
				c2 := logoPoint{nx2, ny2}
				end := logoPoint{nx, ny}
				if rel {
					c1 = add(c1, pt)
					c2 = add(c2, pt)
					end = add(end, pt)
				}
				cur = appendCubic(cur, pt, c1, c2, end)
				ctrl = c2
				pt = end
			}
		case 'S', 's':
			for p.hasNumber() {
				nx2, _ := p.number()
				ny2, _ := p.number()
				nx, _ := p.number()
				ny, _ := p.number()
				c1 := pt
				if prev == 'C' || prev == 'c' || prev == 'S' || prev == 's' {
					c1 = logoPoint{2*pt.x - ctrl.x, 2*pt.y - ctrl.y}
				}
				c2 := logoPoint{nx2, ny2}
				end := logoPoint{nx, ny}
				if rel {
					c2 = add(c2, pt)
					end = add(end, pt)
				}
				cur = appendCubic(cur, pt, c1, c2, end)
				ctrl = c2
				pt = end
			}
		case 'Q', 'q':
			for p.hasNumber() {
				nx1, _ := p.number()
				ny1, _ := p.number()
				nx, _ := p.number()
				ny, _ := p.number()
				c := logoPoint{nx1, ny1}
				end := logoPoint{nx, ny}
				if rel {
					c = add(c, pt)
					end = add(end, pt)
				}
				cur = appendQuad(cur, pt, c, end)
				ctrl = c
				pt = end
			}
		case 'T', 't':
			for p.hasNumber() {
				nx, _ := p.number()
				ny, _ := p.number()
				c := pt
				if prev == 'Q' || prev == 'q' || prev == 'T' || prev == 't' {
					c = logoPoint{2*pt.x - ctrl.x, 2*pt.y - ctrl.y}
				}
				end := logoPoint{nx, ny}
				if rel {
					end = add(end, pt)
				}
				cur = appendQuad(cur, pt, c, end)
				ctrl = c
				pt = end
			}
		case 'A', 'a':
			for p.hasNumber() {
				for i := 0; i < 5; i++ {
					p.number()
				}
				nx, _ := p.number()
				ny, _ := p.number()
				end := logoPoint{nx, ny}
				if rel {
					end = add(end, pt)
				}
				cur = append(cur, end)
				pt = end
			}
		case 'Z', 'z':
			if len(cur) > 0 {
				cur = append(cur, start)
				flush()
			}
			pt = start
		}
		prev = cmd
	}
	flush()
	return subs
}

func add(a, b logoPoint) logoPoint { return logoPoint{a.x + b.x, a.y + b.y} }

func appendCubic(dst []logoPoint, p0, p1, p2, p3 logoPoint) []logoPoint {
	n := curveSteps(dist(p0, p1) + dist(p1, p2) + dist(p2, p3))
	for i := 1; i <= n; i++ {
		t := float64(i) / float64(n)
		u := 1 - t
		dst = append(dst, logoPoint{
			x: u*u*u*p0.x + 3*u*u*t*p1.x + 3*u*t*t*p2.x + t*t*t*p3.x,
			y: u*u*u*p0.y + 3*u*u*t*p1.y + 3*u*t*t*p2.y + t*t*t*p3.y,
		})
	}
	return dst
}

func appendQuad(dst []logoPoint, p0, p1, p2 logoPoint) []logoPoint {
	n := curveSteps(dist(p0, p1) + dist(p1, p2))
	for i := 1; i <= n; i++ {
		t := float64(i) / float64(n)
		u := 1 - t
		dst = append(dst, logoPoint{
			x: u*u*p0.x + 2*u*t*p1.x + t*t*p2.x,
			y: u*u*p0.y + 2*u*t*p1.y + t*t*p2.y,
		})
	}
	return dst
}

// curveSteps 按弦长定段数：用户单位越细的图，段数越多
func curveSteps(length float64) int {
	n := int(math.Ceil(length / 1.5))
	return min(max(n, 6), 48)
}

func dist(a, b logoPoint) float64 { return math.Hypot(b.x-a.x, b.y-a.y) }

// ---------- 光栅化 ----------

type logoDot struct {
	r, g, b float64 // 0..1，非预乘
	a       float64
}

func rasterizeLogo(img *logoImage) []logoDot {
	// 按内容裁到正方形：GitLab 官方的 SVG 四周留白很多，
	// 不裁的话狸猫只有中间几个点大；自己的方框招牌本来就铺满，裁了也一样
	minX, minY, maxX, maxY := img.minX, img.minY, img.minX+img.w, img.minY+img.h
	if len(img.shapes) > 0 {
		minX, minY, maxX, maxY = img.shapes[0].minX, img.shapes[0].minY, img.shapes[0].maxX, img.shapes[0].maxY
		for _, sh := range img.shapes[1:] {
			minX = math.Min(minX, sh.minX)
			minY = math.Min(minY, sh.minY)
			maxX = math.Max(maxX, sh.maxX)
			maxY = math.Max(maxY, sh.maxY)
		}
	}
	side := math.Max(maxX-minX, maxY-minY)
	if side <= 0 {
		return nil
	}
	minX -= (side - (maxX - minX)) / 2
	minY -= (side - (maxY - minY)) / 2

	scale := float64(logoDots) / side
	halfs := make([]float64, len(img.shapes))
	for i, sh := range img.shapes {
		if !sh.hasStroke || sh.strokeW <= 0 {
			continue
		}
		w := sh.strokeW
		if w*scale < minStrokeDots {
			w = minStrokeDots / scale
		}
		halfs[i] = w / 2
	}

	out := make([]logoDot, logoDots*logoDots)
	samples := float64(logoSS * logoSS)
	for dy := 0; dy < logoDots; dy++ {
		for dx := 0; dx < logoDots; dx++ {
			var r, g, b, a float64
			for sy := 0; sy < logoSS; sy++ {
				for sx := 0; sx < logoSS; sx++ {
					x := minX + (float64(dx)+(float64(sx)+0.5)/logoSS)*side/logoDots
					y := minY + (float64(dy)+(float64(sy)+0.5)/logoSS)*side/logoDots
					var sr, sg, sb, sa float64
					for i := range img.shapes {
						sh := &img.shapes[i]
						if x < sh.minX-halfs[i] || x > sh.maxX+halfs[i] ||
							y < sh.minY-halfs[i] || y > sh.maxY+halfs[i] {
							continue
						}
						inFill, inStroke := sh.hit(x, y, halfs[i])
						if inFill {
							sr, sg, sb, sa = chanR(sh.fill), chanG(sh.fill), chanB(sh.fill), 1
						}
						if inStroke {
							sr, sg, sb, sa = chanR(sh.stroke), chanG(sh.stroke), chanB(sh.stroke), 1
						}
					}
					r += sr * sa
					g += sg * sa
					b += sb * sa
					a += sa
				}
			}
			d := logoDot{a: a / samples}
			if a > 0 {
				d.r, d.g, d.b = r/a, g/a, b/a
			}
			out[dy*logoDots+dx] = d
		}
	}
	return out
}

func chanR(c color.RGBA) float64 { return float64(c.R) / 255 }
func chanG(c color.RGBA) float64 { return float64(c.G) / 255 }
func chanB(c color.RGBA) float64 { return float64(c.B) / 255 }

// hit 判断一个采样点在不在填充里、在不在描边里
func (sh *logoShape) hit(x, y float64, half float64) (inFill, inStroke bool) {
	if sh.hasFill {
		wind := 0
		for i := range sh.subs {
			sub := &sh.subs[i]
			if x < sub.minX || x > sub.maxX || y < sub.minY || y > sub.maxY {
				continue
			}
			wind += winding(sub.pts, x, y)
		}
		inFill = wind != 0
	}
	if sh.hasStroke && half > 0 {
		for i := range sh.subs {
			sub := &sh.subs[i]
			if x < sub.minX-half || x > sub.maxX+half || y < sub.minY-half || y > sub.maxY+half {
				continue
			}
			if nearPolyline(sub.pts, x, y, half) {
				inStroke = true
				break
			}
		}
	}
	return inFill, inStroke
}

// winding 是 nonzero 填充规则下的环绕数
func winding(pts []logoPoint, x, y float64) int {
	w := 0
	for i := range pts {
		a := pts[i]
		b := pts[(i+1)%len(pts)]
		if a.y <= y {
			if b.y > y && cross(a, b, x, y) > 0 {
				w++
			}
		} else if b.y <= y && cross(a, b, x, y) < 0 {
			w--
		}
	}
	return w
}

func cross(a, b logoPoint, x, y float64) float64 {
	return (b.x-a.x)*(y-a.y) - (x-a.x)*(b.y-a.y)
}

// nearPolyline 是点到折线的距离，圆头圆角就自然出来了
func nearPolyline(pts []logoPoint, x, y, half float64) bool {
	for i := 0; i+1 < len(pts); i++ {
		if segDist2(pts[i], pts[i+1], x, y) <= half*half {
			return true
		}
	}
	return false
}

func segDist2(a, b logoPoint, x, y float64) float64 {
	dx, dy := b.x-a.x, b.y-a.y
	l2 := dx*dx + dy*dy
	t := 0.0
	if l2 > 0 {
		t = ((x - a.x) * dx) / l2
		t = math.Max(0, math.Min(1, t))
	}
	px, py := a.x+t*dx, a.y+t*dy
	return (x-px)*(x-px) + (y-py)*(y-py)
}

// ---------- 点阵 -> 盲文字符 ----------

// 盲文一个格子是 2 列 × 4 行，[行][列] 对应的点位
var brailleBits = [4][2]rune{
	{0x01, 0x08},
	{0x02, 0x10},
	{0x04, 0x20},
	{0x40, 0x80},
}

type logoCell struct {
	ch    rune
	fg    color.RGBA
	bg    color.RGBA
	hasFG bool
	hasBG bool
}

// logoCells 把点阵折成字符格。每格挑一个底色，和底色不一样的墨点开成盲文点：
// 方框招牌整格都是底色时就画成实心块，没有底色的地方留白（终端自己的背景）
func logoCells(pix []logoDot) [][]logoCell {
	rows := make([][]logoCell, logoRowsN)
	for cy := 0; cy < logoRowsN; cy++ {
		row := make([]logoCell, logoWidth)
		for cx := 0; cx < logoWidth; cx++ {
			var dots [4][2]logoDot
			opaque := 0
			for j := 0; j < 4; j++ {
				for i := 0; i < 2; i++ {
					d := pix[(cy*4+j)*logoDots+cx*2+i]
					dots[j][i] = d
					if d.a >= 0.88 {
						opaque++
					}
				}
			}
			row[cx] = makeCell(dots, opaque == 8)
		}
		rows[cy] = row
	}
	return rows
}

func makeCell(dots [4][2]logoDot, full bool) logoCell {
	if full {
		bg := majorityColor(dots)
		var mask rune
		var fr, fg, fb, n float64
		for j := 0; j < 4; j++ {
			for i := 0; i < 2; i++ {
				d := dots[j][i]
				if colorDist(d, bg) <= inkDist {
					continue
				}
				mask |= brailleBits[j][i]
				fr += d.r
				fg += d.g
				fb += d.b
				n++
			}
		}
		if mask == 0 {
			// 整格一个颜色：画成实心块，底色也用同一个颜色，
			// 相邻格子拼起来没有缝
			return logoCell{ch: '⣿', fg: bg, bg: bg, hasFG: true, hasBG: true}
		}
		return logoCell{
			ch: 0x2800 + mask,
			fg: rgba(fr/n, fg/n, fb/n),
			bg: bg, hasFG: true, hasBG: true,
		}
	}

	var mask rune
	var fr, fg, fb, w float64
	for j := 0; j < 4; j++ {
		for i := 0; i < 2; i++ {
			d := dots[j][i]
			if d.a < 0.45 {
				continue
			}
			mask |= brailleBits[j][i]
			fr += d.r * d.a
			fg += d.g * d.a
			fb += d.b * d.a
			w += d.a
		}
	}
	if mask == 0 {
		return logoCell{ch: ' '}
	}
	return logoCell{ch: 0x2800 + mask, fg: rgba(fr/w, fg/w, fb/w), hasFG: true}
}

// majorityColor 取一格里面最多的那种颜色（量化到 16 级再数）
func majorityColor(dots [4][2]logoDot) color.RGBA {
	counts := map[uint32]int{}
	best, bestN := uint32(0), -1
	for j := 0; j < 4; j++ {
		for i := 0; i < 2; i++ {
			d := dots[j][i]
			k := uint32(d.r*15)<<8 | uint32(d.g*15)<<4 | uint32(d.b*15)
			counts[k]++
			if counts[k] > bestN {
				best, bestN = k, counts[k]
			}
		}
	}
	// 用这个桶里实际颜色的平均值，别拿量化后的粗值
	var r, g, b, n float64
	for j := 0; j < 4; j++ {
		for i := 0; i < 2; i++ {
			d := dots[j][i]
			if uint32(d.r*15)<<8|uint32(d.g*15)<<4|uint32(d.b*15) != best {
				continue
			}
			r += d.r
			g += d.g
			b += d.b
			n++
		}
	}
	return rgba(r/n, g/n, b/n)
}

func rgba(r, g, b float64) color.RGBA {
	clamp := func(v float64) uint8 {
		return uint8(math.Round(math.Max(0, math.Min(1, v)) * 255))
	}
	return color.RGBA{R: clamp(r), G: clamp(g), B: clamp(b), A: 0xff}
}

func colorDist(d logoDot, c color.RGBA) float64 {
	dr := d.r*255 - float64(c.R)
	dg := d.g*255 - float64(c.G)
	db := d.b*255 - float64(c.B)
	return math.Sqrt(dr*dr + dg*dg + db*db)
}

func hexColor(c color.RGBA) string { return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B) }

// styleLogoCells 把字符格拼成四行；同一副颜色只建一次样式，
// 终端颜色少的时候 lipgloss 会自己降级
func styleLogoCells(rows [][]logoCell) []string {
	type key struct {
		fg, bg       string
		hasFG, hasBG bool
	}
	cache := map[key]lipgloss.Style{}
	styleOf := func(c logoCell) lipgloss.Style {
		k := key{hasFG: c.hasFG, hasBG: c.hasBG}
		if c.hasFG {
			k.fg = hexColor(c.fg)
		}
		if c.hasBG {
			k.bg = hexColor(c.bg)
		}
		if st, ok := cache[k]; ok {
			return st
		}
		st := lipgloss.NewStyle()
		if c.hasFG {
			st = st.Foreground(lipgloss.Color(k.fg))
		}
		if c.hasBG {
			st = st.Background(lipgloss.Color(k.bg))
		}
		cache[k] = st
		return st
	}

	out := make([]string, len(rows))
	for y, row := range rows {
		var b strings.Builder
		for _, c := range row {
			b.WriteString(styleOf(c).Render(string(c.ch)))
		}
		out[y] = b.String()
	}
	return out
}
