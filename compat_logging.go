package main

import "github.com/schubydoo/claustrum/internal/logging"

// The names of internal/logging under the names that the code of package main
// uses.

func logDebugf(format string, args ...any) { logging.Debugf(format, args...) }
func logInfof(format string, args ...any)  { logging.Infof(format, args...) }
func logWarnf(format string, args ...any)  { logging.Warnf(format, args...) }
func logErrorf(format string, args ...any) { logging.Errorf(format, args...) }
