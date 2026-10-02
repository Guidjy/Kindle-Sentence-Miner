package render

import (
	_ "embed"
	"encoding/json"
	"strings"
	"unicode"
)

// A minimal DOM, enough to reproduce what Yomitan's AnkiTemplateRenderer
// does in the browser: build elements, inline the class based styles from
// its style tables, strip non structured-content data attributes and
// serialize with innerHTML semantics.

type attr struct{ name, value string }

type node struct {
	tag      string // "" for text nodes
	text     string
	svg      bool
	attrs    []attr
	style    []attr // inline style declarations set through element.style
	children []*node
	parent   *node
}

func elem(tag, class string) *node {
	n := &node{tag: tag}
	if class != "" {
		n.setAttr("class", class)
	}
	return n
}

func svgElem(tag string) *node { return &node{tag: tag, svg: true} }

func textNode(s string) *node { return &node{text: s} }

func (n *node) append(children ...*node) *node {
	for _, c := range children {
		c.parent = n
		n.children = append(n.children, c)
	}
	return n
}

func (n *node) getAttr(name string) (string, bool) {
	for _, a := range n.attrs {
		if a.name == name {
			return a.value, true
		}
	}
	return "", false
}

func (n *node) setAttr(name, value string) {
	for i := range n.attrs {
		if n.attrs[i].name == name {
			n.attrs[i].value = value
			return
		}
	}
	n.attrs = append(n.attrs, attr{name, value})
}

func (n *node) removeAttr(name string) {
	for i := range n.attrs {
		if n.attrs[i].name == name {
			n.attrs = append(n.attrs[:i], n.attrs[i+1:]...)
			return
		}
	}
}

// setData sets element.dataset[key], converting camelCase to data-kebab-case.
func (n *node) setData(key, value string) {
	var b strings.Builder
	b.WriteString("data-")
	for _, r := range key {
		if unicode.IsUpper(r) {
			b.WriteByte('-')
			r = unicode.ToLower(r)
		}
		b.WriteRune(r)
	}
	n.setAttr(b.String(), value)
}

// setStyle sets element.style[prop]; the style attribute keeps the position
// of its first assignment, like in a browser.
func (n *node) setStyle(prop, value string) {
	found := false
	for i := range n.style {
		if n.style[i].name == prop {
			n.style[i].value = value
			found = true
		}
	}
	if !found {
		n.style = append(n.style, attr{prop, value})
	}
	n.setAttr("style", n.cssText())
}

func (n *node) cssText() string {
	parts := make([]string, len(n.style))
	for i, d := range n.style {
		parts[i] = d.name + ": " + d.value + ";"
	}
	return strings.Join(parts, " ")
}

func (n *node) textContent() string {
	if n.tag == "" {
		return n.text
	}
	var b strings.Builder
	for _, c := range n.children {
		b.WriteString(c.textContent())
	}
	return b.String()
}

// walk visits all descendants (not n itself) in document order.
func (n *node) walk(fn func(*node)) {
	for _, c := range n.children {
		fn(c)
		c.walk(fn)
	}
}

var voidElements = map[string]bool{"br": true, "img": true, "hr": true, "wbr": true, "input": true}

func escapeText(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", " ", "&nbsp;").Replace(s)
}

func escapeAttr(s string) string {
	return strings.NewReplacer("&", "&amp;", "\"", "&quot;", "<", "&lt;", ">", "&gt;", " ", "&nbsp;").Replace(s)
}

func (n *node) outerHTML(b *strings.Builder) {
	if n.tag == "" {
		b.WriteString(escapeText(n.text))
		return
	}
	b.WriteByte('<')
	b.WriteString(n.tag)
	for _, a := range n.attrs {
		b.WriteByte(' ')
		b.WriteString(a.name)
		b.WriteString(`="`)
		b.WriteString(escapeAttr(a.value))
		b.WriteByte('"')
	}
	b.WriteByte('>')
	if !n.svg && voidElements[n.tag] {
		return
	}
	n.innerHTML(b)
	b.WriteString("</")
	b.WriteString(n.tag)
	b.WriteByte('>')
}

func (n *node) innerHTML(b *strings.Builder) {
	for _, c := range n.children {
		c.outerHTML(b)
	}
}

// ---- class style application (port of CssStyleApplier) ----

//go:embed styles/structured-content-style.json
var structuredContentStyleJSON []byte

//go:embed styles/pronunciation-style.json
var pronunciationStyleJSON []byte

type styleRule struct {
	selectors []selector
	rawSel    string
	css       string // "prop:value;" concatenated
}

