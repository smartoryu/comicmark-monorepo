package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// missingEnvFile is passed to Load so tests never accidentally pick up a real
// .env sitting next to the package. A missing file is not an error.
func missingEnvFile(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "does-not-exist.env")
}

// validDev returns a development config that passes validation.
func validDev() *Config {
	c := Defaults()
	c.App.JWTSecret = "dev-secret"
	return c
}

func TestDefaultsAreValidInDevelopment(t *testing.T) {
	require.NoError(t, validDev().Validate())
}

func TestValidateRejectsMissingJWTSecret(t *testing.T) {
	c := Defaults()
	err := c.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "JWT_SECRET is required")
}

func TestValidateAllowsShortSecretInDevelopmentOnly(t *testing.T) {
	c := validDev()
	c.App.JWTSecret = "short"
	require.NoError(t, c.Validate(), "development should accept a short secret")

	c.App.Env = EnvProduction
	err := c.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at least 32 bytes")
}

func TestValidateRejectsUnknownEnv(t *testing.T) {
	c := validDev()
	c.App.Env = "staging"
	err := c.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "APP_ENV")
}

func TestValidateRejectsProductionConsoleMailerWithRequiredVerification(t *testing.T) {
	c := validDev()
	c.App.Env = EnvProduction
	c.App.JWTSecret = strings.Repeat("x", 40)
	c.App.RequireEmailVerification = true
	c.Mailer.Driver = MailerDriverConsole

	err := c.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "could never receive their verification link")

	// The same settings with a real SMTP driver are fine.
	c.Mailer.Driver = MailerDriverSMTP
	require.NoError(t, c.Validate())
}

func TestValidateRejectsSMTPWithoutHost(t *testing.T) {
	c := validDev()
	c.Mailer.Driver = MailerDriverSMTP
	c.Mailer.SMTPHost = ""

	err := c.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SMTP_HOST is required")
}

func TestValidateRejectsWildcardCORS(t *testing.T) {
	c := validDev()
	c.App.CORSOrigins = []string{"http://localhost:5173", "*"}

	err := c.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CORS_ORIGINS")
}

func TestValidateRejectsRefreshShorterThanAccess(t *testing.T) {
	c := validDev()
	c.App.AccessTokenTTL = time.Hour
	c.App.RefreshTokenTTL = time.Minute

	err := c.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must exceed ACCESS_TOKEN_TTL")
}

func TestValidateRejectsPrivateIPsInProduction(t *testing.T) {
	c := validDev()
	c.App.Env = EnvProduction
	c.App.JWTSecret = strings.Repeat("x", 40)
	c.Scraper.AllowPrivateIPs = true

	err := c.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SSRF")

	// Allowed in development, where the e2e script scrapes a local fixture.
	c.App.Env = EnvDevelopment
	require.NoError(t, c.Validate())
}

func TestValidateRejectsFirebaseEmulatorInProduction(t *testing.T) {
	c := validDev()
	c.App.Env = EnvProduction
	c.App.JWTSecret = strings.Repeat("x", 40)
	c.Firebase.ProjectID = "comicmark-demo"
	c.Firebase.AuthEmulatorHost = "localhost:9099"

	err := c.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "FIREBASE_AUTH_EMULATOR_HOST")
}

func TestValidateRejectsFirebaseSettingsWithoutProjectID(t *testing.T) {
	c := validDev()
	c.Firebase.CredentialsFile = "/tmp/service-account.json"

	err := c.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "FIREBASE_PROJECT_ID is required")
}

func TestValidateRejectsMalformedBaseURL(t *testing.T) {
	c := validDev()
	c.App.BaseURL = "localhost:5173" // no scheme

	err := c.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "absolute URL")
}

func TestFirebaseEnabled(t *testing.T) {
	c := validDev()
	assert.False(t, c.FirebaseEnabled())

	c.Firebase.ProjectID = "comicmark-demo"
	assert.True(t, c.FirebaseEnabled())
}

func TestLoadAppliesEnvironment(t *testing.T) {
	t.Setenv("JWT_SECRET", "loaded-from-env")
	t.Setenv("SERVER_ADDR", ":9999")
	t.Setenv("ACCESS_TOKEN_TTL", "45m")
	t.Setenv("REFRESH_TOKEN_TTL", "48h")
	t.Setenv("AUTH_REQUIRE_EMAIL_VERIFICATION", "true")
	t.Setenv("CORS_ORIGINS", "http://localhost:5173, https://comicmark.example ")
	t.Setenv("SCRAPER_MAX_BYTES", "1048576")

	cfg, err := Load(missingEnvFile(t))
	require.NoError(t, err)

	assert.Equal(t, "loaded-from-env", cfg.App.JWTSecret)
	assert.Equal(t, ":9999", cfg.App.ServerAddr)
	assert.Equal(t, 45*time.Minute, cfg.App.AccessTokenTTL)
	assert.Equal(t, 48*time.Hour, cfg.App.RefreshTokenTTL)
	assert.True(t, cfg.App.RequireEmailVerification)
	assert.Equal(t, []string{"http://localhost:5173", "https://comicmark.example"}, cfg.App.CORSOrigins)
	assert.Equal(t, int64(1048576), cfg.Scraper.MaxBytes)
}

func TestLoadTreatsEmptyValuesAsUnset(t *testing.T) {
	t.Setenv("JWT_SECRET", "dev-secret")
	t.Setenv("FIREBASE_PROJECT_ID", "")
	t.Setenv("MAILER_DRIVER", "")

	cfg, err := Load(missingEnvFile(t))
	require.NoError(t, err)

	assert.Empty(t, cfg.Firebase.ProjectID)
	assert.False(t, cfg.FirebaseEnabled())
	assert.Equal(t, MailerDriverConsole, cfg.Mailer.Driver, "empty MAILER_DRIVER should fall back to the default")
}

func TestLoadRejectsMalformedValues(t *testing.T) {
	t.Setenv("JWT_SECRET", "dev-secret")
	t.Setenv("ACCESS_TOKEN_TTL", "fifteen-minutes")
	t.Setenv("RATE_LIMIT_RPS", "lots")
	t.Setenv("AUTH_REQUIRE_EMAIL_VERIFICATION", "maybe")

	_, err := Load(missingEnvFile(t))
	require.Error(t, err)

	// All three mistakes surface in one message rather than one per run.
	assert.Contains(t, err.Error(), "ACCESS_TOKEN_TTL")
	assert.Contains(t, err.Error(), "RATE_LIMIT_RPS")
	assert.Contains(t, err.Error(), "AUTH_REQUIRE_EMAIL_VERIFICATION")
}

func TestLoadCollectsValidationErrors(t *testing.T) {
	t.Setenv("JWT_SECRET", "")
	t.Setenv("APP_BASE_URL", "not-a-url")
	t.Setenv("CORS_ORIGINS", "*")

	_, err := Load(missingEnvFile(t))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "JWT_SECRET")
	assert.Contains(t, err.Error(), "APP_BASE_URL")
	assert.Contains(t, err.Error(), "CORS_ORIGINS")
}
