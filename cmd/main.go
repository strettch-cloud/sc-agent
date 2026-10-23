package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/strettch-cloud/sc-agent/pkg/config"
	"github.com/strettch-cloud/sc-agent/pkg/pipeline"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var (
	version   = "dev"
	commit    = "unknown"
	buildTime = "unknown"
)

type Agent struct {
	config    *config.Config
	logger    *zap.Logger
	processor *pipeline.Processor
}

func main() {
	var (
		showVersion    = flag.Bool("v", false, "Print version and exit")
		validateConfig = flag.Bool("validate-config", false, "Validate configuration, including /etc/sc-console-agent/config.yaml, and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Printf("Version: %s\nCommit: %s\nBuildTime: %s\n", version, commit, buildTime)
		os.Exit(0)
	}

	if *validateConfig {
		if err := config.Validate(); err != nil {
			fmt.Fprintf(os.Stderr, "Configuration validation failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Configuration is valid")
		os.Exit(0)
	}

	if err := runAgent(); err != nil {
		fmt.Fprintf(os.Stderr, "Agent failed: %v\n", err)
		os.Exit(1)
	}
}

func runAgent() error {
	if os.Getuid() != 0 {
		fmt.Fprintf(os.Stderr, "Warning: Running as non-root user may limit SSH key management capabilities\n")
	}

	// The logger writes each line straight to stdout, so it never needs a
	// Sync, and Sync on a socket, as stdout is under systemd, only fails.
	logger := initLogger("info")

	agent := &Agent{logger: logger}
	if err := agent.initialize(); err != nil {
		return err
	}
	agent.run()
	return nil
}

func (a *Agent) initialize() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config load failed: %w", err)
	}
	a.config = cfg

	if cfg.LogLevel != "info" {
		a.logger = initLogger(cfg.LogLevel)
	}

	a.logger.Info("Starting Strettch Cloud Console Agent",
		zap.String("version", version))

	if a.processor, err = pipeline.NewProcessor(cfg, a.logger); err != nil {
		return fmt.Errorf("processor creation failed: %w", err)
	}

	if err := a.processor.Start(); err != nil {
		return fmt.Errorf("processor start failed: %w", err)
	}

	return nil
}

func (a *Agent) run() {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	sig := <-sigChan
	a.logger.Info("Shutting down", zap.String("signal", sig.String()))
	a.processor.Stop()
}

func initLogger(logLevel string) *zap.Logger {
	level := zapcore.InfoLevel
	switch logLevel {
	case "debug":
		level = zapcore.DebugLevel
	case "warn":
		level = zapcore.WarnLevel
	case "error":
		level = zapcore.ErrorLevel
	}

	encoderConfig := zapcore.EncoderConfig{
		TimeKey:        "time",
		LevelKey:       "level",
		MessageKey:     "msg",
		CallerKey:      "caller",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.LowercaseLevelEncoder,
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeDuration: zapcore.SecondsDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	}

	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(encoderConfig),
		zapcore.AddSync(os.Stdout),
		level,
	)

	return zap.New(core, zap.AddCaller())
}
