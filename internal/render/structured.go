package render

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/xythh/ann2html/internal/japanese"
)

// Port of Yomitan's StructuredContentGenerator
// (ext/js/display/structured-content-generator.js) as used for Anki notes.

// MediaResolver returns the Anki file name for a dictionary media file,
// uploading it on first use.
type MediaResolver func(dictionary, path string) (fileName string, ok bool)

type structuredContentGenerator struct {
	media MediaResolver
}

func jsNumber(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

func (g *structuredContentGenerator) createStructuredContent(content json.RawMessage, dictionary string) *node {
	n := elem("span", "structured-content")
	g.appendContent(n, content, dictionary, "")
	return n
}

// appendContent appends content to container. language is "" when unknown
// (null in Yomitan), in which case Japanese text marks its container lang="ja".
func (g *structuredContentGenerator) appendContent(container *node, content json.RawMessage, dictionary, language string) {
	content = trimJSON(content)
	if len(content) == 0 {
		return
	}
	switch content[0] {
	case '"':
		var s string
		json.Unmarshal(content, &s)
		if s == "" {
			return
		}
		container.append(textNode(s))
		if language == "" && japanese.IsStringPartiallyJapanese(s) {
			container.setAttr("lang", "ja")
		}
	case '[':
		var items []json.RawMessage
		json.Unmarshal(content, &items)
		for _, it := range items {
			g.appendContent(container, it, dictionary, language)
		}
	case '{':
		if n := g.createGenericElement(content, dictionary, language); n != nil {
			container.append(n)
		}
	}
}

func trimJSON(b json.RawMessage) json.RawMessage {
	return json.RawMessage(strings.TrimSpace(string(b)))
}

type scElement struct {
	Tag     string                     `json:"tag"`
	Content json.RawMessage            `json:"content"`
	Data    json.RawMessage            `json:"data"`
	Lang    *string                    `json:"lang"`
	Style   map[string]json.RawMessage `json:"style"`
	Title   *string                    `json:"title"`
	Open    *bool                      `json:"open"`
	ColSpan *float64                   `json:"colSpan"`
	RowSpan *float64                   `json:"rowSpan"`
	Href    string                     `json:"href"`
}

func (g *structuredContentGenerator) createGenericElement(raw json.RawMessage, dictionary, language string) *node {
	var el scElement
	if json.Unmarshal(raw, &el) != nil {
		return nil
	}
	switch el.Tag {
	case "br":
		return g.createElement(el, dictionary, language, "simple", false, false)
	case "ruby", "rt", "rp":
		return g.createElement(el, dictionary, language, "simple", true, false)
	case "table":
		container := elem("div", "gloss-sc-table-container")
		container.append(g.createElement(el, dictionary, language, "table", true, false))
		return container
	case "thead", "tbody", "tfoot", "tr":
		return g.createElement(el, dictionary, language, "table", true, false)
	case "th", "td":
		return g.createElement(el, dictionary, language, "table-cell", true, true)
	case "div", "span", "ol", "ul", "li", "details", "summary":
		return g.createElement(el, dictionary, language, "simple", true, true)
	case "img":
		var img imageData
		json.Unmarshal(raw, &img)
		return g.createDefinitionImage(img, dictionary)
	case "a":
		return g.createLink(el, dictionary, language)
	}
	return nil
}

func (g *structuredContentGenerator) createElement(el scElement, dictionary, language, kind string, hasChildren, hasStyle bool) *node {
	n := elem(el.Tag, "gloss-sc-"+el.Tag)
	for _, kv := range orderedObject(el.Data) {
		key := kv.key
		if key != "" {
			key = strings.ToUpper(key[:1]) + key[1:]
		}
		n.setData("sc"+key, jsValueString(kv.value))
	}
	if el.Lang != nil {
		n.setAttr("lang", *el.Lang)
		language = *el.Lang
	}
	if kind == "table-cell" {
		if el.ColSpan != nil {
			n.setAttr("colspan", jsNumber(*el.ColSpan))
		}
		if el.RowSpan != nil {
			n.setAttr("rowspan", jsNumber(*el.RowSpan))
		}
	}
	if hasStyle {
		if el.Style != nil {
			setStructuredContentStyle(n, el.Style)
		}
		if el.Title != nil {
			n.setAttr("title", *el.Title)
		}
		if el.Open != nil && *el.Open {
			n.setAttr("open", "")
		}
	}
	if hasChildren {
		g.appendContent(n, el.Content, dictionary, language)
	}
	return n
}

type keyValue struct {
	key   string
	value json.RawMessage
}

// orderedObject decodes a JSON object keeping its key order, which is the
// order Object.entries produces.
func orderedObject(raw json.RawMessage) []keyValue {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil
	}
	var out []keyValue
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return out
		}
		key, _ := tok.(string)
		var v json.RawMessage
		if dec.Decode(&v) != nil {
			return out
		}
		out = append(out, keyValue{key, v})
	}
	return out
}

