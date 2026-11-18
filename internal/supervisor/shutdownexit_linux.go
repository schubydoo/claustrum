package supervisor

// quietEveryShutdownExit is false on Linux: a child that the shutdown sweep ended
// logs its exited line, as on c9125eb8 and 4c111e86 (measured on Linux).
const quietEveryShutdownExit = false
