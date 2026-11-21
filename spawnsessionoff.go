package main

// sessionOffTyped is true: a process.spawn with a sessionFoldersOff of another JSON
// type than a bool or null gets Invalid params (sessionOffTypeRefused). 4c111e86
// answers 1 so on Linux, macOS and Windows, and "x", 0 and {} so on Linux and macOS.
// A gitRead spawn gets it there for 1 and "x". On Linux and macOS it starts no
// child (measured). The tests set it on every system.
var sessionOffTyped = true
