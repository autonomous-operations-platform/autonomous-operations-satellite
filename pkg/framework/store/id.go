package store

import (
	"time"

	"github.com/autonomous-operations-platform/autonomous-operations-satellite/pkg/uuidv7"
)

var (
	//nolint:gochecknoglobals // globals are acceptable since this is a global framework
	uuidGenerate func() string = uuidv7.Generate
	//nolint:gochecknoglobals // globals are acceptable since this is a global framework
	uuidExtractTime func(string) time.Time = uuidv7.ExtractTime
)

// RecordID is a UUIDv7 string. It is strictly monotonic within a process and
// embeds the ingestion timestamp, which makes cursor pagination safe.
type RecordID string

func (id RecordID) Time() time.Time {
	if id == "" {
		return time.Time{}
	}
	return uuidExtractTime(string(id))
}

func (id RecordID) String() string { return string(id) }

// NewID generates a new monotonic UUIDv7 RecordID.
func NewID() RecordID {
	return RecordID(uuidGenerate())
}
