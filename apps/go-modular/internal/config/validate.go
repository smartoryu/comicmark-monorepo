package config

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Validate reports every configuration problem it can find at once, so a
// misconfigured deployment fails at startup with an actionable message rather
// than misbehaving later.
func (c *Config) Validate() error {
	var errs []error

	switch c.App.Env {
	case EnvDevelopment, EnvProduction, EnvTest:
	default:
		errs = append(errs, fmt.Errorf(
			"APP_ENV: %q is not one of %s, %s, %s",
			c.App.Env, EnvDevelopment, EnvProduction, EnvTest,
		))
	}

	errs = append(errs, c.validateAuth()...)
	errs = append(errs, c.validateMailer()...)
	errs = append(errs, c.validateScraper()...)
	errs = append(errs, c.validateFirebase()...)

	return errors.Join(errs...)
}

func (c *Config) validateAuth() []error {
	var errs []error

	switch {
	case c.App.JWTSecret == "":
		errs = append(errs, errors.New(
			"JWT_SECRET is required; generate one with `openssl rand -hex 32`",
		))
	case !c.IsDevelopment() && len(c.App.JWTSecret) < 32:
		errs = append(errs, fmt.Errorf(
			"JWT_SECRET must be at least 32 bytes outside development (got %d)",
			len(c.App.JWTSecret),
		))
	}

	if c.App.ServerAddr == "" {
		errs = append(errs, errors.New("SERVER_ADDR must not be empty"))
	}
	if c.App.DatabasePath == "" {
		errs = append(errs, errors.New("DATABASE_PATH must not be empty"))
	}

	if c.App.BaseURL == "" {
		errs = append(errs, errors.New("APP_BASE_URL must not be empty; it is used to build verification and reset links"))
	} else if u, err := url.Parse(c.App.BaseURL); err != nil {
		errs = append(errs, fmt.Errorf("APP_BASE_URL: %q is not a valid URL: %w", c.App.BaseURL, err))
	} else if !u.IsAbs() || u.Host == "" {
		errs = append(errs, fmt.Errorf("APP_BASE_URL: %q must be an absolute URL including scheme and host", c.App.BaseURL))
	}

	if c.App.AccessTokenTTL <= 0 {
		errs = append(errs, errors.New("ACCESS_TOKEN_TTL must be positive"))
	}
	if c.App.RefreshTokenTTL <= 0 {
		errs = append(errs, errors.New("REFRESH_TOKEN_TTL must be positive"))
	}
	if c.App.AccessTokenTTL > 0 && c.App.RefreshTokenTTL > 0 && c.App.RefreshTokenTTL <= c.App.AccessTokenTTL {
		errs = append(errs, fmt.Errorf(
			"REFRESH_TOKEN_TTL (%s) must exceed ACCESS_TOKEN_TTL (%s)",
			c.App.RefreshTokenTTL, c.App.AccessTokenTTL,
		))
	}
	if c.App.SessionIdleTTL <= 0 {
		errs = append(errs, errors.New("SESSION_IDLE_TTL must be positive"))
	}
	if c.App.SessionTouchInterval < 0 {
		errs = append(errs, errors.New("SESSION_TOUCH_INTERVAL must not be negative"))
	}

	if c.App.VerificationTokenTTL <= 0 {
		errs = append(errs, errors.New("VERIFICATION_TOKEN_TTL must be positive"))
	}
	if c.App.PasswordResetTokenTTL <= 0 {
		errs = append(errs, errors.New("PASSWORD_RESET_TOKEN_TTL must be positive"))
	}

	if c.App.RateLimitRPS <= 0 {
		errs = append(errs, errors.New("RATE_LIMIT_RPS must be positive"))
	}
	if c.App.RateLimitBurst <= 0 {
		errs = append(errs, errors.New("RATE_LIMIT_BURST must be positive"))
	}
	if c.App.AuthRateLimitPerMin <= 0 {
		errs = append(errs, errors.New("AUTH_RATE_LIMIT_PER_MIN must be positive"))
	}
	if c.App.SensitiveRateLimitPerMin <= 0 {
		errs = append(errs, errors.New("SENSITIVE_RATE_LIMIT_PER_MIN must be positive"))
	}

	for _, o := range c.App.CORSOrigins {
		if o == "*" {
			errs = append(errs, errors.New(
				"CORS_ORIGINS: \"*\" is not allowed because credentials are sent; list explicit origins",
			))
		}
	}

	return errs
}

func (c *Config) validateMailer() []error {
	var errs []error

	switch c.Mailer.Driver {
	case MailerDriverConsole:
	case MailerDriverSMTP:
		if c.Mailer.SMTPHost == "" {
			errs = append(errs, errors.New("SMTP_HOST is required when MAILER_DRIVER=smtp"))
		}
		if c.Mailer.SMTPPort < 1 || c.Mailer.SMTPPort > 65535 {
			errs = append(errs, fmt.Errorf("SMTP_PORT: %d is not a valid port", c.Mailer.SMTPPort))
		}
		if c.Mailer.SMTPTimeout <= 0 {
			errs = append(errs, errors.New("SMTP_TIMEOUT must be positive"))
		}
	default:
		errs = append(errs, fmt.Errorf(
			"MAILER_DRIVER: %q is not one of %s, %s",
			c.Mailer.Driver, MailerDriverConsole, MailerDriverSMTP,
		))
	}

	if strings.TrimSpace(c.Mailer.From) == "" {
		errs = append(errs, errors.New("MAILER_FROM must not be empty"))
	}

	// The combination that locks every new account out of a production
	// deployment: verification is mandatory but mail only reaches a log line.
	if c.IsProduction() && c.App.RequireEmailVerification && c.Mailer.Driver == MailerDriverConsole {
		errs = append(errs, errors.New(
			"AUTH_REQUIRE_EMAIL_VERIFICATION=true with MAILER_DRIVER=console in production: "+
				"new accounts could never receive their verification link; use MAILER_DRIVER=smtp",
		))
	}

	return errs
}

func (c *Config) validateScraper() []error {
	var errs []error

	if strings.TrimSpace(c.Scraper.UserAgent) == "" {
		errs = append(errs, errors.New("SCRAPER_USER_AGENT must not be empty; scraped sites deserve an honest caller"))
	}
	if c.Scraper.Timeout <= 0 {
		errs = append(errs, errors.New("SCRAPER_TIMEOUT must be positive"))
	}
	if c.Scraper.MaxBytes <= 0 {
		errs = append(errs, errors.New("SCRAPER_MAX_BYTES must be positive"))
	}
	if c.Scraper.AllowPrivateIPs && c.IsProduction() {
		errs = append(errs, errors.New(
			"SCRAPER_ALLOW_PRIVATE_IPS=true is not allowed in production: it disables SSRF protection",
		))
	}

	return errs
}

func (c *Config) validateFirebase() []error {
	var errs []error

	if c.Firebase.ProjectID == "" && (c.Firebase.CredentialsFile != "" || c.Firebase.AuthEmulatorHost != "") {
		errs = append(errs, errors.New(
			"FIREBASE_PROJECT_ID is required when FIREBASE_CREDENTIALS_FILE or FIREBASE_AUTH_EMULATOR_HOST is set",
		))
	}
	if c.Firebase.AuthEmulatorHost != "" && c.IsProduction() {
		errs = append(errs, errors.New(
			"FIREBASE_AUTH_EMULATOR_HOST must not be set in production: emulator tokens are not signed",
		))
	}

	return errs
}
