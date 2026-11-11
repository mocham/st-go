//go:build googlepinyin && pinyin_external

package pinyinengine

import (
	"fmt"
	"io"
	"os"

	"st-go/googlepinyin"
)

func dictionaryBytes() ([]byte, error) {
	path := os.Getenv("GOVIM_PINYIN_DICTIONARY")
	if path == "" {
		return nil, fmt.Errorf("unbundled helper requires GOVIM_PINYIN_DICTIONARY")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > googlepinyin.MaxCompressedDictionary {
		return nil, fmt.Errorf("invalid external Pinyin dictionary")
	}
	data, err := io.ReadAll(io.LimitReader(f, googlepinyin.MaxCompressedDictionary+1))
	if err != nil {
		return nil, err
	}
	if len(data) > googlepinyin.MaxCompressedDictionary {
		return nil, fmt.Errorf("external Pinyin dictionary too large")
	}
	return data, nil
}
