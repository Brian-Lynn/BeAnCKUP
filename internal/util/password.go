package util

import (
	"crypto/rand"
	"math/big"
)

const (
	upperLetters = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	lowerLetters = "abcdefghijklmnopqrstuvwxyz"
	digits       = "0123456789"
	symbols      = "~!@#$%^&*()-_+={}[]|:;<>,.?"
)

// GeneratePassword 生成一个指定长度和复杂度的随机密码
func GeneratePassword(length int, includeUpper, includeLower, includeSymbols bool) string {
	var charSet string
	if includeUpper {
		charSet += upperLetters
	}
	if includeLower {
		charSet += lowerLetters
	}
	if !includeUpper && !includeLower {
		charSet += lowerLetters // 至少包含小写字母
	}
	charSet += digits // 总是包含数字
	if includeSymbols {
		charSet += symbols
	}

	password := make([]byte, length)
	for i := 0; i < length; i++ {
		num, err := rand.Int(rand.Reader, big.NewInt(int64(len(charSet))))
		if err != nil {
			// Fallback to a simple character in case of error
			password[i] = 'X'
			continue
		}
		password[i] = charSet[num.Int64()]
	}
	return string(password)
}
