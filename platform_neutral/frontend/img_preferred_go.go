//go:build !fastimage || backend_web || !cgo || purego

package frontend

func decodePreferredImage([]byte, string, int, int, int) ([]byte, bool) {
	return nil, false
}
