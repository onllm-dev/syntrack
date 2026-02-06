//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"syscall"
)

func daemonSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

func defaultPIDDir() string {
	return filepath.Join(os.Getenv("HOME"), ".syntrack")
}
