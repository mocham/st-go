//go:build !webpcache

package main

const staticWebPFrame = -1

type webPCache struct{}

func openWebPCache(string) (*webPCache, error) {
	return nil, nil
}

func (*webPCache) close() {}

func (*webPCache) frame([]byte, int) (int, int, []byte, bool) {
	return 0, 0, nil, false
}

func (*webPCache) putFrame([]byte, int, int, int, []byte) bool {
	return false
}

func (*webPCache) putFrameAsync([]byte, int, int, int, []byte) {}
