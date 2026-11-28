// Package leftsweep ends the processes that the children of this daemon started
// and left behind. Such a process carries both marks of a child of this daemon in
// its environment, and its root passes the run-folder test that the next paragraph
// gives. The sweep acts on Linux only. On macOS and Windows Run ends nothing
// (claustrum's own, not measured).
//
// Measured on 13047a51 (Linux): a process with both marks ends by one
// pidfd_send_signal SIGKILL, also in a mount namespace where the run folder path
// is missing, where another folder with no children entry stands at that path, or
// where an absolute link there leads to the run folder. A link to a folder with a
// children entry keeps it alive.
// A process with one mark, a process of another uid and a process whose other
// folder at that path holds a children entry stay alive.
//
// Only the end by the unattended limit calls Run (sweepAfterLimit of the daemon).
// On 13047a51 the sweep ran there (measured on Linux). After
// server.shutdown and after the orphan end the log shows no sweep line.
package leftsweep