// jsValueString converts a JSON value the way assigning it to a dataset
// property stringifies it.
func jsValueString(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var f float64
	if json.Unmarshal(raw, &f) == nil {
		return jsNumber(f)
	}
	var b bool
	if json.Unmarshal(raw, &b) == nil {
		return strconv.FormatBool(b)
	}
	if string(trimJSON(raw)) == "null" {
		return "null"
	}
	return "[object Object]"
}

var styleProps = []struct{ key, css string }{
	{"fontStyle", "font-style"}, {"fontWeight", "font-weight"}, {"fontSize", "font-size"},
	{"color", "color"}, {"background", "background"}, {"backgroundColor", "background-color"},
	{"verticalAlign", "vertical-align"}, {"textAlign", "text-align"}, {"textEmphasis", "text-emphasis"},
	{"textShadow", "text-shadow"},
}

func setStructuredContentStyle(n *node, style map[string]json.RawMessage) {
	str := func(k string) (string, bool) {
		raw, ok := style[k]
		if !ok {
			return "", false
		}
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return "", false
		}
		return s, true
	}
	num := func(k string) (float64, bool) {
		raw, ok := style[k]
		if !ok {
			return 0, false
		}
		var f float64
		if json.Unmarshal(raw, &f) != nil {
			return 0, false
		}
		return f, true
	}
	for _, p := range styleProps {
		if v, ok := str(p.key); ok {
			n.setStyle(p.css, v)
		}
	}
	if v, ok := str("textDecorationLine"); ok {
		n.setStyle("text-decoration", v)
	} else if raw, ok := style["textDecorationLine"]; ok {
		var list []string
		if json.Unmarshal(raw, &list) == nil {
			n.setStyle("text-decoration", strings.Join(list, " "))
		}
	}
	for _, p := range []struct{ key, css string }{
		{"textDecorationStyle", "text-decoration-style"}, {"textDecorationColor", "text-decoration-color"},
		{"borderColor", "border-color"}, {"borderStyle", "border-style"}, {"borderRadius", "border-radius"},
		{"borderWidth", "border-width"}, {"clipPath", "clip-path"}, {"margin", "margin"},
	} {
		if v, ok := str(p.key); ok {
			n.setStyle(p.css, v)
		}
	}
	for _, p := range []struct{ key, css string }{
		{"marginTop", "margin-top"}, {"marginLeft", "margin-left"}, {"marginRight", "margin-right"}, {"marginBottom", "margin-bottom"},
	} {
		if v, ok := num(p.key); ok {
			n.setStyle(p.css, jsNumber(v)+"em")
		}
		if v, ok := str(p.key); ok {
			n.setStyle(p.css, v)
		}
	}
	for _, p := range []struct{ key, css string }{
		{"padding", "padding"}, {"paddingTop", "padding-top"}, {"paddingLeft", "padding-left"},
		{"paddingRight", "padding-right"}, {"paddingBottom", "padding-bottom"}, {"wordBreak", "word-break"},
		{"whiteSpace", "white-space"}, {"cursor", "cursor"}, {"listStyleType", "list-style-type"},
	} {
		if v, ok := str(p.key); ok {
			n.setStyle(p.css, v)
		}
	}
}

func (g *structuredContentGenerator) createLink(el scElement, dictionary, language string) *node {
	href := el.Href
	internal := strings.HasPrefix(href, "?")
	n := elem("a", "gloss-link")
	n.setData("external", strconv.FormatBool(!internal))
	text := elem("span", "gloss-link-text")
	n.append(text)
	if el.Lang != nil {
		n.setAttr("lang", *el.Lang)
		language = *el.Lang
	}
	g.appendContent(text, el.Content, dictionary, language)
	if !internal {
		icon := elem("span", "gloss-link-external-icon icon")
		icon.setData("icon", "external-link")
		n.append(icon)
	}
	if internal {
		n.setAttr("href", "#")
	} else {
		n.setAttr("href", href)
	}
	return n
}

type imageData struct {
	Path            string   `json:"path"`
	Width           *float64 `json:"width"`
	Height          *float64 `json:"height"`
	PreferredWidth  *float64 `json:"preferredWidth"`
	PreferredHeight *float64 `json:"preferredHeight"`
	Title           *string  `json:"title"`
	Pixelated       bool     `json:"pixelated"`
	ImageRendering  *string  `json:"imageRendering"`
	Appearance      *string  `json:"appearance"`
	Background      *bool    `json:"background"`
	Collapsed       *bool    `json:"collapsed"`
	Collapsible     *bool    `json:"collapsible"`
	VerticalAlign   *string  `json:"verticalAlign"`
	Border          *string  `json:"border"`
	BorderRadius    *string  `json:"borderRadius"`
	SizeUnits       *string  `json:"sizeUnits"`
}

