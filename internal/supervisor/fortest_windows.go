//go:build windows

package supervisor

import "golang.org/x/sys/windows"

// JobForTest returns the job handle of the group, 0 with no job. Only tests call it.
func (g *ProcGroup) JobForTest() windows.Handle {
	return g.job
}
