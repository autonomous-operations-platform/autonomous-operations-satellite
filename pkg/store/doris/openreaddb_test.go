package doris_test

import (
	"testing"

	"github.com/autonomous-operations-platform/autonomous-operations-satellite/pkg/store/doris"
)

func TestOpenReadDB(t *testing.T) {
	db, err := doris.OpenReadDB(doris.Params{Addr: "127.0.0.1:9030", User: "root", DB: "aos"})
	if err != nil {
		t.Fatalf("OpenReadDB: %v", err)
	}
	if db == nil {
		t.Fatal("OpenReadDB returned nil db")
	}
	_ = db.Close()
}
