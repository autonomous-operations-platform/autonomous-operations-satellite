package collectorstorewrapper

import "github.com/autonomous-operations/aos-framework/pkg/framework/store"

func NewWithIDGenerator(s store.Store, idGen func() store.RecordID) *StoreWrapper {
	w := New(s, nil)
	w.idGen = idGen
	return w
}
