//go:build windows

package main

// unreadableDestDirGoes turns on removeUnreadableEmptyDestDir. A test turns it
// on off Windows to hold the gates of that step. Production never reassigns it.
var unreadableDestDirGoes = true
