// hero is the CLI entry point. Subcommands:
//
//	hero watch              start the inbox watcher (default)
//	hero run <story.md>     process a single story and exit
//	hero plan <plan.md>     turn a high-level plan into N user stories under inbox/<plan-name>/
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"hero-coding/internal/config"
	"hero-coding/internal/dispatcher"
	"hero-coding/internal/planner"
	"hero-coding/internal/role"
)

func main() {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "getwd:", err)
		os.Exit(2)
	}

	cfg, err := config.Load(cwd)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(2)
	}

	paths := dispatcher.DefaultPaths(cwd)

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
		d, err := dispatcher.New(cfg, paths)
		if err != nil {
			fmt.Fprintln(os.Stderr, "init dispatcher:", err)
			os.Exit(2)
		}
		if err := d.Watch(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "watch:", err)
			os.Exit(1)
		}
	case "run":
		if len(args) == 0 {
			fmt.Fprintln(os.Stderr, "usage: hero run <story.md>")
			os.Exit(2)
		}
		d, err := dispatcher.New(cfg, paths)
		if err != nil {
			fmt.Fprintln(os.Stderr, "init dispatcher:", err)
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
	case "plan":
		if len(args) == 0 {
			fmt.Fprintln(os.Stderr, "usage: hero plan <plan.md>")
			os.Exit(2)
		}
		planPath, err := filepath.Abs(args[0])
		if err != nil {
			fmt.Fprintln(os.Stderr, "abs:", err)
			os.Exit(2)
		}
		plannerCfg, err := cfg.LLMFor("planner")
		if err != nil {
			fmt.Fprintln(os.Stderr, "config: planner role:", err)
			os.Exit(2)
		}
		res, err := planner.Run(ctx, planner.Options{
			PlanPath:  planPath,
			OutputDir: paths.Inbox,
			Planner:   plannerCfg,
			Role:      role.Defaults().Planner,
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "plan:", err)
			os.Exit(1)
		}
		fmt.Printf("planned %q → %d stories under %s\n", res.PlanName, len(res.StoryFiles), filepath.Dir(res.ManifestPath))
		for _, p := range res.StoryFiles {
			rel, _ := filepath.Rel(cwd, p)
			fmt.Printf("  %s\n", rel)
		}
		manifestRel, _ := filepath.Rel(cwd, res.ManifestPath)
		fmt.Printf("manifest: %s\n", manifestRel)
	case "-h", "--help", "help":
		fmt.Println("usage: hero [watch | run <story.md> | plan <plan.md>]")
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
