//go:build windows

package logger

import (
	"fmt"
	"syscall"
	"unsafe"
)

// enableVirtualTerminalProcessing включает обработку ANSI-кодов в консоли Windows.
const enableVirtualTerminalProcessing = 0x0004

var (
	kernel32           = syscall.NewLazyDLL("kernel32.dll")
	procGetConsoleMode = kernel32.NewProc("GetConsoleMode")
	procSetConsoleMode = kernel32.NewProc("SetConsoleMode")
)

func init() {
	for _, h := range []syscall.Handle{syscall.Stdout, syscall.Stderr} {
		var mode uint32
		if r, _, _ := procGetConsoleMode.Call(uintptr(h), uintptr(unsafe.Pointer(&mode))); r == 0 {
			continue
		}
		_, _, err := procSetConsoleMode.Call(uintptr(h), uintptr(mode|enableVirtualTerminalProcessing))
		if err != nil {
			fmt.Printf("internal/logger/color_windows.go error: %v", err)
		}
	}
}
