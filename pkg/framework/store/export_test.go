package store

import "time"

func SetUUIDGenerate(fn func() string) (restore func()) {
	old := uuidGenerate
	uuidGenerate = fn
	return func() { uuidGenerate = old }
}

func SetUUIDExtractTime(fn func(string) time.Time) (restore func()) {
	old := uuidExtractTime
	uuidExtractTime = fn
	return func() { uuidExtractTime = old }
}
