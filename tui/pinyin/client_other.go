//go:build !linux

package pinyin

import (
	"context"
	"fmt"
)

func HelperPath() (string, error) {
	return "", fmt.Errorf("the isolated Pinyin helper currently requires Linux")
}
func Start(context.Context, string) (Service, error) {
	return nil, fmt.Errorf("the isolated Pinyin helper currently requires Linux")
}

func NewProcessWorker(string) (*Worker, error) {
	return nil, fmt.Errorf("the isolated Pinyin helper currently requires Linux")
}
