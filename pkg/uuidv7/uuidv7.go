package uuidv7

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"runtime"
	"sync"
	"time"
)

type uuidGen struct {
	mu     sync.Mutex
	lastMs int64
	seqHi  uint16
	seqLo  uint64
}

var gen = &uuidGen{}

func Generate() string {
	gen.mu.Lock()
	defer gen.mu.Unlock()

	now := time.Now().UnixMilli()

	if now > gen.lastMs {
		gen.lastMs = now
		gen.seedCounter()
	} else {
		gen.seqLo++
		if gen.seqLo > 0x3FFFFFFFFFFFFFFF {
			gen.seqLo = 0
			gen.seqHi++
			if gen.seqHi > 0x0FFF {
				for time.Now().UnixMilli() == now {
					runtime.Gosched()
				}
				gen.lastMs = time.Now().UnixMilli()
				gen.seedCounter()
			}
		}
		now = gen.lastMs
	}

	uuid := make([]byte, 16)
	binary.BigEndian.PutUint16(uuid[0:2], uint16((uint64(now)>>32)&0xFFFF))
	binary.BigEndian.PutUint32(uuid[2:6], uint32(uint64(now)&0xFFFFFFFF))
	uuid[6] = 0x70 | byte((gen.seqHi>>8)&0x0F)
	uuid[7] = byte(gen.seqHi & 0xFF)
	uuid[8] = 0x80 | byte((gen.seqLo>>56)&0x3F)
	uuid[9] = byte((gen.seqLo >> 48) & 0xFF)
	uuid[10] = byte((gen.seqLo >> 40) & 0xFF)
	uuid[11] = byte((gen.seqLo >> 32) & 0xFF)
	uuid[12] = byte((gen.seqLo >> 24) & 0xFF)
	uuid[13] = byte((gen.seqLo >> 16) & 0xFF)
	uuid[14] = byte((gen.seqLo >> 8) & 0xFF)
	uuid[15] = byte(gen.seqLo & 0xFF)

	return format(uuid)
}

// ExtractTime returns the timestamp embedded in a UUIDv7 string.
// It returns the zero time if the string is not a valid UUIDv7.
func ExtractTime(s string) time.Time {
	b, err := parse(s)
	if err != nil {
		return time.Time{}
	}
	ms := int64(binary.BigEndian.Uint16(b[0:2]))<<32 |
		int64(binary.BigEndian.Uint32(b[2:6]))
	return time.UnixMilli(ms).UTC()
}

// IsValid reports whether s is a well-formed UUIDv7 string.
func IsValid(s string) bool {
	b, err := parse(s)
	if err != nil {
		return false
	}
	return b[6]>>4 == 7 && b[8]>>6 == 2
}

func (g *uuidGen) seedCounter() {
	var b [10]byte
	_, _ = rand.Read(b[:])
	g.seqHi = binary.BigEndian.Uint16(b[0:2]) & 0x07FF
	g.seqLo = binary.BigEndian.Uint64(b[2:10]) & 0x1FFFFFFFFFFFFFFF
}

func format(uuid []byte) string {
	return hex.EncodeToString(uuid[0:4]) + "-" +
		hex.EncodeToString(uuid[4:6]) + "-" +
		hex.EncodeToString(uuid[6:8]) + "-" +
		hex.EncodeToString(uuid[8:10]) + "-" +
		hex.EncodeToString(uuid[10:16])
}

func parse(s string) ([]byte, error) {
	clean := ""
	for _, c := range s {
		if c != '-' {
			clean += string(c)
		}
	}
	if len(clean) != 32 {
		return nil, errInvalid
	}
	return hex.DecodeString(clean)
}

var errInvalid = &invalidErr{}

type invalidErr struct{}

func (e *invalidErr) Error() string { return "uuidv7: invalid UUID" }
