package conn

import "testing"

func TestStampEndMeasuredShape(t *testing.T) {
	plain := `{"jsonrpc":"2.0","id":1,"result":{"pong":true}}`
	in := []byte(plain + "\n")
	keep := string(in)
	got := string(StampEnd(in, 0))
	want := `{"jsonrpc":"2.0","id":1,"result":{"pong":true},"end":{"at":0,"len":46}}` + "\n"
	if got != want {
		t.Errorf("StampEnd = %q, want %q", got, want)
	}
	if string(in) != keep {
		t.Errorf("StampEnd changed its input: %q", in)
	}
	stream := `{"type":"stream","processId":"f1","stream":"stdout","seq":1,"data":"Z290OmhpCg=="}` + "\n"
	got = string(StampEnd([]byte(stream), 174))
	want = `{"type":"stream","processId":"f1","stream":"stdout","seq":1,"data":"Z290OmhpCg==","end":{"at":174,"len":81}}` + "\n"
	if got != want {
		t.Errorf("StampEnd stream = %q, want %q", got, want)
	}
}
