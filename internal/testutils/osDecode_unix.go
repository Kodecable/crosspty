//go:build unix

package testutils

func OsDecode(b []byte) string {
	return string(b)
}
