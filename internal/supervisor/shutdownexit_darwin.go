package supervisor

// quietEveryShutdownExit is true on macOS: a child that the shutdown sweep ended
// logs no exited line. 4c111e86 logs it for almost no such child (measured on macOS).
const quietEveryShutdownExit = true
