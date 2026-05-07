// hero is the CLI entry point. Two subcommands:
//
//	hero watch              start the inbox watcher (default)
//	hero run <story.md>     process a single story and exit
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/joho/godotenv"

	"hero-coding/internal/config"
	"hero-coding/internal/dispatcher"
)

func main() {
	// Load .env from the project root if present (silent if missing).
	_ = godotenv.Load()

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(2)
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "getwd:", err)
		os.Exit(2)
	}
	paths := dispatcher.DefaultPaths(cwd)
	d := dispatcher.New(cfg, paths)

	ctx, cancel := signalContext()
	defer cancel()

	args := os.Args[1:]
	cmd := "watch"
	if len(args) > 0 {
		cmd = args[0]
		args = args[1:]
	}

	switch cmd {
	case "watch":
		if err := d.Watch(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "watch:", err)
			os.Exit(1)
		}
	case "run":
		if len(args) == 0 {
			fmt.Fprintln(os.Stderr, "usage: hero run <story.md>")
			os.Exit(2)
		}
		path, err := filepath.Abs(args[0])
		if err != nil {
			fmt.Fprintln(os.Stderr, "abs:", err)
			os.Exit(2)
		}
		if _, err := d.RunOnce(ctx, path); err != nil {
			fmt.Fprintln(os.Stderr, "run:", err)
			os.Exit(1)
		}
	case "-h", "--help", "help":
		fmt.Println("usage: hero [watch | run <story.md>]")
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", cmd)
		os.Exit(2)
	}
}

func signalContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ch
		cancel()
	}()
	return ctx, cancel
}
