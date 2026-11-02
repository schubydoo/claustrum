package files

import (
	"encoding/json"
	"testing"

	"github.com/schubydoo/claustrum/internal/rpc"
)

// FuzzBindParams fuzzes the param-decode layer (rpc.Bind) across the param
// structs of this package. The only acceptable outcomes are nil (decoded) or an
// Invalid-params response, never a panic and never any other error code.
func FuzzBindParams(f *testing.F) {
	for _, seed := range []string{
		`{"path":"/x","maxBytes":5}`, `{"path":123}`, `{"maxBytes":"4"}`,
		`{"id":"a","command":"c","args":["y"],"env":{"K":"V"}}`,
		`{"archivePath":"a","destDir":"/d"}`, `{"id":"a","fromSeq":2}`,
		`[]`, `"x"`, `null`, `true`, `123`, ``, `{`, `{"x":[1,2,{}]}`,
	} {
		f.Add([]byte(seed))
	}

	makers := []func() rpc.PathExpander{
		func() rpc.PathExpander { return &pathParams{} },
		func() rpc.PathExpander { return &fileReadParams{} },
		func() rpc.PathExpander { return &extractTarParams{} },
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		req := &rpc.Request{ID: json.RawMessage(`1`), Params: json.RawMessage(raw)}
		for _, mk := range makers {
			if bad := rpc.Bind(req, mk()); bad != nil {
				if bad.Error == nil || bad.Error.Code != rpc.CodeInvalidParam {
					t.Errorf("rpc.Bind(%q) returned a non-Invalid-params response: %+v", raw, bad)
				}
			}
		}
	})
}
