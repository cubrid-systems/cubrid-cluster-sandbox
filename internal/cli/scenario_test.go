package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// decodeScenario is what cmdScenarioRun does before it stands anything up.
func decodeScenario(t *testing.T, src string) (Scenario, error) {
	t.Helper()
	var s Scenario
	dec := json.NewDecoder(bytes.NewReader([]byte(src)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return s, err
	}
	return s, s.validate()
}

// A scenario is refused for what it says, before a cluster is stood up for it.
//
// Every case here used to be paid for at runtime, and three of them were not
// paid for at all: `contain` for `contains`, `withn` for `within` and a
// `${score}` no matrix key fills all ran, asserted nothing, and printed ok.
func TestAScenarioIsRefusedForWhatItSays(t *testing.T) {
	cases := []struct {
		name, src, want string
	}{
		{"a misspelt assertion is an assertion that never runs",
			`{"name":"x","steps":[{"run":["repl","check"],"contain":["a"]}]}`,
			`unknown field "contain"`},
		{"a misspelt wait is a step that waits for nothing",
			`{"name":"x","steps":[{"awaits":{"masters":1}}]}`,
			`unknown field "awaits"`},
		{"a misspelt cluster field",
			`{"name":"x","cluster":{"presset":"ha"},"steps":[{"run":["ha","status"]}]}`,
			`unknown field "presset"`},
		{"a verb that does not exist",
			`{"name":"x","steps":[{"run":["repl","chek"]}]}`,
			`repl has no verb "chek"`},
		{"a noun that does not exist",
			`{"name":"x","steps":[{"run":["rpl","check"]}]}`,
			`unknown noun "rpl"`},
		{"an argv that is not a command",
			`{"name":"x","steps":[{"run":["repl"]}]}`,
			"needs a noun and a verb"},
		{"a within that is not a duration, which used to fall back to 60s in silence",
			`{"name":"x","steps":[{"await":{"masters":1},"within":"1min"}]}`,
			`within "1min" is not a duration`},
		{"a role_change_within that is not a duration",
			`{"name":"x","steps":[{"run":["ha","status"],"role_change_within":"10 s"}]}`,
			"is not a duration"},
		{"a master_is that is not a created role",
			`{"name":"x","steps":[{"await":{"master_is":"salve"},"within":"10s"}]}`,
			`master_is "salve" is not a created role`},
		{"a measure nothing emits, which used to be a column of nulls",
			`{"name":"x","measure":["canary.second"],"steps":[{"run":["ha","status"]}]}`,
			`measure "canary.second" is not something this tool emits`},
		{"a binding nothing fills, which used to reach the argv as literal text",
			`{"name":"x","matrix":{"interval":[1,2]},"steps":[{"run":["node","exec","c","--","echo","${intervals}"]}]}`,
			"${intervals}, which nothing fills"},
		{"a binding nothing fills, in a cluster parameter",
			`{"name":"x","steps":[{"run":["ha","status"]}],"cluster":{"set_hidden":["k=${nope}"]}}`,
			"${nope}, which nothing fills"},
		{"a step that does nothing and asserts nothing",
			`{"name":"x","steps":[{"note":"nothing"}]}`,
			"neither run nor await"},
	}
	for _, c := range cases {
		_, err := decodeScenario(t, c.src)
		if err == nil {
			t.Errorf("%s: accepted", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %q does not contain %q", c.name, err, c.want)
		}
	}
}

// The two bindings the runner supplies itself are not matrix keys and must not
// be refused as unfilled. `${cluster}` is how a step tells somebody's program
// which database to talk to, and switchover-threshold.json has used it since it
// was written.
func TestTheRunnersOwnBindingsAreBound(t *testing.T) {
	src := `{"name":"x","matrix":{"i":[1]},"steps":[{"run":["node","exec","c","--","sh","/tools/x.sh","${cluster}","${cluster}-n1","${repeat}","${i}"]}]}`
	if _, err := decodeScenario(t, src); err != nil {
		t.Errorf("refused a scenario using the runner's own bindings: %v", err)
	}
}

// The scenarios this repository ships have to survive its own validator. This
// is the test that would have caught a rule stricter than the format.
func TestShippedScenariosValidate(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "scenarios", "*.json"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no scenarios to check: %v", err)
	}
	for _, p := range paths {
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			t.Fatalf("%s: %v", p, rerr)
		}
		if _, verr := decodeScenario(t, string(b)); verr != nil {
			t.Errorf("%s: %v", filepath.Base(p), verr)
		}
	}
}

