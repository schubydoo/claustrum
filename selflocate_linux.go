package main

// selfLocateFails tells the -serve launcher to stop when it cannot locate its
// own binary. On Linux 5fd08069 prints "claude-ssh: locate self: readlink
// /proc/self/exe: no such file or directory" and exits 1. It makes nothing
// first (round tier3-02, Linux VM). It is a var, so a test can run the arm
// of macOS and Windows.
var selfLocateFails = true