type styleSheet []styleRule

var (
	structuredContentStyles = parseStyleJSON(structuredContentStyleJSON)
	pronunciationStyles     = parseStyleJSON(pronunciationStyleJSON)
)

func parseStyleJSON(data []byte) styleSheet {
	var raw []struct {
		Selectors []string    `json:"selectors"`
		Styles    [][2]string `json:"styles"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		panic(err)
	}
	var out styleSheet
	for _, r := range raw {
		var css strings.Builder
		for _, s := range r.Styles {
			css.WriteString(s[0] + ":" + s[1] + ";")
		}
		rule := styleRule{rawSel: strings.Join(r.Selectors, ","), css: css.String()}
		for _, s := range r.Selectors {
			rule.selectors = append(rule.selectors, parseSelector(s))
		}
		out = append(out, rule)
	}
	return out
}

// selectorMightMatch is the cheap pre-filter from CssStyleApplier.
func selectorMightMatch(selectors string, classes []string) bool {
	for _, c := range classes {
		prefixed := "." + c
		start := 0
		for {
			i := strings.Index(selectors[start:], prefixed)
			if i < 0 {
				break
			}
			start += i + len(prefixed)
			if start >= len(selectors) || !isClassNameChar(selectors[start]) {
				return true
			}
		}
	}
	return false
}

func isClassNameChar(c byte) bool {
	return c == '-' || c == '_' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// applyClassStyles inlines the sheet's rules for every classed element
// under root, then removes the class attributes.
func applyClassStyles(root *node, sheet styleSheet) {
	type pending struct {
		n     *node
		style string
	}
	var updates []pending
	root.walk(func(n *node) {
		if n.tag == "" {
			return
		}
		class, ok := n.getAttr("class")
		if !ok || class == "" {
			return
		}
		classes := strings.Fields(class)
		var css strings.Builder
		for _, rule := range sheet {
			if !selectorMightMatch(rule.rawSel, classes) {
				continue
			}
			for _, sel := range rule.selectors {
				if sel.matches(n) {
					css.WriteString(rule.css)
					break
				}
			}
		}
		existing, _ := n.getAttr("style")
		css.WriteString(existing)
		updates = append(updates, pending{n, css.String()})
	})
	for _, u := range updates {
		u.n.removeAttr("class")
		if u.style != "" {
			u.n.setAttr("style", u.style)
		} else {
			u.n.removeAttr("style")
		}
	}
}

// normalizeHTML mirrors AnkiTemplateRenderer._normalizeHtml: inline class
// styles, drop data attributes not matching keepData, and turn newlines in
// text into <br>.
func normalizeHTML(root *node, sheet styleSheet, keepData func(name string) bool) {
	applyClassStyles(root, sheet)
	var texts []*node
	root.walk(func(n *node) {
		if n.tag == "" {
			texts = append(texts, n)
			return
		}
		kept := n.attrs[:0]
		for _, a := range n.attrs {
			if strings.HasPrefix(a.name, "data-") && (keepData == nil || !keepData(a.name)) {
				continue
			}
			kept = append(kept, a)
		}
		n.attrs = kept
	})
	for _, t := range texts {
		parts := strings.Split(t.text, "\n")
		if len(parts) <= 1 || t.parent == nil {
			continue
		}
		var replacement []*node
		for i, p := range parts {
			if i > 0 {
				replacement = append(replacement, elem("br", ""))
			}
			replacement = append(replacement, textNode(p))
		}
		parent := t.parent
		var children []*node
		for _, c := range parent.children {
			if c == t {
				for _, r := range replacement {
					r.parent = parent
				}
				children = append(children, replacement...)
			} else {
				children = append(children, c)
			}
		}
		parent.children = children
	}
}

// keepStructuredContentData keeps data-sc-* attributes, i.e. dataset keys
// matching /^sc([^a-z]|$)/.
func keepStructuredContentData(name string) bool {
	rest := strings.TrimPrefix(name, "data-")
	return rest == "sc" || strings.HasPrefix(rest, "sc-")
}

// html renders node the way AnkiTemplateRenderer._getHtml does.
func html(n *node, sheet styleSheet, keepData func(string) bool) string {
	container := elem("div", "")
	container.append(n)
	normalizeHTML(container, sheet, keepData)
	var b strings.Builder
	container.innerHTML(&b)
	return b.String()
}

// ---- selectors ----

type attrTest struct {
	name, op, value string
	negate          bool
}

type compound struct {
	classes    []string
	attrs      []attrTest
	impossible bool // pseudo-classes/elements that never match in a static render
}

type selector struct {
	parts       []compound
	combinators []byte // combinators[i] joins parts[i] and parts[i+1]: ' ' or '>' or '~'
}

func parseSelector(s string) selector {
	var sel selector
	s = strings.TrimSpace(s)
	isComb := func(c byte) bool { return c == ' ' || c == '>' || c == '~' || c == '+' }
	i := 0
	for {
		var cur compound
		for i < len(s) && !isComb(s[i]) {
			c := s[i]
			switch {
			case c == '.':
				j := i + 1
				for j < len(s) && isClassNameChar(s[j]) {
					j++
				}
				cur.classes = append(cur.classes, s[i+1:j])
				i = j
			case c == '[':
				j := strings.IndexByte(s[i:], ']') + i
				cur.attrs = append(cur.attrs, parseAttrTest(s[i+1:j], false))
				i = j + 1
			case strings.HasPrefix(s[i:], ":not("):
				j := strings.IndexByte(s[i:], ')') + i
				inner := s[i+5 : j]
				if strings.HasPrefix(inner, "[") && strings.HasSuffix(inner, "]") {
					cur.attrs = append(cur.attrs, parseAttrTest(inner[1:len(inner)-1], true))
				} else {
					cur.impossible = true
				}
				i = j + 1
			case c == ':':
				// :root, :hover, ::after, :nth-of-type(...) never match a
				// detached static render.
				j := i + 1
				for j < len(s) && (s[j] == ':' || isClassNameChar(s[j])) {
					j++
				}
				if j < len(s) && s[j] == '(' {
					j = strings.IndexByte(s[j:], ')') + j + 1
				}
				cur.impossible = true
				i = j
			default:
				j := i
				for j < len(s) && isClassNameChar(s[j]) {
					j++
				}
				if j == i {
					j++
				}
				cur.attrs = append(cur.attrs, attrTest{name: "\x00tag", op: "=", value: s[i:j]})
				i = j
			}
		}
		sel.parts = append(sel.parts, cur)
		comb := byte(' ')
		for i < len(s) && isComb(s[i]) {
			if s[i] != ' ' {
				comb = s[i]
			}
			i++
		}
		if i >= len(s) {
			return sel
		}
		sel.combinators = append(sel.combinators, comb)
	}
}

func parseAttrTest(s string, negate bool) attrTest {
	for _, op := range []string{"^=", "$=", "*=", "~=", "="} {
		if i := strings.Index(s, op); i >= 0 {
			return attrTest{name: s[:i], op: op, value: strings.Trim(s[i+len(op):], `"'`), negate: negate}
		}
	}
	return attrTest{name: s, op: "", negate: negate}
}

