package supervisor

import (
	"strings"
	"testing"
)

// cliSessionKey gates superseding: a non-empty key needs stream-json mode AND a
// valid session id (session-id wins; resume is the fallback unless --fork-session).
func TestCliSessionKey(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"session-id equals form", []string{"--input-format=stream-json", "--session-id=abc"}, "abc"},
		{"session-id space form", []string{"--input-format=stream-json", "--session-id", "abc"}, "abc"},
		{"output-format stream-json + resume", []string{"--output-format=stream-json", "--resume=r1"}, "r1"},
		{"resume space form", []string{"--output-format=stream-json", "--resume", "r2"}, "r2"},
		{"bare stream-json token", []string{"stream-json", "--session-id=xy"}, "xy"},
		{"resume suppressed by fork-session", []string{"--input-format=stream-json", "--resume=r1", "--fork-session"}, ""},
		{"session-id wins over fork-session", []string{"--input-format=stream-json", "--session-id=s1", "--fork-session"}, "s1"},
		{"session-id beats resume", []string{"--input-format=stream-json", "--resume=r1", "--session-id=s1"}, "s1"},
		{"not stream-json", []string{"--session-id=abc"}, ""},
		{"stream-json no id", []string{"--input-format=stream-json"}, ""},
		{"session-id is last arg, no value", []string{"--input-format=stream-json", "--session-id"}, ""},
		{"next arg is a flag, rejected", []string{"--input-format=stream-json", "--session-id", "--resume"}, ""},
		{"value of resume is not read as a flag", []string{"--input-format=stream-json", "--resume", "--session-id=abc"}, ""},
		{"value of session-id is not the stream-json mark", []string{"--session-id", "stream-json"}, ""},
		{"value of resume is not the fork mark", []string{"--input-format=stream-json", "--resume", "--fork-session", "--resume=r1"}, "r1"},
		{"token too long", []string{"--input-format=stream-json", "--session-id=" + strings.Repeat("a", 129)}, ""},
		{"token bad char", []string{"--input-format=stream-json", "--session-id=has space"}, ""},
		{"token with allowed punctuation", []string{"--input-format=stream-json", "--session-id=a-b_c.d:1"}, "a-b_c.d:1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cliSessionKey(tc.args); got != tc.want {
				t.Errorf("cliSessionKey(%q) = %q, want %q", tc.args, got, tc.want)
			}
		})
	}
}
