// Package procinfo holds the process facts that the run-dir lock, the host cleaner and
// the child records share: the group signal, the /proc mount, the node identity and the
// signal hold of one process. Windows builds none of them.
package procinfo
