package main

import (
	"context"
	"os"
	"os/signal"

	"dbmeta-cli/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	os.Exit(cli.Run(ctx, cli.DefaultEnv(), os.Args[1:]))
}
