package doris

import (
	"testing"

	mysqldriver "github.com/go-mysql-org/go-mysql/driver"
)

func TestDSN_withDB(t *testing.T) {
	p := Params{Addr: "localhost:9030", User: "root", Password: "secret", DB: "aos"}
	got := dsn(p, p.DB)
	want := "root:secret@localhost:9030/aos?collation=utf8mb4_general_ci"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDSN_noDB(t *testing.T) {
	p := Params{Addr: "localhost:9030", User: "root", Password: "secret"}
	got := dsn(p, "")
	want := "root:secret@localhost:9030/?collation=utf8mb4_general_ci"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDSN_specialChars_roundtrip(t *testing.T) {
	p := Params{
		Addr:     "localhost:9030",
		User:     "user@domain",
		Password: "p@ss:w0rd/secret?foo#bar%baz",
		DB:       "my/db",
	}
	for _, tc := range []struct {
		name   string
		dbName string
	}{
		{"with db", p.DB},
		{"no db", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := dsn(p, tc.dbName)
			c, err := mysqldriver.ParseDSN(result)
			if err != nil {
				t.Fatalf("ParseDSN(%q): %v", result, err)
			}
			if c.User != p.User {
				t.Errorf("user: got %q, want %q", c.User, p.User)
			}
			if c.Password != p.Password {
				t.Errorf("password: got %q, want %q", c.Password, p.Password)
			}
			if c.DB != tc.dbName {
				t.Errorf("db: got %q, want %q", c.DB, tc.dbName)
			}
		})
	}
}