func (c compound) matches(n *node) bool {
	if c.impossible || n.tag == "" {
		return false
	}
	if len(c.classes) > 0 {
		class, _ := n.getAttr("class")
		have := strings.Fields(class)
		for _, want := range c.classes {
			found := false
			for _, h := range have {
				if h == want {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
	}
	for _, t := range c.attrs {
		var ok bool
		if t.name == "\x00tag" {
			ok = n.tag == t.value
		} else {
			v, present := n.getAttr(t.name)
			switch t.op {
			case "":
				ok = present
			case "=":
				ok = present && v == t.value
			case "^=":
				ok = present && strings.HasPrefix(v, t.value)
			case "$=":
				ok = present && strings.HasSuffix(v, t.value)
			case "*=":
				ok = present && strings.Contains(v, t.value)
			case "~=":
				ok = present && contains(strings.Fields(v), t.value)
			}
		}
		if ok == t.negate {
			return false
		}
	}
	return true
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func (s selector) matches(n *node) bool {
	return s.matchFrom(len(s.parts)-1, n)
}

func (s selector) matchFrom(i int, n *node) bool {
	if !s.parts[i].matches(n) {
		return false
	}
	if i == 0 {
		return true
	}
	switch s.combinators[i-1] {
	case '>':
		return n.parent != nil && s.matchFrom(i-1, n.parent)
	case ' ':
		for p := n.parent; p != nil; p = p.parent {
			if s.matchFrom(i-1, p) {
				return true
			}
		}
	case '~', '+':
		if n.parent == nil {
			return false
		}
		var prev []*node
		for _, c := range n.parent.children {
			if c == n {
				break
			}
			if c.tag != "" {
				prev = append(prev, c)
			}
		}
		if s.combinators[i-1] == '+' && len(prev) > 0 {
			prev = prev[len(prev)-1:]
		}
		for _, p := range prev {
			if s.matchFrom(i-1, p) {
				return true
			}
		}
	}
	return false
}
