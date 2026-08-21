package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/maxbeizer/gh-hush/cmd"
)

func main() {
	userMessages := log.New(os.Stderr, "", 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(signals)

	go func() {
		select {
		case sig := <-signals:
			userMessages.Printf("received signal %v", sig)
			cancel()
		case <-ctx.Done():
		}
	}()

	rootCmd := cmd.NewRootCommand(os.Stdout, os.Stderr)
	if err := rootCmd.ExecuteContext(ctx); err != nil {
		userMessages.Printf("error: %v", err)
		os.Exit(1)
	}
}
