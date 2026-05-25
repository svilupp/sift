package ref

import (
	"strings"

	"github.com/cespare/xxhash/v2"
)

const tokenAlphabet = "abcdefghijklmnopqrstuvwxyz234567"

func normalizeLine(line string) string {
	return strings.TrimSpace(strings.TrimRight(line, "\r"))
}

func sanitizeTokenLength(length int) int {
	switch {
	case length < 2:
		return defaultTokenLength
	case length > 8:
		return 8
	default:
		return length
	}
}

func LineToken(line string, length int) string {
	length = sanitizeTokenLength(length)

	bits := uint(length * 5)
	hash := xxhash.Sum64String(normalizeLine(line))
	value := hash >> (64 - bits)

	out := make([]byte, length)
	for i := 0; i < length; i++ {
		shift := uint((length - i - 1) * 5)
		out[i] = tokenAlphabet[(value>>shift)&31]
	}
	return string(out)
}
