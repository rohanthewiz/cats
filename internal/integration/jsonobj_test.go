package integration

import "testing"

func TestOrderedJSONRoundTripPreservesKeyOrder(t *testing.T) {
	in := `{"zeta":1,"alpha":{"z":[1,2,{}],"y":null,"x":"a<b&c"},"beta":[],"gamma":true}`
	parsed, err := parseJSONDocument([]byte(in))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := string(marshalJSONPretty(parsed))
	want := `{
  "zeta": 1,
  "alpha": {
    "z": [
      1,
      2,
      {}
    ],
    "y": null,
    "x": "a<b&c"
  },
  "beta": [],
  "gamma": true
}`
	if got != want {
		t.Fatalf("round trip:\n%s\nwant:\n%s", got, want)
	}
}

func TestOrderedJSONNumberFidelity(t *testing.T) {
	in := `{"a":1.50,"b":1e10,"c":-0.25}`
	parsed, err := parseJSONDocument([]byte(in))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := string(marshalJSONPretty(parsed))
	want := "{\n  \"a\": 1.50,\n  \"b\": 1e10,\n  \"c\": -0.25\n}"
	if got != want {
		t.Fatalf("numbers not preserved verbatim:\n%s", got)
	}
}

func TestOrderedJSONSetDeleteSemantics(t *testing.T) {
	obj := newJSONObject()
	obj.Set("a", int64(1))
	obj.Set("b", int64(2))
	obj.Set("a", int64(3)) // replace keeps position
	if got := string(marshalJSONPretty(obj)); got != "{\n  \"a\": 3,\n  \"b\": 2\n}" {
		t.Fatalf("replace changed order: %s", got)
	}
	obj.Delete("a")
	obj.Set("a", int64(4)) // re-insert appends
	if got := string(marshalJSONPretty(obj)); got != "{\n  \"b\": 2,\n  \"a\": 4\n}" {
		t.Fatalf("delete/insert order wrong: %s", got)
	}
}

func TestOrderedJSONRejectsTrailingContent(t *testing.T) {
	if _, err := parseJSONDocument([]byte(`{"a":1} {"b":2}`)); err == nil {
		t.Fatal("expected error for trailing content")
	}
}

func TestShellSingleQuote(t *testing.T) {
	if got := shellSingleQuote("/plain/path.sh"); got != "'/plain/path.sh'" {
		t.Fatalf("plain: %s", got)
	}
	if got := shellSingleQuote("/it's here/h.sh"); got != `'/it'"'"'s here/h.sh'` {
		t.Fatalf("quoted: %s", got)
	}
}

func TestHookCommandShape(t *testing.T) {
	if got := hookCommand("/x/h.sh", "session", true); got != "bash '/x/h.sh' session" {
		t.Fatalf("with action: %s", got)
	}
	if got := hookCommand("/x/h.sh", "", false); got != "bash '/x/h.sh'" {
		t.Fatalf("without action: %s", got)
	}
}