func (g *structuredContentGenerator) createDefinitionImage(d imageData, dictionary string) *node {
	width, height := 100.0, 100.0
	if d.Width != nil {
		width = *d.Width
	}
	if d.Height != nil {
		height = *d.Height
	}
	hasPW, hasPH := d.PreferredWidth != nil, d.PreferredHeight != nil
	invAspect := height / width
	if hasPW && hasPH {
		invAspect = *d.PreferredHeight / *d.PreferredWidth
	}
	usedWidth := width
	if hasPW {
		usedWidth = *d.PreferredWidth
	} else if hasPH {
		usedWidth = *d.PreferredHeight / invAspect
	}

	n := elem("a", "gloss-image-link")
	n.setAttr("target", "_blank")
	n.setAttr("rel", "noreferrer noopener")
	container := elem("span", "gloss-image-container")
	n.append(container)
	sizer := elem("span", "gloss-image-sizer")
	container.append(sizer)
	background := elem("span", "gloss-image-background")
	container.append(background)
	overlay := elem("span", "gloss-image-container-overlay")
	container.append(overlay)
	linkText := elem("span", "gloss-image-link-text")
	linkText.append(textNode("Image"))
	n.append(linkText)

	n.setData("path", d.Path)
	n.setData("dictionary", dictionary)
	n.setData("imageLoadState", "not-loaded")
	n.setData("hasAspectRatio", "true")
	rendering := "auto"
	if d.ImageRendering != nil {
		rendering = *d.ImageRendering
	} else if d.Pixelated {
		rendering = "pixelated"
	}
	n.setData("imageRendering", rendering)
	n.setData("appearance", strOr(d.Appearance, "auto"))
	n.setData("background", boolOr(d.Background, "true"))
	n.setData("collapsed", boolOr(d.Collapsed, "false"))
	n.setData("collapsible", boolOr(d.Collapsible, "true"))
	if d.VerticalAlign != nil {
		n.setData("verticalAlign", *d.VerticalAlign)
	}
	if d.SizeUnits != nil && (hasPW || hasPH) {
		n.setData("sizeUnits", *d.SizeUnits)
	}
	sizer.setStyle("padding-top", jsNumber(invAspect*100)+"%")
	if d.Border != nil {
		container.setStyle("border", *d.Border)
	}
	if d.BorderRadius != nil {
		container.setStyle("border-radius", *d.BorderRadius)
	}
	container.setStyle("width", jsNumber(usedWidth)+"em")
	if d.Title != nil {
		container.setAttr("title", *d.Title)
	}

	img := elem("img", "gloss-image")
	var imgWidth int64
	if d.SizeUnits != nil && *d.SizeUnits == "em" && (hasPW || hasPH) {
		const emSize, scaleFactor = 14, 2
		img.setStyle("width", jsNumber(usedWidth)+"em")
		img.setStyle("height", jsNumber(usedWidth*invAspect)+"em")
		imgWidth = int64(usedWidth * emSize * scaleFactor)
	} else {
		imgWidth = int64(usedWidth)
	}
	img.setAttr("width", strconv.FormatInt(imgWidth, 10))
	img.setAttr("height", strconv.FormatInt(int64(float64(imgWidth)*invAspect), 10))
	// Anki will not render images correctly without 100% width and height.
	img.setStyle("width", "100%")
	img.setStyle("height", "100%")
	container.append(img)

	if g.media != nil {
		if fileName, ok := g.media(dictionary, d.Path); ok {
			img.setAttr("src", fileName)
			n.setAttr("href", fileName)
			n.setData("imageLoadState", "loaded")
			background.setStyle("--image", `url("`+fileName+`")`)
		}
	}
	return n
}

func strOr(s *string, def string) string {
	if s != nil {
		return *s
	}
	return def
}

func boolOr(b *bool, def string) string {
	if b != nil {
		return strconv.FormatBool(*b)
	}
	return def
}

// ---- pronunciation (port of PronunciationGenerator) ----

