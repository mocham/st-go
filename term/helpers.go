package term

import "log"

func logf(format string, args ...interface{}) {
	log.Printf(format, args...)
}

var base64Digits = [256]int{}

func init() {
	for i := range base64Digits {
		base64Digits[i] = -1
	}
	set := func(idx byte, v int) { base64Digits[idx] = v }
	set(43, 62) // '+'
	set(47, 63) // '/'
	for i := 0; i < 26; i++ {
		set('A'+byte(i), i)
		set('a'+byte(i), 26+i)
	}
	for i := 0; i < 10; i++ {
		set('0'+byte(i), 52+i)
	}
}

func base64decGetc(src *string) byte {
	for len(*src) > 0 && (*src)[0] < 0x20 {
		*src = (*src)[1:]
	}
	if len(*src) > 0 {
		c := (*src)[0]
		*src = (*src)[1:]
		return c
	}
	return '='
}

func base64dec(src string) string {
	inLen := len(src)
	if inLen%4 != 0 {
		inLen += 4 - inLen%4
	}
	result := make([]byte, 0, inLen/4*3+1)
	for len(src) > 0 {
		a := base64Digits[base64decGetc(&src)]
		b := base64Digits[base64decGetc(&src)]
		c := base64Digits[base64decGetc(&src)]
		d := base64Digits[base64decGetc(&src)]
		if a == -1 || b == -1 {
			break
		}
		result = append(result, byte(a<<2|((b&0x30)>>4)))
		if c == -1 {
			break
		}
		result = append(result, byte(((b&0x0f)<<4)|((c&0x3c)>>2)))
		if d == -1 {
			break
		}
		result = append(result, byte(((c&0x03)<<6)|d))
	}
	return string(result)
}
