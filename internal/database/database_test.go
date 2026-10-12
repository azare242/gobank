package database

import "testing"

func TestInvalidConfig(t *testing.T) {
	for _, config := range []Config{
		{Driver: "unsupported"},
		{Driver: "postgres"},
		{Driver: "postgresql", DSN: " "},
	} {
		if db, err := OpenConfig(config); err == nil || db != nil {
			t.Fatalf("expected invalid configuration error for %q", config.Driver)
		}
	}
}
