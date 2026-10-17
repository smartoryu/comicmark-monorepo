// Package config loads ComicMark runtime configuration.
//
// Configuration comes from environment variables only, with an optional .env
// file for local development. Every setting has a default, so the API starts
// with nothing configured beyond a JWT secret; anything that cannot be safely
// defaulted is caught by Config.Validate.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// Recognised values for APP_ENV.
const (
	EnvDevelopment = "development"
	EnvProduction  = "production"
	EnvTest        = "test"
)

// Recognised values for MAILER_DRIVER.
const (
	MailerDriverConsole = "console"
	MailerDriverSMTP    = "smtp"
)

// Config is the fully resolved application configuration.
type Config struct {
	App      App
	Mailer   Mailer
	Scraper  Scraper
	Firebase Firebase
}

// App holds HTTP server, auth and rate-limit settings.
type App struct {
	Env        string
	BaseURL    string
	ServerAddr string

	DatabasePath string

	JWTSecret string
	JWTIssuer string

	AccessTokenTTL  time.Duration
	RefreshTokenTTL time.Duration

	// SessionIdleTTL is how long a device session may go untouched before its
	// refresh tokens stop working.
	SessionIdleTTL time.Duration
	// SessionTouchInterval throttles writes to sessions.last_seen_at.
	SessionTouchInterval time.Duration

	CORSOrigins []string

	RateLimitRPS             int
	RateLimitBurst           int
	AuthRateLimitPerMin      int
	SensitiveRateLimitPerMin int

	RequireEmailVerification bool
	VerificationTokenTTL     time.Duration
	PasswordResetTokenTTL    time.Duration
}

// Mailer configures outbound email.
type Mailer struct {
	Driver       string
	From         string
	SMTPHost     string
	SMTPPort     int
	SMTPUser     string
	SMTPPass     string
	SMTPStartTLS bool
	SMTPTimeout  time.Duration
}

// Scraper configures the OpenGraph fetcher.
type Scraper struct {
	UserAgent  string
	ContactURL string
	Timeout    time.Duration
	MaxBytes   int64
	// AllowPrivateIPs disables the SSRF guard. Development and the end-to-end
	// script need it to scrape a fixture served on 127.0.0.1. Never enable it
	// in production.
	AllowPrivateIPs bool
}

// Firebase configures optional social sign-in. Empty ProjectID disables it.
type Firebase struct {
	ProjectID        string
	CredentialsFile  string
	AuthEmulatorHost string
}

// IsDevelopment reports whether the API runs in development mode.
func (c *Config) IsDevelopment() bool { return c.App.Env == EnvDevelopment }

// IsTest reports whether the API runs under test.
func (c *Config) IsTest() bool { return c.App.Env == EnvTest }

// IsProduction reports whether the API runs in production mode.
func (c *Config) IsProduction() bool { return c.App.Env == EnvProduction }

// FirebaseEnabled reports whether Firebase sign-in should be mounted. Without a
// project ID the route is absent and clients hide the Google button.
func (c *Config) FirebaseEnabled() bool { return c.App.Env != "" && c.Firebase.ProjectID != "" }

// Load resolves configuration from envFile when one is given, otherwise from a
// .env file in the working directory if present. Real environment variables
// always win over .env values, and a missing .env is not an error.
func Load(envFile string) (*Config, error) {
	if envFile != "" {
		if err := godotenv.Load(envFile); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("load env file %q: %w", envFile, err)
		}
	} else {
		// Both are optional; ignore any error and fall back to real env vars.
		_ = godotenv.Load(".env", "config/.env")
	}

	cfg := Defaults()
	l := &envLoader{}
	l.apply(cfg)
	if err := l.err(); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// envLoader reads environment variables into a config, collecting every parse
// failure so the operator sees all mistakes at once instead of one per run.
type envLoader struct {
	errs []error
}

func (l *envLoader) err() error {
	if len(l.errs) == 0 {
		return nil
	}
	return fmt.Errorf("invalid configuration: %w", errors.Join(l.errs...))
}

// lookup returns the value for key, treating an empty value as unset so that
// blank entries in a copied .env.example behave like absent variables.
func (l *envLoader) lookup(key string) (string, bool) {
	v, ok := os.LookupEnv(key)
	if !ok {
		return "", false
	}
	v = strings.TrimSpace(v)
	if v == "" {
		return "", false
	}
	return v, true
}

func (l *envLoader) str(key, def string) string {
	if v, ok := l.lookup(key); ok {
		return v
	}
	return def
}

func (l *envLoader) duration(key string, def time.Duration) time.Duration {
	v, ok := l.lookup(key)
	if !ok {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		l.errs = append(l.errs, fmt.Errorf("%s: %q is not a duration (e.g. 15m, 720h): %w", key, v, err))
		return def
	}
	return d
}

func (l *envLoader) integer(key string, def int) int {
	v, ok := l.lookup(key)
	if !ok {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		l.errs = append(l.errs, fmt.Errorf("%s: %q is not an integer: %w", key, v, err))
		return def
	}
	return n
}

