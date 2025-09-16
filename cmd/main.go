package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/strettch/strettch-cloud/sc-console-agent/pkg/config"
	"github.com/strettch/strettch-cloud/sc-console-agent/pkg/pipeline"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

const (
	defaultReadHeaderTimeout = 10 * time.Second
	defaultReadTimeout       = 30 * time.Second
	defaultWriteTimeout      = 30 * time.Second
	defaultIdleTimeout       = 60 * time.Second
	defaultShutdownTimeout   = 30 * time.Second
)

var (
	version   = "dev"
	commit    = "unknown"
	buildTime = "unknown"
)

type Agent struct {
	config      *config.Config
	logger      *zap.Logger
	processor   *pipeline.Processor
	httpServer  *http.Server
}

func main() {
	var (
		showVersion    = flag.Bool("v", false, "Print version and exit")
		validateConfig = flag.Bool("validate-config", false, "Validate configuration and exit")
		configFile     = flag.String("config", "", "Path to configuration file")
	)
	flag.Parse()

	if *showVersion {
		fmt.Printf("Version: %s\nCommit: %s\nBuildTime: %s\n", version, commit, buildTime)
		os.Exit(0)
	}

	// Set config file path if provided
	if *configFile != "" {
		os.Setenv("SC_AGENT_CONFIG", *configFile)
	}

	if *validateConfig {
		_, err := config.Load()
		if err != nil {
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

	logger := initLogger("info")
	defer syncLogger(logger)

	agent := &Agent{logger: logger}
	if err := agent.initialize(); err != nil {
		return err
	}
	return agent.run()
}

func (a *Agent) initialize() error {
	cfg, err := config.Load()
	if err != nil {
		if strings.Contains(err.Error(), "ResourceID cannot be determined") {
			a.logger.Fatal("Resource_id detection failed",
				zap.Error(err),
				zap.String("help", "Ensure dmidecode is installed or set SC_RESOURCE_ID manually"))
		}
		return fmt.Errorf("config load failed: %w", err)
	}
	a.config = cfg

	if cfg.LogLevel != "info" {
		a.logger = initLogger(cfg.LogLevel)
	}

	a.logger.Info("Starting Strettch Cloud Console Agent",
		zap.String("version", version),
		zap.Int("http_port", cfg.Console.HTTPNotifyPort))

	if a.processor, err = pipeline.NewProcessor(cfg, a.logger); err != nil {
		return fmt.Errorf("processor creation failed: %w", err)
	}

	if err := a.processor.Start(); err != nil {
		return fmt.Errorf("processor start failed: %w", err)
	}


	a.httpServer = &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Console.HTTPNotifyPort),
		ReadHeaderTimeout: defaultReadHeaderTimeout,
		ReadTimeout:       defaultReadTimeout,
		WriteTimeout:      defaultWriteTimeout,
		IdleTimeout:       defaultIdleTimeout,
	}

	return nil
}

func (a *Agent) run() error {
	go func() {
		if err := a.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			a.logger.Error("HTTP server error", zap.Error(err))
		}
	}()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	sig := <-sigChan
	a.logger.Info("Shutting down", zap.String("signal", sig.String()))

	return a.shutdown()
}

func (a *Agent) shutdown() error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultShutdownTimeout)
	defer cancel()

	var shutdownErr error

	if err := a.httpServer.Shutdown(ctx); err != nil {
		a.logger.Error("HTTP server shutdown failed", zap.Error(err))
		shutdownErr = err
	}

	if err := a.processor.Stop(); err != nil {
		a.logger.Error("Processor shutdown failed", zap.Error(err))
		shutdownErr = err
	}

	return shutdownErr
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

func syncLogger(logger *zap.Logger) {
	if err := logger.Sync(); err != nil && !strings.Contains(err.Error(), "inappropriate ioctl") {
		fmt.Fprintf(os.Stderr, "Failed to sync logger: %v\n", err)
	}
}
