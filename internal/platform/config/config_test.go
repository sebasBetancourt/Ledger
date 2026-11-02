package config_test

import (
	"os"
	"strings"
	"testing"

	"github.com/sebasBetancourt/Ledger/internal/platform/config"
)

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		env     *string
		want    string
		wantErr bool
	}{
		{name: "missing", env: nil, wantErr: true},
		{name: "empty", env: ptr(""), wantErr: true},
		{name: "set", env: ptr("postgres://db:5432/ledger"), want: "postgres://db:5432/ledger"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("DATABASE_URL", "")
			if tt.env == nil {
				if err := os.Unsetenv("DATABASE_URL"); err != nil {
					t.Fatal(err)
				}
			} else {
				t.Setenv("DATABASE_URL", *tt.env)
			}

			cfg, err := config.Load()
			if tt.wantErr {
				if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
					t.Fatalf("Load() error = %v, want an error naming DATABASE_URL", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if cfg.DatabaseURL != tt.want {
				t.Errorf("DatabaseURL = %q, want %q", cfg.DatabaseURL, tt.want)
			}
		})
	}
}

func ptr(s string) *string { return &s }
