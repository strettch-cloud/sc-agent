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

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"github.com/strettch/strettch-cloud/sc-agent/pkg/config"
	"github.com/strettch/strettch-cloud/sc-agent/pkg/console"
	"github.com/strettch/strettch-cloud/sc-agent/pkg/pipeline"
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
	config       *config.Config
	logger       *zap.Logger
	processor    *pipeline.Processor
	httpHandler  *console.HTTPHandler
	httpServer   *http.Server
}

func main() {
	var (
		showVersion    = flag.Bool("v", false, "Print version and exit")
		validateConfig = flag.String("validate-config", "", "Validate configuration file and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Printf("Version: %s\nCommit: %s\nBuildTime: %s\n", version, commit, buildTime)
		os.Exit(0)
	}

	if *validateConfig != "" {
		if err := validateConfigFile(*validateConfig); err != nil {
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

func validateConfigFile(configPath string) error {
	if err := os.Setenv("SC_AGENT_CONFIG", configPath); err != nil {
		return fmt.Errorf("failed to set config path: %w", err)
	}
	_, err := config.Load()
	return err
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
		if strings.Contains(err.Error(), "resource_id cannot be determined") {
			a.logger.Fatal("Resource ID detection failed",
				zap.Error(err),
				zap.String("help", "Ensure dmidecode is installed or set SC_RESOURCE_ID manually"))
		}
		return fmt.Errorf("config load failed: %w", err)
	}
	a.config = cfg

	if cfg.LogLevel != "info" {
		a.logger = initLogger(cfg.LogLevel)
	}

	a.logger.Info("Starting Strettch Cloud Agent",
		zap.String("version", version),
		zap.String("resource_id", cfg.ResourceID),
		zap.Int("udp_port", cfg.Console.UDPKnockPort),
		zap.Int("http_port", cfg.Console.HTTPNotifyPort))

	if a.processor, err = pipeline.NewProcessor(cfg, a.logger); err != nil {
		return fmt.Errorf("processor creation failed: %w", err)
	}

	if err := a.processor.Start(); err != nil {
		return fmt.Errorf("processor start failed: %w", err)
	}

	if a.httpHandler, err = console.NewHTTPHandler(cfg, a.logger); err != nil {
		return fmt.Errorf("HTTP handler creation failed: %w", err)
	}

	a.httpServer = &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Console.HTTPNotifyPort),
		Handler:           a.httpHandler,
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

	if err := a.httpHandler.Close(); err != nil {
		a.logger.Error("HTTP handler close failed", zap.Error(err))
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