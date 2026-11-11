//go:build !libwebp

package frontend

import "time"

type animFrame struct {
	rgba     []byte
	duration time.Duration
}

type webPAnimDecoder struct {
	index int
}

func openWebPAnimDecoder([]byte) (*webPAnimDecoder, bool) {
	return nil, false
}

func (*webPAnimDecoder) close() {}

func (*webPAnimDecoder) frame(int) (int, int, []byte, bool) {
	return 0, 0, nil, false
}

func (*Terminal) decodeWebPAnimFrameSequential([]byte, int) (int, int, []byte, bool) {
	return 0, 0, nil, false
}

func decodeWebPAnim([]byte) (int, int, []animFrame, bool) {
	return 0, 0, nil, false
}

func decodeWebPAnimInfo([]byte) ([]int, int, int, bool) {
	return nil, 0, 0, false
}

func decodeWebPAnimFrame([]byte, int) (int, int, []byte, bool) {
	return 0, 0, nil, false
}

func webPAnimDimensions([]byte) (int, int, int, bool) {
	return 0, 0, 0, false
}
