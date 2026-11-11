package googlepinyin

import (
	_ "embed"
	"sync"
)

//go:embed assets/dict_pinyin.txt.gz
var defaultDictionary []byte

//go:embed assets/LICENSE
var corpusLicense string

//go:embed assets/NOTICE
var corpusNotice string

var defaultState struct {
	sync.Once
	decoder *Decoder
	err     error
}

// OpenDefault returns the concurrency-safe decoder backed by the checked-in
// portable corpus. The corpus is validated and expanded once per process.
func OpenDefault() (*Decoder, error) {
	defaultState.Do(func() {
		defaultState.decoder, defaultState.err = Open(defaultDictionary)
	})
	return defaultState.decoder, defaultState.err
}

// CorpusLicense returns the attribution text for the checked-in corpus.
func CorpusLicense() string { return corpusNotice + "\n" + corpusLicense }
