package main

import (
	"context"
	"fmt"
	"os"

	"github.com/mnm/sync-time-thing/internal/app"
	"github.com/mnm/sync-time-thing/internal/config"
)

var runMain = func() error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}

	program, err := app.New(context.Background(), cfg, app.Dependencies{})
	if err != nil {
		return err
	}

	return program.Serve(context.Background())
}

func main() {
	if err := runMain(); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
}
