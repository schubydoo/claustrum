package supervisor

// SignalFailed is the error of a signal that the system refused to a live child,
// such as EPERM for a child that runs as another user. Its text is the text of
// that error. Name is the name of the signal as the log prints it, such as
// "terminated". Only the ProcGroup of Linux and macOS makes one.
type SignalFailed struct {
	Name string
	Err  error
}

func (e *SignalFailed) Error() string { return e.Err.Error() }

func (e *SignalFailed) Unwrap() error { return e.Err }
