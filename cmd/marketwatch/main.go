package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/urfave/cli/v3"

	"marketwatch/internal/monitor"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(os.Args[1:], logger); err != nil {
		logger.Error("check_failed", "reason", err.Error())
		os.Exit(1)
	}
}
func run(args []string, logger *slog.Logger) error {
	if len(args) == 0 {
		return errors.New("usage: marketwatch check [--config /config/config.yaml] [--state /data/state.json] [--dry-run]")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	app := &cli.Command{
		Name:  "marketwatch",
		Usage: "Check CoinLore rules and notify through Pushover",
		Action: func(context.Context, *cli.Command) error {
			return errors.New("usage: marketwatch check [options]")
		},
		Commands: []*cli.Command{{
			Name:  "check",
			Usage: "Evaluate rules once",
			Flags: []cli.Flag{
				&cli.StringFlag{Name: "config", Value: "/config/config.yaml", Usage: "rules YAML", Sources: cli.EnvVars("MARKETWATCH_CONFIG")},
				&cli.StringFlag{Name: "state", Value: "/data/state.json", Usage: "persistent JSON state", Sources: cli.EnvVars("MARKETWATCH_STATE")},
				&cli.BoolFlag{Name: "dry-run", Usage: "preview without sending or writing", Sources: cli.EnvVars("MARKETWATCH_DRY_RUN")},
			},
			Action: func(ctx context.Context, cmd *cli.Command) error {
				if cmd.Args().Len() != 0 {
					return errors.New("unexpected positional arguments")
				}
				config, state := cmd.String("config"), cmd.String("state")
				if config == "" || state == "" {
					return errors.New("config and state paths must be nonempty")
				}
				ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
				defer cancel()
				return monitor.New(logger).Run(ctx, monitor.Options{
					ConfigPath: config, StatePath: state, DryRun: cmd.Bool("dry-run"),
					Token: os.Getenv("PUSHOVER_APP_TOKEN"), User: os.Getenv("PUSHOVER_USER_KEY"),
				})
			},
		}},
	}
	return app.Run(ctx, append([]string{"marketwatch"}, args...))
}
