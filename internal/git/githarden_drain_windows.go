package git

// failedCheckoutSkipsStdoutDrain is false on Windows: a failed read-tree call
// keeps the stdout drain (claustrum's own, not measured).
const failedCheckoutSkipsStdoutDrain = false