func pronunciationText(morae []string, pitch japanese.Pitch, nasal, devoice []int) *node {
	has := func(list []int, v int) bool {
		for _, x := range list {
			if x == v {
				return true
			}
		}
		return false
	}
	container := elem("span", "pronunciation-text")
	for i, mora := range morae {
		high := japanese.IsMoraPitchHigh(i, pitch)
		highNext := japanese.IsMoraPitchHigh(i+1, pitch)
		n1 := elem("span", "pronunciation-mora")
		n1.setData("position", strconv.Itoa(i))
		n1.setData("pitch", map[bool]string{true: "high", false: "low"}[high])
		n1.setData("pitchNext", map[bool]string{true: "high", false: "low"}[highNext])
		var chars []*node
		for _, c := range mora {
			n2 := elem("span", "pronunciation-character")
			n2.append(textNode(string(c)))
			n1.append(n2)
			chars = append(chars, n2)
		}
		if has(devoice, i+1) {
			n1.setData("devoice", "true")
			n1.append(elem("span", "pronunciation-devoice-indicator"))
		}
		if has(nasal, i+1) && len(chars) > 0 {
			n1.setData("nasal", "true")
			group := elem("span", "pronunciation-character-group")
			n2 := chars[0]
			character := n2.textContent()
			if info, ok := japanese.KanaDiacriticInfo(character); ok {
				n1.setData("originalText", mora)
				n2.setData("originalText", character)
				n2.children = []*node{textNode(info.Character)}
			}
			diacritic := elem("span", "pronunciation-nasal-diacritic")
			diacritic.append(textNode("゚"))
			// The group takes the first character's place, then contains it.
			for idx, c := range n1.children {
				if c == n2 {
					n1.children[idx] = group
					group.parent = n1
					break
				}
			}
			group.append(n2, diacritic, elem("span", "pronunciation-nasal-indicator"))
		}
		n1.append(elem("span", "pronunciation-mora-line"))
		container.append(n1)
	}
	return container
}

func pronunciationGraph(morae []string, pitch japanese.Pitch) *node {
	ii := len(morae)
	svg := svgElem("svg")
	svg.setAttr("xmlns", "http://www.w3.org/2000/svg")
	svg.setAttr("class", "pronunciation-graph")
	svg.setAttr("focusable", "false")
	svg.setAttr("viewBox", "0 0 "+strconv.Itoa(50*(ii+1))+" 100")
	if ii <= 0 {
		return svg
	}
	path1 := svgElem("path")
	path2 := svgElem("path")
	svg.append(path1, path2)
	circle := func(class string, x, y int, r string) *node {
		c := svgElem("circle")
		c.setAttr("class", class)
		c.setAttr("cx", strconv.Itoa(x))
		c.setAttr("cy", strconv.Itoa(y))
		c.setAttr("r", r)
		return c
	}
	var points []string
	for i := 0; i < ii; i++ {
		high := japanese.IsMoraPitchHigh(i, pitch)
		highNext := japanese.IsMoraPitchHigh(i+1, pitch)
		x := i*50 + 25
		y := 75
		if high {
			y = 25
		}
		if high && !highNext {
			svg.append(circle("pronunciation-graph-dot-downstep1", x, y, "15"), circle("pronunciation-graph-dot-downstep2", x, y, "5"))
		} else {
			svg.append(circle("pronunciation-graph-dot", x, y, "15"))
		}
		points = append(points, strconv.Itoa(x)+" "+strconv.Itoa(y))
	}
	path1.setAttr("class", "pronunciation-graph-line")
	path1.setAttr("d", "M"+strings.Join(points, " L"))
	points = points[ii-1:]
	x := ii*50 + 25
	y := 75
	if japanese.IsMoraPitchHigh(ii, pitch) {
		y = 25
	}
	tri := svgElem("path")
	tri.setAttr("class", "pronunciation-graph-triangle")
	tri.setAttr("d", "M0 13 L15 -13 L-15 -13 Z")
	tri.setAttr("transform", "translate("+strconv.Itoa(x)+","+strconv.Itoa(y)+")")
	svg.append(tri)
	points = append(points, strconv.Itoa(x)+" "+strconv.Itoa(y))
	path2.setAttr("class", "pronunciation-graph-line-tail")
	path2.setAttr("d", "M"+strings.Join(points, " L"))
	return svg
}

func pronunciationDownstepPosition(pitch japanese.Pitch) *node {
	var s string
	if pitch.IsPattern() {
		parts := []string{}
		for _, p := range japanese.GetDownstepPositions(pitch.Pattern) {
			parts = append(parts, strconv.Itoa(p))
		}
		s = strings.Join(parts, ",")
	} else {
		s = strconv.Itoa(pitch.Position)
	}
	n1 := elem("span", "pronunciation-downstep-notation")
	n1.setData("downstepPosition", s)
	for _, part := range []struct{ class, text string }{
		{"pronunciation-downstep-notation-prefix", "["},
		{"pronunciation-downstep-notation-number", s},
		{"pronunciation-downstep-notation-suffix", "]"},
	} {
		n := elem("span", part.class)
		n.append(textNode(part.text))
		n1.append(n)
	}
	return n1
}
