package config

import "time"

// Defaults returns the configuration used when nothing is set in the
// environment. The values mirror the documented table in the plan so that a
// bare `go run ./apps/api` behaves like `moon run go-modular:dev`.
func Defaults() *Config {
	return &Config{
		App: App{
			Env:          EnvDevelopment,
			BaseURL:      "http://localhost:5173",
			ServerAddr:   ":8080",
			DatabasePath: "./data/comicmark.db",

			// Deliberately empty: Config.Validate rejects this outside
			// development, which forces an explicit secret to be configured.
			JWTSecret: "",
			JWTIssuer: "comicmark",

			AccessTokenTTL:  15 * time.Minute,
			RefreshTokenTTL: 720 * time.Hour, // 30 days

			SessionIdleTTL:       720 * time.Hour,
			SessionTouchInterval: 5 * time.Minute,

			CORSOrigins: []string{"http://localhost:5173"},

			RateLimitRPS:             20,
			RateLimitBurst:           40,
			AuthRateLimitPerMin:      5,
			SensitiveRateLimitPerMin: 3,

			RequireEmailVerification: false,
			VerificationTokenTTL:     24 * time.Hour,
			PasswordResetTokenTTL:    time.Hour,
		},
		Mailer: Mailer{
			Driver:      MailerDriverConsole,
			From:        "ComicMark <no-reply@comicmark.local>",
			SMTPHost:    "localhost",
			SMTPPort:    1025,
			SMTPTimeout: 10 * time.Second,
		},
		Scraper: Scraper{
			UserAgent:  "ComicMarkBot/1.0",
			ContactURL: "https://example.com/bot",
			Timeout:    10 * time.Second,
			MaxBytes:   2 << 20, // 2 MiB
		},
		Firebase: Firebase{},
	}
}
