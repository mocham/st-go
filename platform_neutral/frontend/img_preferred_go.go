//go:build !fastimage

package frontend

func decodePreferredImage([]byte, string, int, int, int) ([]byte, bool) {
	return nil, false
}
