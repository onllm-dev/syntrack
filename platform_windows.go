//go:build windows

package main

import (
	"os"
	"path/filepath"
	"syscall"
)

func daemonSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
}

func defaultPIDDir() string {
	if dir := os.Getenv("LOCALAPPDATA"); dir != "" {
		return filepath.Join(dir, "syntrack")
	}
	return filepath.Join(os.Getenv("USERPROFILE"), ".syntrack")
}
