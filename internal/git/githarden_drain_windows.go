package git

// failedCheckoutSkipsStdoutDrain is false on Windows: a failed read-tree call
// keeps the stdout drain (claustrum's own, not measured).
const failedCheckoutSkipsStdoutDrain = false

// stoppedCheckoutSkipsStdoutDrain reports that a read-tree call that the
// deadline stopped gets no stdout drain (runCheckoutPipes). 87375cc7 answers
// at the stop while a process holds only the stdout of git, and it waits for
// a process that holds stderr (measured on Windows).
const stoppedCheckoutSkipsStdoutDrain = true
