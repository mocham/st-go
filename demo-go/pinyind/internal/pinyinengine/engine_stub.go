//go:build !googlepinyin || !cgo || !linux

package pinyinengine

import (
	"fmt"
	"st-go/tui/pinyin"
)

type Engine struct{}

func Open() (*Engine, error) {
	return nil, fmt.Errorf("build the isolated helper with make govim-pinyind")
}
func (*Engine) Close() {}
func (*Engine) Lookup(pinyin.Request) (pinyin.Response, error) {
	return pinyin.Response{}, fmt.Errorf("Google Pinyin not built")
}
