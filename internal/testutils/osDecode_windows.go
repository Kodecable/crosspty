//go:build windows

package testutils

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func OsDecode(b []byte) string {
	if len(b) == 0 {
		return ""
	}

	cp, err := windows.GetConsoleCP()
	if err != nil {
		panic(err)
	}

	n, err := windows.MultiByteToWideChar(cp, 0, &(b[0]), int32(len(b)), (*uint16)(unsafe.Pointer(nil)), 0)
	if n == 0 {
		panic(err)
	}

	utf16Buf := make([]uint16, n)
	windows.MultiByteToWideChar(cp, 0, &(b[0]), int32(len(b)), &utf16Buf[0], n)
	return syscall.UTF16ToString(utf16Buf)
}
