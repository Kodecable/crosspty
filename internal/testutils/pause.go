package testutils

import "os"

func Pause() {
	for {
		var b [16]byte
		n, _ := os.Stdin.Read(b[:])
		if n != 0 {
			break
		}
	}
}
