// Package leftsweep ends the processes that the children of this daemon started
// and left behind. Such a process carries both marks of a child of this daemon in
// its environment and still sees this daemon's run folder, or sees no run folder
// at all. The sweep runs on Linux only. On macOS and Windows Run ends nothing
// (claustrum's own, not measured).
//
// Measured on 13047a51 (Linux, row K-1 and its three variants of TASK-937, run
// m25c-ubuntu2-20261128T060259Z): a process with both marks ends by one
// pidfd_send_signal SIGKILL, also in a mount namespace where the run folder path
// is missing, or where another folder with no children entry stands at that path.
// A process with one mark, a process of another uid and a process whose other
// folder at that path holds a children entry stay alive.
//
// No path of the daemon calls Run yet. On 13047a51 the sweep ran only at the end
// by the unattended limit (rows K-1 and l_m25_limit2). After server.shutdown and
// after the orphan end the log shows no sweep line.
package leftsweep
