// Command api es el proceso HTTP.
package main

import (
	"log/slog"
	"os"

	"github.com/sebasBetancourt/Ledger/internal/platform/config"
)

func main() {
	if _, err := config.Load(); err != nil {
		slog.Error("load config", "err", err)
		os.Exit(1)
	}
}
