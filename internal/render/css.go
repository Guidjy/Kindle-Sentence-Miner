package render

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Ports of Yomitan's sanitizeCSS and addScopeToCssLegacy. Yomitan relies on
// the browser's CSS parser and serializer (CSSStyleSheet.cssRules[].cssText);
// this reproduces Chrome's output for dictionary styles.css files, including
// nested rules and Chrome's value normalization (hex colors to rgb(), 0 to
// 0px for lengths).

type cssRule struct {
	selector string
	decls    []string // "prop: value;" serialized declarations
	children []cssRule
	at       bool // @-rule; dropped when scoping, like non-CSSStyleRule rules
	raw      string
}

var cssComment = regexp.MustCompile(`(?s)/\*.*?\*/`)

func parseCSS(css string) []cssRule {
	rules, _ := parseBlock(cssComment.ReplaceAllString(css, ""), false)
	return rules
}

// parseBlock parses rules (and, inside a style rule, declarations).
func parseBlock(css string, inRule bool) (rules []cssRule, decls []string) {
	i := 0
	for i < len(css) {
		for i < len(css) && (isSpace(css[i]) || css[i] == ';') {
			i++
		}
		if i >= len(css) {
			break
		}
		start := i
		for i < len(css) && css[i] != '{' && css[i] != ';' && css[i] != '}' {
			i = skipString(css, i)
		}
		prelude := strings.TrimSpace(css[start:min(i, len(css))])
		if i >= len(css) || css[i] != '{' {
			// A declaration (or a statement at-rule at the top level).
			if inRule {
				if d, ok := normalizeDeclaration(prelude); ok {
					decls = append(decls, d)
				}
			} else if strings.HasPrefix(prelude, "@") {
				rules = append(rules, cssRule{at: true, raw: prelude + ";"})
			}
			if i < len(css) {
				i++
			}
			continue
		}
		bodyStart := i + 1
		depth := 0
		for i < len(css) {
			switch css[i] {
			case '{':
				depth++
			case '}':
				depth--
			case '"', '\'':
				i = skipString(css, i) - 1
			}
			i++
			if depth == 0 {
				break
			}
		}
		body := css[bodyStart:max(bodyStart, i-1)]
		if strings.HasPrefix(prelude, "@") {
			rules = append(rules, cssRule{at: true, raw: prelude + " {" + body + "}"})
			continue
		}
		if prelude == "" {
			continue
		}
		children, ds := parseBlock(body, true)
		rules = append(rules, cssRule{selector: normalizeSelectorList(prelude), decls: ds, children: children})
	}
	return rules, decls
}

func isSpace(c byte) bool { return c == ' ' || c == '\n' || c == '\t' || c == '\r' || c == '\f' }

// skipString returns the index after the string starting at i, or i+1.
func skipString(s string, i int) int {
	q := s[i]
	if q != '"' && q != '\'' {
		return i + 1
	}
	for j := i + 1; j < len(s); j++ {
		if s[j] == '\\' {
			j++
			continue
		}
		if s[j] == q {
			return j + 1
		}
	}
	return len(s)
}

var (
	spaceRun        = regexp.MustCompile(`\s+`)
	combinatorSpace = regexp.MustCompile(`\s*([>~+])\s*`)
)

func normalizeSelector(s string) string {
	s = spaceRun.ReplaceAllString(strings.TrimSpace(s), " ")
	return combinatorSpace.ReplaceAllString(s, " $1 ")
}

func normalizeSelectorList(s string) string {
	parts := splitTopLevel(s, ',')
	for i := range parts {
		parts[i] = normalizeSelector(parts[i])
	}
	return strings.Join(parts, ", ")
}

// splitTopLevel splits on sep outside parentheses, brackets and strings.
func splitTopLevel(s string, sep byte) []string {
	var parts []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(', '[':
			depth++
		case ')', ']':
			depth--
		case '"', '\'':
			i = skipString(s, i) - 1
		case sep:
			if depth == 0 {
				parts = append(parts, s[start:i])
				start = i + 1
			}
		}
	}
	return append(parts, s[start:])
}

var (
	singleQuoted = regexp.MustCompile(`'([^'"\\]*)'`)
	hexColor     = regexp.MustCompile(`#([0-9a-fA-F]{8}|[0-9a-fA-F]{6}|[0-9a-fA-F]{3,4})\b`)
)

// lengthProperties get a bare 0 serialized as 0px, like Chrome does.
var lengthProperties = map[string]bool{
	"margin": true, "margin-top": true, "margin-right": true, "margin-bottom": true, "margin-left": true,
	"padding": true, "padding-top": true, "padding-right": true, "padding-bottom": true, "padding-left": true,
	"width": true, "height": true, "min-width": true, "min-height": true, "max-width": true, "max-height": true,
	"top": true, "right": true, "bottom": true, "left": true, "border-width": true, "border-top-width": true,
	"border-right-width": true, "border-bottom-width": true, "border-left-width": true, "border-radius": true,
	"gap": true, "row-gap": true, "column-gap": true, "text-indent": true, "letter-spacing": true, "font-size": true,
	"margin-inline-start": true, "margin-inline-end": true, "padding-inline-start": true, "padding-inline-end": true,
}

