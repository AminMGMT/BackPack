//go:build !linux

package web

func socketCount() (int, error) {
	return portableSocketCount()
}
