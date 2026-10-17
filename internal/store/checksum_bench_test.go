package store

import (
	"crypto/sha256"
	"fmt"
	"testing"
)

// Развилка на горячем пути сверки: строка "uuid:hlc:device\n" для хэша.
// Кандидаты: fmt.Sprintf против append в переиспользуемый буфер.
// Результаты — docs/perf/checksum.md.

var benchRows = func() [][3]string {
	rows := make([][3]string, 10000)
	for i := range rows {
		rows[i] = [3]string{
			"01J8ZC9E2QWERTYUIOPASDFGHJ",
			"018f45e2a1b90003",
			"01J8ZDEVICEAAAAAAAAAAAAAAA",
		}
	}
	return rows
}()

func BenchmarkChecksumLineSprintf(b *testing.B) {
	for b.Loop() {
		h := sha256.New()
		for _, r := range benchRows {
			fmt.Fprintf(h, "%s:%s:%s\n", r[0], r[1], r[2])
		}
		h.Sum(nil)
	}
}

func BenchmarkChecksumLineAppend(b *testing.B) {
	for b.Loop() {
		h := sha256.New()
		buf := make([]byte, 0, 128)
		for _, r := range benchRows {
			buf = ChecksumLine(buf[:0], r[0], r[1], r[2])
			h.Write(buf)
		}
		h.Sum(nil)
	}
}
