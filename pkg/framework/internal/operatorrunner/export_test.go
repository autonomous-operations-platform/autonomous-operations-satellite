package operatorrunner

import (
	"log/slog"

	"github.tools.sap/autonomous-operations/aos-framework/pkg/framework/operator"
	"github.tools.sap/autonomous-operations/aos-framework/pkg/framework/store"
)

func NewStoreWrapper(inner store.Store) store.OperatorStore {
	return &storeWrapper{inner: inner, logger: slog.Default()}
}

func NewStoreWrapperWithIDGenerator(inner store.Store, idGen func() store.RecordID) store.OperatorStore {
	return &storeWrapper{inner: inner, idGen: idGen, logger: slog.Default()}
}

func NewWithIDGenerator(s store.Store, op operator.Operator, idGen func() store.RecordID) *Runner {
	r := New(s, op)
	r.idGen = idGen
	return r
}
