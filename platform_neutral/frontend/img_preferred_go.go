//go:build !fastimage || backend_web

package frontend

func decodePreferredImage([]byte, string, int, int, int) ([]byte, bool) {
	return nil, false
}
