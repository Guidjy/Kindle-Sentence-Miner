package render

import (
	"regexp"
	"strings"
)

// Ports of Yomitan's sanitizeCSS and addScopeToCssLegacy. Yomitan relies on
// the browser's CSS parser; this is a small parser for top level rules that
// produces equivalent output for dictionary styles.css files.

type cssRule struct {
	selector string
	body     string
	at       bool // @-rule; dropped when scoping, like non-CSSStyleRule rules
	raw      string
}

var cssComment = regexp.MustCompile(`(?s)/\*.*?\*/`)

func parseCSS(css string) []cssRule {
	css = cssComment.ReplaceAllString(css, "")
	var rules []cssRule
	i := 0
	for i < len(css) {
		for i < len(css) && isSpace(css[i]) {
			i++
		}
		if i >= len(css) {
			break
		}
		start := i
		// Find '{' or ';' (statement at-rule) at depth 0, skipping strings.
		for i < len(css) && css[i] != '{' && css[i] != ';' {
			i = skipString(css, i)
		}
		if i >= len(css) {
			break
		}
		prelude := strings.TrimSpace(css[start:i])
		if css[i] == ';' {
			i++
			if strings.HasPrefix(prelude, "@") {
				rules = append(rules, cssRule{at: true, raw: prelude + ";"})
			}
			continue
		}
		// Block: find the matching '}'.
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
		body := css[bodyStart : i-1]
		if strings.HasPrefix(prelude, "@") {
			rules = append(rules, cssRule{at: true, raw: prelude + " {" + body + "}"})
			continue
		}
		if prelude == "" {
			continue
		}
		rules = append(rules, cssRule{selector: normalizeSelectorList(prelude), body: normalizeDeclarations(body)})
	}
	return rules
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

var singleQuoted = regexp.MustCompile(`'([^'"\\]*)'`)

func normalizeDeclarations(body string) string {
	var decls []string
	for _, d := range splitTopLevel(body, ';') {
		d = strings.TrimSpace(d)
		if d == "" || strings.Contains(d, "{") {
			continue
		}
		i := strings.IndexByte(d, ':')
		if i <= 0 {
			continue
		}
		prop := strings.TrimSpace(d[:i])
		if !strings.HasPrefix(prop, "--") {
			prop = strings.ToLower(prop)
		}
		value := spaceRun.ReplaceAllString(strings.TrimSpace(d[i+1:]), " ")
		value = singleQuoted.ReplaceAllString(value, `"$1"`)
		decls = append(decls, prop+": "+value+";")
	}
	return strings.Join(decls, " ")
}

func (r cssRule) String() string {
	if r.at {
		return r.raw
	}
	if r.body == "" {
		return r.selector + " { }"
	}
	return r.selector + " { " + r.body + " }"
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

// addScopeToCSS prefixes every selector of every style rule with scope and
// drops @-rules (Yomitan's addScopeToCssLegacy).
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
