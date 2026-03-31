package domain

import "testing"

func TestParseAction(t *testing.T) {
	action, err := ParseAction("pause")
	if err != nil {
		t.Fatalf("ParseAction returned error: %v", err)
	}
	if !action.PausedValue() {
		t.Fatal("expected pause to map to paused=true")
	}

	action, err = ParseAction("resume")
	if err != nil {
		t.Fatalf("ParseAction returned error: %v", err)
	}
	if action.PausedValue() {
		t.Fatal("expected resume to map to paused=false")
	}

	if _, err := ParseAction("stop"); err == nil {
		t.Fatal("expected invalid action to fail")
	}
}

func TestParseTargetKindAndValidateRule(t *testing.T) {
	if _, err := ParseTargetKind("device"); err != nil {
		t.Fatalf("ParseTargetKind returned error: %v", err)
	}
	if _, err := ParseTargetKind("wat"); err == nil {
		t.Fatal("expected invalid target kind to fail")
	}

	valid := Rule{Name: "Night", Schedule: "0 0 * * *", Action: ActionPause, TargetKind: TargetGlobal}
	if err := valid.ValidateBasic(); err != nil {
		t.Fatalf("ValidateBasic returned error: %v", err)
	}

	cases := []Rule{
		{Schedule: "0 0 * * *", Action: ActionPause, TargetKind: TargetGlobal},
		{Name: "Night", Action: ActionPause, TargetKind: TargetGlobal},
		{Name: "Night", Schedule: "0 0 * * *", Action: "nope", TargetKind: TargetGlobal},
		{Name: "Night", Schedule: "0 0 * * *", Action: ActionPause, TargetKind: "nope"},
		{Name: "Night", Schedule: "0 0 * * *", Action: ActionPause, TargetKind: TargetDevice},
	}
	for _, rule := range cases {
		if err := rule.ValidateBasic(); err == nil {
			t.Fatalf("expected validation to fail for %+v", rule)
		}
	}
}
