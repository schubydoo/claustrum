package supervisor

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
)

// The rule checks of a farewell and their order (measured on Linux, build
// 52d82f0a). The unit test covers the 30 s cut of graceMs and both sides of the cap.
func TestReadFarewellRules(t *testing.T) {
	big := base64.StdEncoding.EncodeToString(make([]byte, FarewellMaxBytes))
	over := base64.StdEncoding.EncodeToString(make([]byte, FarewellMaxBytes+1))
	for _, tc := range []struct {
		farewell, grace string // raw JSON, "" for absent
		rule            string
		ask             bool
		graceMs         int
	}{
		{`"aGkK"`, `2000`, "", true, 2000},
		{`"aGkK"`, `45000`, "", true, 30000},
		{`"` + big + `"`, `100`, "", true, 100},
		{`"` + over + `"`, `100`, "over the cap", false, 0},
		{`"!!!"`, `100`, "not base64", false, 0},
		{`"aGkK"`, ``, "a farewell with no graceMs", false, 0},
		{``, `100`, "graceMs with no farewell", false, 0},
		{`5`, `100`, "of the wrong type", false, 0},
		{`"!!!"`, ``, "a farewell with no graceMs", false, 0},
		{`5`, `"x"`, "of the wrong type", false, 0},
		{`"!!!"`, `"x"`, "of the wrong type", false, 0},
		{`"aGkK"`, `-5`, "a farewell with no graceMs", false, 0},
		{`""`, `0`, "", false, 0},
		{`null`, `100`, "graceMs with no farewell", false, 0},
		{`"aGkK"`, `null`, "a farewell with no graceMs", false, 0},
		{`"aGkK"`, `1.5`, "of the wrong type", false, 0},
		{``, ``, "", false, 0},
	} {
		ask, rule := ReadFarewell(json.RawMessage(tc.farewell), json.RawMessage(tc.grace))
		if rule != tc.rule || (ask != nil) != tc.ask {
			t.Errorf("readFarewell(%.20s, %s) = %v, %q, want ask %v, %q", tc.farewell, tc.grace, ask != nil, rule, tc.ask, tc.rule)
			continue
		}
		if ask != nil && ask.grace != time.Duration(tc.graceMs)*time.Millisecond {
			t.Errorf("readFarewell(%.20s, %s) grace = %v, want %d ms", tc.farewell, tc.grace, ask.grace, tc.graceMs)
		}
	}
}
