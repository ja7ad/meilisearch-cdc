// Command meilisearch-cdc streams database changes into Meilisearch.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/ja7ad/meilisearch-cdc/internal/admincli"
	"github.com/ja7ad/meilisearch-cdc/internal/app"
	"github.com/ja7ad/meilisearch-cdc/internal/config"
	_ "github.com/ja7ad/meilisearch-cdc/internal/source/all"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "version" || os.Args[1] == "-version" || os.Args[1] == "--version") {
		fmt.Println(version)
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "admin" {
		os.Exit(admincli.Run(context.Background(), os.Args[2:], admincli.IO{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr}))
	}
	cfg, err := config.Load(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	log := app.NewLogger(cfg).With("version", version)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	a, err := app.New(cfg, log)
	if err != nil {
		log.Error("startup failed", "err", err)
		os.Exit(1)
	}
	if err := a.Run(ctx); err != nil {
		log.Error("stopped with error", "err", err)
		os.Exit(1)
	}
}
