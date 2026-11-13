package files

import "testing"

// TestFindDarwinHomeRoot: on macOS a root equal to the HOME of the daemon gets
// the cut "home" and no walk (4c111e86, measured on macOS).
func TestFindDarwinHomeRoot(t *testing.T) {
	home := resolveTestRoot(t, t.TempDir())
	findTree(t, home, "t.txt")
	t.Setenv("HOME", home)
	res := findResult(t, map[string]any{"roots": []string{home}, "suffix": "t.txt"})
	if res.Cut != "home" || len(res.Paths) != 0 {
		t.Errorf("root HOME: %+v, want the cut home and no path", res)
	}
}