func normalizeDeclaration(d string) (string, bool) {
	i := strings.IndexByte(d, ':')
	if i <= 0 {
		return "", false
	}
	prop := strings.TrimSpace(d[:i])
	value := strings.TrimSpace(d[i+1:])
	if strings.HasPrefix(prop, "--") {
		return prop + ": " + value + ";", true
	}
	prop = strings.ToLower(prop)
	value = spaceRun.ReplaceAllString(value, " ")
	value = singleQuoted.ReplaceAllString(value, `"$1"`)
	// Values with var() are kept as written until substitution.
	if !strings.Contains(value, "var(") {
		value = hexColor.ReplaceAllStringFunc(value, hexToRGB)
		if lengthProperties[prop] {
			fields := strings.Split(value, " ")
			for j, f := range fields {
				if f == "0" {
					fields[j] = "0px"
				}
			}
			value = strings.Join(fields, " ")
		}
	}
	return prop + ": " + value + ";", true
}

func hexToRGB(hex string) string {
	h := hex[1:]
	if len(h) == 3 || len(h) == 4 {
		var b strings.Builder
		for _, c := range h {
			b.WriteRune(c)
			b.WriteRune(c)
		}
		h = b.String()
	}
	v, err := strconv.ParseUint(h, 16, 32)
	if err != nil {
		return hex
	}
	if len(h) == 6 {
		return fmt.Sprintf("rgb(%d, %d, %d)", v>>16&0xff, v>>8&0xff, v&0xff)
	}
	return fmt.Sprintf("rgba(%d, %d, %d, %s)", v>>24&0xff, v>>16&0xff, v>>8&0xff, alphaString(int(v&0xff)))
}

// alphaString serializes an 8-bit alpha with the fewest decimals that
// round-trip, like Chrome.
func alphaString(a int) string {
	if a == 255 {
		return "1"
	}
	for _, scale := range []float64{100, 1000} {
		f := math.Round(float64(a)/255*scale) / scale
		if int(math.Round(f*255)) == a {
			return strconv.FormatFloat(f, 'f', -1, 64)
		}
	}
	return strconv.FormatFloat(float64(a)/255, 'f', 3, 64)
}

// String serializes like CSSStyleRule.cssText: flat rules on one line,
// rules with nested rules over several lines.
func (r cssRule) String() string {
	if r.at {
		return r.raw
	}
	if len(r.children) == 0 {
		if len(r.decls) == 0 {
			return r.selector + " { }"
		}
		return r.selector + " { " + strings.Join(r.decls, " ") + " }"
	}
	var b strings.Builder
	b.WriteString(r.selector + " {\n")
	if len(r.decls) > 0 {
		b.WriteString("  " + strings.Join(r.decls, " ") + "\n")
	}
	for _, c := range r.children {
		// Chrome indents only the first line of a nested rule.
		b.WriteString("  " + c.String() + "\n")
	}
	b.WriteString("}")
	return b.String()
}

// sanitizeCSS re-serializes CSS through the parser, dropping anything that
// does not parse as a rule.
func sanitizeCSS(css string) string {
	rules := parseCSS(css)
	out := make([]string, len(rules))
	for i, r := range rules {
		out[i] = r.String()
	}
	return strings.Join(out, "\n")
}

// addScopeToCSS prefixes every top level selector with scope and drops
// @-rules (Yomitan's addScopeToCssLegacy).
func addScopeToCSS(css, scope string) string {
	var out []string
	for _, r := range parseCSS(css) {
		if r.at {
			continue
		}
		sels := splitTopLevel(r.selector, ',')
		for i := range sels {
			sels[i] = scope + " " + strings.TrimSpace(sels[i])
		}
		r.selector = strings.Join(sels, ", ")
		out = append(out, r.String())
	}
	return strings.Join(out, "\n")
}

func addGlossaryScope(css string) string { return addScopeToCSS(css, ".yomitan-glossary") }

func addDictionaryScope(css, dictionary string) string {
	escaped := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(dictionary)
	return addScopeToCSS(css, `[data-dictionary="`+escaped+`"]`)
}

const ankiCompactGlossStyles = `ul[data-sc-content="glossary"] > li:not(:first-child)::before {
  white-space: pre-wrap;
  content: ' | ';
  display: inline;
  color: #777777;
}

ul[data-sc-content="glossary"] > li {
  display: inline;
}

ul[data-sc-content="glossary"] {
  display: inline;
  list-style: none;
  padding-left: 0;
}`
