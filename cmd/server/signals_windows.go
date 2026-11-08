//go:build windows

package cmd

import "os"

// ShutdownSignals 在 Windows 上只有 os.Interrupt（Ctrl+C）可用，
// SIGTERM/SIGHUP 不会被系统投递，因此不注册。
var ShutdownSignals = []os.Signal{os.Interrupt}
