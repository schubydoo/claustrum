package supervisor

import (
	"slices"
	"testing"
)

// TestAdoptedBaseDropsUnkeptName: a name of keptOnlyNames leaves the base of a
// child unless kept names it, and the adopt line still counts it. Windows has
// HOME in the list (c9125eb8, measured on Windows). The test sets the list, so it
// runs on every system.
func TestAdoptedBaseDropsUnkeptName(t *testing.T) {
	old := keptOnlyNames
	keptOnlyNames = []string{"HOME"}
	t.Cleanup(func() { keptOnlyNames = old })
	m := newTestProcManager(t)
	c, _ := pipeConn(t)

	every, _, _ := m.TakeAdoptedEnv(c, []string{"A=1", "HOME=/h/b", "GIT_X=1"}, nil, true)
	base, ok := m.adoptedBase(c)
	if !ok || !slices.Contains(base, "A=1") || !slices.Contains(base, "GIT_X=1") {
		t.Fatalf("the base after the adopt = %v (ok %v), want A=1 and GIT_X=1 in it", base, ok)
	}
	if slices.Contains(base, "HOME=/h/b") {
		t.Errorf("the base after an adopt with no kept HOME = %v, want no HOME=/h/b", base)
	}
	m.adopted.mu.Lock()
	n := countEnvNames(m.adopted.set)
	m.adopted.mu.Unlock()
	if every != n || !slices.Contains(m.adopted.set, "HOME=/h/b") {
		t.Errorf("the count = %d, the set %v: want HOME=/h/b counted (%d)", every, m.adopted.set, n)
	}

	m.TakeAdoptedEnv(c, []string{"HOME=/sent"}, []string{"HOME"}, true)
	if base, _ := m.adoptedBase(c); !slices.Contains(base, "HOME=/sent") {
		t.Errorf("the base after an adopt with kept HOME = %v, want HOME=/sent in it", base)
	}

	// A kept HOME with no sent HOME takes the daemon value (claustrum's own, not
	// measured).
	t.Setenv("HOME", "/daemon/home")
	m.TakeAdoptedEnv(c, []string{"A=1"}, []string{"HOME"}, true)
	if base, _ := m.adoptedBase(c); !slices.Contains(base, "HOME=/daemon/home") {
		t.Errorf("the base after a kept HOME with no sent HOME = %v, want HOME=/daemon/home in it", base)
	}
}
