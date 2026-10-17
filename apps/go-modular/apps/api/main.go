// Command api is the ComicMark HTTP API server.
//
// It serves the JSON API under /api/v1, a health check at /healthz, and the
// embedded Flutter web build for everything else, so a single binary and a
// single origin are enough to run the whole product.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"comicmark/go-modular/internal/config"
	"comicmark/go-modular/internal/pkg/database"
	"comicmark/go-modular/internal/pkg/jwt"
	"comicmark/go-modular/internal/pkg/mailer"
	"comicmark/go-modular/internal/pkg/password"
	"comicmark/go-modular/internal/pkg/scraper"
)

// Server timeouts. The write timeout allows for a scrape, which can take up to
// SCRAPER_TIMEOUT, plus room to write the response.
const (
	readHeaderTimeout = 10 * time.Second
	readTimeout       = 30 * time.Second
	writeTimeout      = 60 * time.Second
	idleTimeout       = 120 * time.Second
	maxHeaderBytes    = 1 << 20 // 1 MiB
	shutdownTimeout   = 15 * time.Second
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "comicmark: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	envFile := flag.String("env-file", "", "path to a .env file to load (default: .env in the working directory)")
	autoMigrate := flag.Bool("auto-migrate", true, "apply pending database migrations before serving (default on in development)")
	flag.Parse()

	// Distinguish "not specified" from "explicitly false" so that the
	// development default can differ from the production default.
	autoMigrateSet := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "auto-migrate" {
			autoMigrateSet = true
		}
	})

	cfg, err := config.Load(*envFile)
	if err != nil {
		return err
	}

	logger := newLogger(cfg)

	migrate := *autoMigrate
	if !autoMigrateSet && !cfg.IsDevelopment() {
		// Applying schema changes silently is too surprising outside development.
		migrate = false
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := database.Open(ctx, cfg.App.DatabasePath)
	if err != nil {
		return err
	}
	defer func() {
		if err := db.Close(); err != nil {
			logger.Warn("closing database failed", "error", err)
		}
	}()

	if migrate {
		applied, err := database.NewMigrator(db).Up(ctx)
		if err != nil {
			return err
		}
		if len(applied) > 0 {
			logger.Info("applied database migrations", "versions", applied)
		} else {
			logger.Debug("database schema is up to date")
		}
	} else {
		logger.Info("skipping database migrations", "hint", "pass --auto-migrate to apply them")
	}

	jwtManager, err := jwt.NewManager(
		[]byte(cfg.App.JWTSecret),
		cfg.App.JWTIssuer,
		cfg.App.AccessTokenTTL,
		cfg.App.RefreshTokenTTL,
	)
	if err != nil {
		return fmt.Errorf("configure jwt: %w", err)
	}

	driver, err := mailer.New(mailer.Options{
		Driver:       cfg.Mailer.Driver,
		From:         cfg.Mailer.From,
		SMTPHost:     cfg.Mailer.SMTPHost,
		SMTPPort:     cfg.Mailer.SMTPPort,
		SMTPUser:     cfg.Mailer.SMTPUser,
		SMTPPass:     cfg.Mailer.SMTPPass,
		SMTPStartTLS: cfg.Mailer.SMTPStartTLS,
		SMTPTimeout:  cfg.Mailer.SMTPTimeout,
		Logger:       logger,
	})
	if err != nil {
		return fmt.Errorf("configure mailer: %w", err)
	}
	// Delivery is queued so a slow mail server cannot delay an HTTP response.
	asyncMailer := mailer.Async(driver, logger, 0)

	// Firebase sign-in is optional and entirely absent when unconfigured: the
	// route is not mounted, so the app hides the Google button instead of
	// offering a button that cannot work.
	firebaseVerifier, err := newFirebaseVerifier(cfg, logger)
	if err != nil {
		return err
	}

	// The scraper enforces its own destination rules, response cap and timeout.
	fetcher, err := scraper.New(scraper.Options{
		UserAgent:  cfg.Scraper.UserAgent,
		ContactURL: cfg.Scraper.ContactURL,
		Timeout:    cfg.Scraper.Timeout,
		MaxBytes:   cfg.Scraper.MaxBytes,
		Guard:      scraper.NewGuard(scraper.GuardOptions{AllowPrivate: cfg.Scraper.AllowPrivateIPs}),
		Logger:     logger,
	})
	if err != nil {
		return fmt.Errorf("configure scraper: %w", err)
	}

	api, err := newAPIServer(cfg, logger, db, jwtManager, password.NewHasher(), asyncMailer, firebaseVerifier, fetcher)
	if err != nil {
		return err
	}
	defer api.close()

	srv := &http.Server{
		Addr:              cfg.App.ServerAddr,
		Handler:           api.handler(),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}

	logger.Info("starting comicmark",
		"addr", cfg.App.ServerAddr,
		"env", cfg.App.Env,
		"database", cfg.App.DatabasePath,
		"mailer", cfg.Mailer.Driver,
		"base_url", cfg.App.BaseURL,
		"require_email_verification", cfg.App.RequireEmailVerification,
		"firebase_enabled", cfg.FirebaseEnabled(),
		"scraper_allow_private_ips", cfg.Scraper.AllowPrivateIPs,
		"cors_origins", cfg.App.CORSOrigins,
	)

	serveErr := make(chan error, 1)
	go func() {
		err := srv.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serveErr <- err
	}()

	select {
	case err := <-serveErr:
		// The listener failed outright, most likely the port is taken.
		return err
	case <-ctx.Done():
		logger.Info("shutdown signal received, draining connections")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Warn("http shutdown did not complete cleanly", "error", err)
	}
	if err := asyncMailer.Shutdown(shutdownCtx); err != nil {
		logger.Warn("mailer shutdown did not complete cleanly", "error", err)
	}

	logger.Info("stopped")
	return nil
}

// newLogger returns a human-readable logger in development and structured JSON
// everywhere else. Development also logs at debug level, which is where the
// console mailer prints message bodies.
func newLogger(cfg *config.Config) *slog.Logger {
	if cfg.IsDevelopment() {
		return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
			Level: slog.LevelDebug,
		}))
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
}
