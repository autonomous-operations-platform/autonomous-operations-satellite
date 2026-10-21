package store

import (
	"time"

	"github.tools.sap/autonomous-operations/aos-framework/pkg/uuidv7"
)

var (
	uuidGenerate    func() string          = uuidv7.Generate
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
