package git

import (
	"encoding/json"
	"testing"
)

// FuzzBindGitParams is FuzzBindParams for gitParams: bindParams either decodes
// the input or answers Invalid params. It never panics and never answers
// another error code.
func FuzzBindGitParams(f *testing.F) {
	for _, seed := range []string{
		`{"path":"/x","maxBytes":5}`, `{"path":123}`, `{"maxBytes":"4"}`,
		`{"id":"a","command":"c","args":["y"],"env":{"K":"V"}}`,
		`{"archivePath":"a","destDir":"/d"}`, `{"id":"a","fromSeq":2}`,
		`[]`, `"x"`, `null`, `true`, `123`, ``, `{`, `{"x":[1,2,{}]}`,
	} {
		f.Add([]byte(seed))
	}

	f.Fuzz(func(t *testing.T, raw []byte) {
		req := &request{ID: json.RawMessage(`1`), Params: json.RawMessage(raw)}
		if bad := bindParams(req, &gitParams{}); bad != nil {
			if bad.Error == nil || bad.Error.Code != codeInvalidParam {
				t.Errorf("bindParams(%q) returned a non-Invalid-params response: %+v", raw, bad)
			}
		}
	})
}
