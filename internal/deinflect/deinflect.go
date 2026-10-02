// Package deinflect runs Yomitan's own Japanese deinflection rules
// (ext/js/language/ja/japanese-transforms.js and the LanguageTransformer that
// applies them) in an embedded JavaScript engine. The JS files in js/ are
// copied verbatim from Yomitan (GPL-3.0); to update the rules, replace them.
package deinflect

import (
	"embed"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/dop251/goja"
)

//go:embed js/*.js
var sources embed.FS

// Load order matters: helpers first, then the rules, then the transformer.
var files = []string{"js/language-transforms.js", "js/japanese-transforms.js", "js/language-transformer.js"}

var (
	importLine = regexp.MustCompile(`(?m)^import\s[^;]*;\s*$`)
	exportWord = regexp.MustCompile(`(?m)^export\s+`)
)

// Result is one possible deinflection of a text.
type Result struct {
	Text       string
	Conditions int
	// Rules are the transform ids applied, most recent first (Yomitan's
	// inflectionRules order).
	Rules []string
}

type Deinflector struct {
	mu        sync.Mutex
	vm        *goja.Runtime
	transform goja.Callable
	flags     goja.Callable
	names     goja.Callable
	cache     map[string][]Result
}

// New loads Yomitan's Japanese transforms.
func New() (*Deinflector, error) {
	var src strings.Builder
	src.WriteString("var log = {log() {}, warn() {}, error() {}};\n")
	for _, f := range files {
		b, err := sources.ReadFile(f)
		if err != nil {
			return nil, err
		}
		s := importLine.ReplaceAllString(string(b), "")
		s = exportWord.ReplaceAllString(s, "")
		src.WriteString(s)
		src.WriteString("\n")
	}
	src.WriteString(`
var __transformer = new LanguageTransformer();
__transformer.addDescriptor(japaneseTransforms);
function __transform(text) {
	return JSON.stringify(__transformer.transform(text).map((r) => [r.text, r.conditions, r.trace.map((f) => f.transform)]));
}
function __flags(partsOfSpeech) {
	return __transformer.getConditionFlagsFromPartsOfSpeech(JSON.parse(partsOfSpeech));
}
function __names(rules) {
	return JSON.stringify(__transformer.getUserFacingInflectionRules(JSON.parse(rules)).map((r) => r.name));
}
`)
	vm := goja.New()
	if _, err := vm.RunScript("yomitan-transforms.js", src.String()); err != nil {
		return nil, fmt.Errorf("loading deinflection rules: %w", err)
	}
	d := &Deinflector{vm: vm, cache: map[string][]Result{}}
	for name, dst := range map[string]*goja.Callable{"__transform": &d.transform, "__flags": &d.flags, "__names": &d.names} {
		fn, ok := goja.AssertFunction(vm.Get(name))
		if !ok {
			return nil, fmt.Errorf("deinflection rules: %s is not a function", name)
		}
		*dst = fn
	}
	return d, nil
}

// Transform returns every deinflection candidate of text, including text
// itself with no conditions.
func (d *Deinflector) Transform(text string) ([]Result, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if r, ok := d.cache[text]; ok {
		return r, nil
	}
	v, err := d.transform(goja.Undefined(), d.vm.ToValue(text))
	if err != nil {
		return nil, err
	}
	var raw [][3]json.RawMessage
	if err := json.Unmarshal([]byte(v.String()), &raw); err != nil {
		return nil, err
	}
	out := make([]Result, len(raw))
	for i, r := range raw {
		json.Unmarshal(r[0], &out[i].Text)
		json.Unmarshal(r[1], &out[i].Conditions)
		json.Unmarshal(r[2], &out[i].Rules)
	}
	d.cache[text] = out
	return out, nil
}

// ConditionFlags converts a dictionary entry's rules (parts of speech such
// as "v1" or "adj-i") into condition flags.
func (d *Deinflector) ConditionFlags(partsOfSpeech []string) int {
	if len(partsOfSpeech) == 0 {
		return 0
	}
	b, _ := json.Marshal(partsOfSpeech)
	d.mu.Lock()
	defer d.mu.Unlock()
	v, err := d.flags(goja.Undefined(), d.vm.ToValue(string(b)))
	if err != nil {
		return 0
	}
	return int(v.ToInteger())
}

// ConditionsMatch is LanguageTransformer.conditionsMatch.
func ConditionsMatch(current, next int) bool {
	return current == 0 || current&next != 0
}

// RuleNames returns the user facing names of inflection rules, e.g. "past".
func (d *Deinflector) RuleNames(rules []string) []string {
	if len(rules) == 0 {
		return nil
	}
	b, _ := json.Marshal(rules)
	d.mu.Lock()
	defer d.mu.Unlock()
	v, err := d.names(goja.Undefined(), d.vm.ToValue(string(b)))
	if err != nil {
		return rules
	}
	var names []string
	if json.Unmarshal([]byte(v.String()), &names) != nil {
		return rules
	}
	return names
}
