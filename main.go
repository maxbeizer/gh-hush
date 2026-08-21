package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
)

func main() {
	userMessages := log.New(os.Stderr, "", 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer func() {
		signal.Stop(c)
		cancel()
	}()

	go func() {
		for sig := range c {
			userMessages.Printf("received signal %v", sig)
			cancel()
		}
	}()

	rootCmd := &cobra.Command{
		Use:   "gh-extension-template",
		Short: "TODO: describe your extension",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("Hello from gh-extension-template! Replace this with your implementation.")
			return nil
		},
	}

	if err := rootCmd.ExecuteContext(ctx); err != nil {
		userMessages.Printf("error: %v", err)
		os.Exit(1)
	}
}
