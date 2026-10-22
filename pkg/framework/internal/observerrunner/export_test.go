package observerrunner

import (
	"log/slog"

	"github.com/autonomous-operations-platform/autonomous-operations-satellite/pkg/framework/store"
)

func NewStoreWrapper(inner store.Store, observerName string) store.ObserverStore {
	return &storeWrapper{inner: inner, observerName: observerName, logger: slog.Default()}
}

func NewStoreWrapperWithIDGenerator(inner store.Store, observerName string, idGen func() store.RecordID) store.ObserverStore {
	return &storeWrapper{inner: inner, observerName: observerName, idGen: idGen, logger: slog.Default()}
}
