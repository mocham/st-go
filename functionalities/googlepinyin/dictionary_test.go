package googlepinyin

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"strings"
	"sync"
	"testing"
	"unicode/utf16"
)

func utf16File(text string) []byte {
	result := []byte{0xff, 0xfe}
	for _, unit := range utf16.Encode([]rune(text)) {
		var encoded [2]byte
		binary.LittleEndian.PutUint16(encoded[:], unit)
		result = append(result, encoded[:]...)
	}
	return result
}

func testDictionary(t *testing.T) []byte {
	t.Helper()
	data, err := testDictionaryData()
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func testDictionaryData() ([]byte, error) {
	raw := strings.Join([]string{
		"你好 150 0 ni hao",
		"女孩 140 0 nv hai",
		"您好 130 0 nin hao",
		"南海 120 0 nan hai",
		"农行 110 0 nong hang",
		"浓厚 100 0 nong hou",
		"你 1000 0 ni",
		"那 900 0 na",
		"西安 200 0 xi an",
		"低频 59 0 di pin",
		"先 500 0 xian",
		"西 400 0 xi",
		"安 300 0 an",
	}, "\n") + "\n"
	valid := "你好女孩您南海农行浓厚你那西安先低频"
	var encoded bytes.Buffer
	if err := BuildDictionary(bytes.NewReader(utf16File(raw)), bytes.NewReader(utf16File(valid)), &encoded); err != nil {
		return nil, err
	}
	return encoded.Bytes(), nil
}

func TestBuildLoadAndCandidateOrdering(t *testing.T) {
	decoder, err := Open(testDictionary(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, entries := range decoder.entries {
		for _, candidate := range entries {
			if candidate.text == "低频" {
				t.Fatal("low-frequency phrase was retained")
			}
		}
	}
	candidates, err := decoder.Lookup("nh", 20)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"你好", "女孩", "您好", "南海", "农行", "浓厚", "你", "那"}
	if strings.Join(candidates, ",") != strings.Join(want, ",") {
		t.Fatalf("nh candidates = %q", candidates)
	}
	candidates, err = decoder.Lookup("xian", 20)
	if err != nil || len(candidates) == 0 || candidates[0] != "先" {
		t.Fatalf("xian candidates = %q, %v", candidates, err)
	}
	candidates, err = decoder.Lookup("xi'an", 20)
	if err != nil || len(candidates) < 2 || candidates[0] != "西安" || candidates[1] != "西" {
		t.Fatalf("xi'an candidates = %q, %v", candidates, err)
	}
}

func TestDictionaryAndPromptBounds(t *testing.T) {
	valid := testDictionary(t)
	for _, data := range [][]byte{nil, []byte("not gzip"), valid[:len(valid)/2]} {
		if _, err := Open(data); err == nil {
			t.Fatalf("accepted malformed dictionary of %d bytes", len(data))
		}
	}
	var malformed bytes.Buffer
	zipper := gzip.NewWriter(&malformed)
	_, _ = zipper.Write([]byte(dictionaryHeader + "\t1\n坏\t1\tBAD\n"))
	_ = zipper.Close()
	if _, err := Open(malformed.Bytes()); err == nil {
		t.Fatal("accepted malformed spelling")
	}
	decoder, err := Open(valid)
	if err != nil {
		t.Fatal(err)
	}
	for _, prompt := range []string{"", "Ni", "ni hao", "'ni", "ni'", strings.Repeat("a", MaxPrompt+1)} {
		if _, err := decoder.Lookup(prompt, 5); err == nil {
			t.Errorf("accepted prompt %q", prompt)
		}
	}
	if _, err := decoder.Lookup("ni", 0); err == nil {
		t.Fatal("accepted zero candidate limit")
	}
}

func TestRepeatedBoundedPromptsDoNotPanic(t *testing.T) {
	decoder, err := Open(testDictionary(t))
	if err != nil {
		t.Fatal(err)
	}
	for length := 1; length <= MaxPrompt; length++ {
		prompt := strings.Repeat("a", length)
		if _, err := decoder.Lookup(prompt, MaxCandidates); err != nil {
			t.Fatal(err)
		}
	}
}

func TestConcurrentLookup(t *testing.T) {
	decoder, err := Open(testDictionary(t))
	if err != nil {
		t.Fatal(err)
	}
	prompts := []string{"nh", "nihao", "xian", "xi'an", strings.Repeat("a", MaxPrompt)}
	var workers sync.WaitGroup
	for worker := 0; worker < 16; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for repetition := 0; repetition < 20; repetition++ {
				for _, prompt := range prompts {
					if _, err := decoder.Lookup(prompt, MaxCandidates); err != nil {
						t.Errorf("%s: %v", prompt, err)
						return
					}
				}
			}
		}()
	}
	workers.Wait()
}

func FuzzOpenRejectsMalformedDictionariesWithoutPanicking(f *testing.F) {
	f.Add([]byte("not a dictionary"))
	dictionary, err := testDictionaryData()
	if err != nil {
		f.Fatal(err)
	}
	f.Add(dictionary)
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > MaxCompressedDictionary+1 {
			data = data[:MaxCompressedDictionary+1]
		}
		_, _ = Open(data)
	})
}

func FuzzLookupRejectsMalformedPromptsWithoutPanicking(f *testing.F) {
	dictionary, err := testDictionaryData()
	if err != nil {
		f.Fatal(err)
	}
	decoder, err := Open(dictionary)
	if err != nil {
		f.Fatal(err)
	}
	f.Add("nh", 20)
	f.Add(strings.Repeat("a", MaxPrompt), MaxCandidates)
	f.Fuzz(func(t *testing.T, prompt string, limit int) {
		if len(prompt) > MaxPrompt+1 {
			prompt = prompt[:MaxPrompt+1]
		}
		if limit < 0 {
			limit = -limit
		}
		limit %= MaxCandidates + 2
		_, _ = decoder.Lookup(prompt, limit)
	})
}
