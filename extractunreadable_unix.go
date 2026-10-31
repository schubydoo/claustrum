//go:build unix

package main

// unreadableDestDirGoes is false on Linux and macOS. There a folder that the
// daemon cannot read goes to prepareOldDestDir, because the lstat through the
// parent handle still reads it (from the code). A test turns it on to hold the
// gates of removeUnreadableDestEntry. Production never reassigns it.
var unreadableDestDirGoes = false
