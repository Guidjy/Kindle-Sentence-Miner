package deinflect

import "testing"

func TestTransform(t *testing.T) {
	d, err := New()
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"いたわって": "いたわる",
		"食べた":   "食べる",
		"散らした":  "散らす",
		"芳しく":   "芳しい",
		"見開いた":  "見開く",
		"写されて":  "写す",
	}
	for in, want := range cases {
		results, err := d.Transform(in)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, r := range results {
			if r.Text == want && len(r.Rules) > 0 {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: no deinflection to %s in %v", in, want, results)
		}
	}
}

func TestConditions(t *testing.T) {
	d, err := New()
	if err != nil {
		t.Fatal(err)
	}
	results, _ := d.Transform("食べた")
	var past Result
	for _, r := range results {
		if r.Text == "食べる" {
			past = r
		}
	}
	if past.Conditions == 0 {
		t.Fatal("expected conditions on deinflected verb")
	}
	if !ConditionsMatch(past.Conditions, d.ConditionFlags([]string{"v1"})) {
		t.Error("ichidan verb should match")
	}
	if ConditionsMatch(past.Conditions, d.ConditionFlags([]string{"n"})) {
		t.Error("noun should not match a verb inflection")
	}
	if names := d.RuleNames(past.Rules); len(names) == 0 || names[0] == "" {
		t.Errorf("rule names = %v", names)
	}
}
