package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/gilsu/fortigate-external-dns/internal/config"
	"github.com/gilsu/fortigate-external-dns/internal/controller"
	"github.com/gilsu/fortigate-external-dns/internal/fortigate"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.Load(os.Args[1:])
	if err != nil {
		logger.Error("configuration failed", "error", err)
		os.Exit(2)
	}
	if err := cfg.Validate(); err != nil {
		logger.Error("configuration invalid", "error", err)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	kubeClients, err := controller.NewKubernetesClients(cfg.Kubeconfig)
	if err != nil {
		logger.Error("kubernetes client setup failed", "error", err)
		os.Exit(1)
	}

	fortiClient, err := fortigate.NewClient(cfg.FortiGate, logger)
	if err != nil {
		logger.Error("fortigate client setup failed", "error", err)
		os.Exit(1)
	}

	runner := controller.Runner{
		Config:    cfg,
		Kube:      kubeClients,
		DNSClient: fortiClient,
		Logger:    logger,
	}

	if cfg.Once {
		err = runner.RunOnce(ctx)
	} else {
		err = runner.Run(ctx)
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("controller stopped with error", "error", err)
		os.Exit(1)
	}

	fmt.Fprintln(os.Stdout, "controller stopped")
}
