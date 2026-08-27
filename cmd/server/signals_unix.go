//go:build !windows

package cmd

import (
	"os"
	"syscall"
)

var ShutdownSignals = []os.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP}