func (l *envLoader) int64(key string, def int64) int64 {
	v, ok := l.lookup(key)
	if !ok {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		l.errs = append(l.errs, fmt.Errorf("%s: %q is not an integer: %w", key, v, err))
		return def
	}
	return n
}

func (l *envLoader) boolean(key string, def bool) bool {
	v, ok := l.lookup(key)
	if !ok {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		l.errs = append(l.errs, fmt.Errorf("%s: %q is not a boolean (use true/false): %w", key, v, err))
		return def
	}
	return b
}

func (l *envLoader) slice(key string, def []string) []string {
	v, ok := l.lookup(key)
	if !ok {
		return def
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (l *envLoader) apply(c *Config) {
	c.App.Env = l.str("APP_ENV", c.App.Env)
	c.App.BaseURL = l.str("APP_BASE_URL", c.App.BaseURL)
	c.App.ServerAddr = l.str("SERVER_ADDR", c.App.ServerAddr)
	c.App.DatabasePath = l.str("DATABASE_PATH", c.App.DatabasePath)

	c.App.JWTSecret = l.str("JWT_SECRET", c.App.JWTSecret)
	c.App.JWTIssuer = l.str("JWT_ISSUER", c.App.JWTIssuer)

	c.App.AccessTokenTTL = l.duration("ACCESS_TOKEN_TTL", c.App.AccessTokenTTL)
	c.App.RefreshTokenTTL = l.duration("REFRESH_TOKEN_TTL", c.App.RefreshTokenTTL)
	c.App.SessionIdleTTL = l.duration("SESSION_IDLE_TTL", c.App.SessionIdleTTL)
	c.App.SessionTouchInterval = l.duration("SESSION_TOUCH_INTERVAL", c.App.SessionTouchInterval)

	c.App.CORSOrigins = l.slice("CORS_ORIGINS", c.App.CORSOrigins)

	c.App.RateLimitRPS = l.integer("RATE_LIMIT_RPS", c.App.RateLimitRPS)
	c.App.RateLimitBurst = l.integer("RATE_LIMIT_BURST", c.App.RateLimitBurst)
	c.App.AuthRateLimitPerMin = l.integer("AUTH_RATE_LIMIT_PER_MIN", c.App.AuthRateLimitPerMin)
	c.App.SensitiveRateLimitPerMin = l.integer("SENSITIVE_RATE_LIMIT_PER_MIN", c.App.SensitiveRateLimitPerMin)

	c.App.RequireEmailVerification = l.boolean("AUTH_REQUIRE_EMAIL_VERIFICATION", c.App.RequireEmailVerification)
	c.App.VerificationTokenTTL = l.duration("VERIFICATION_TOKEN_TTL", c.App.VerificationTokenTTL)
	c.App.PasswordResetTokenTTL = l.duration("PASSWORD_RESET_TOKEN_TTL", c.App.PasswordResetTokenTTL)

	c.Mailer.Driver = strings.ToLower(l.str("MAILER_DRIVER", c.Mailer.Driver))
	c.Mailer.From = l.str("MAILER_FROM", c.Mailer.From)
	c.Mailer.SMTPHost = l.str("SMTP_HOST", c.Mailer.SMTPHost)
	c.Mailer.SMTPPort = l.integer("SMTP_PORT", c.Mailer.SMTPPort)
	c.Mailer.SMTPUser = l.str("SMTP_USERNAME", c.Mailer.SMTPUser)
	c.Mailer.SMTPPass = l.str("SMTP_PASSWORD", c.Mailer.SMTPPass)
	c.Mailer.SMTPStartTLS = l.boolean("SMTP_STARTTLS", c.Mailer.SMTPStartTLS)
	c.Mailer.SMTPTimeout = l.duration("SMTP_TIMEOUT", c.Mailer.SMTPTimeout)

	c.Scraper.UserAgent = l.str("SCRAPER_USER_AGENT", c.Scraper.UserAgent)
	c.Scraper.ContactURL = l.str("SCRAPER_CONTACT_URL", c.Scraper.ContactURL)
	c.Scraper.Timeout = l.duration("SCRAPER_TIMEOUT", c.Scraper.Timeout)
	c.Scraper.MaxBytes = l.int64("SCRAPER_MAX_BYTES", c.Scraper.MaxBytes)
	c.Scraper.AllowPrivateIPs = l.boolean("SCRAPER_ALLOW_PRIVATE_IPS", c.Scraper.AllowPrivateIPs)

	c.Firebase.ProjectID = l.str("FIREBASE_PROJECT_ID", c.Firebase.ProjectID)
	c.Firebase.CredentialsFile = l.str("FIREBASE_CREDENTIALS_FILE", c.Firebase.CredentialsFile)
	c.Firebase.AuthEmulatorHost = l.str("FIREBASE_AUTH_EMULATOR_HOST", c.Firebase.AuthEmulatorHost)
}