// knownVerb made "a step is an argv this tool already accepts" true of the verb
// and left it false of everything after it, so a misspelt flag was accepted, a
// cluster was built for it, and the run died thirty seconds later on something
// visible before anything started.
func TestAStepsFlagsAreCheckedBeforeAnythingIsBuilt(t *testing.T) {
	bad := []struct{ src, want string }{
		{`{"name":"x","steps":[{"run":["repl","check","--waitt","30s"]}]}`,
			`repl check has no flag --waitt`},
		{`{"name":"x","steps":[{"run":["fault","lag","slave","--stage=aply","--mechansim","suspend"]}]}`,
			`fault lag has no flag --mechansim`},
		{`{"name":"x","steps":[{"run":["cluster","status","--jsonn"]}]}`,
			`cluster status has no flag --jsonn`},
	}
	for _, c := range bad {
		_, err := decodeScenario(t, c.src)
		if err == nil {
			t.Errorf("accepted: %s", c.src)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q does not contain %q", err, c.want)
		}
	}

	good := []string{
		// Real flags, in every shape: separate, =-joined, bool, global.
		`{"name":"x","steps":[{"run":["repl","check","--wait","30s","--json"]}]}`,
		`{"name":"x","steps":[{"run":["fault","lag","slave","--stage=apply","--mechanism","suspend"]}]}`,
		// A value that is filled per matrix point cannot parse here, and must
		// not be mistaken for a flag.
		`{"name":"x","matrix":{"w":["30s"]},"steps":[{"run":["repl","check","--wait","${w}"]}]}`,
		// A negative value is a value, not a flag, because the flag before it
		// is not a bool.
		`{"name":"x","steps":[{"run":["fault","failcount","--rows","-1"]}]}`,
		// Everything after a bare -- belongs to another program.
		`{"name":"x","steps":[{"run":["node","exec","client","--","sh","/tools/x.sh","--anything","-z"]}]}`,
	}
	for _, src := range good {
		if _, err := decodeScenario(t, src); err != nil {
			t.Errorf("refused a valid step: %v\n%s", err, src)
		}
	}
}

// The schema in `scenario run --help` is prose beside a struct, which is exactly
// how documentation drifts. Every field the format accepts has to appear in it.
func TestTheDocumentedSchemaNamesEveryFieldTheFormatAccepts(t *testing.T) {
	for _, typ := range []reflect.Type{
		reflect.TypeOf(Scenario{}), reflect.TypeOf(ScenarioCluster{}),
		reflect.TypeOf(Step{}), reflect.TypeOf(Condition{}),
	} {
		for i := 0; i < typ.NumField(); i++ {
			tag := typ.Field(i).Tag.Get("json")
			name, _, _ := strings.Cut(tag, ",")
			if name == "" || name == "-" {
				continue
			}
			if !strings.Contains(scenarioSchemaHelp, name) {
				t.Errorf("%s.%s is accepted and `scenario run --help` does not mention %q",
					typ.Name(), typ.Field(i).Name, name)
			}
		}
	}
	// And the closed lists, which are the ones a caller cannot guess.
	for _, m := range measurable {
		if !strings.Contains(scenarioSchemaHelp, m) {
			t.Errorf("measure %q is accepted and undocumented", m)
		}
	}
}
