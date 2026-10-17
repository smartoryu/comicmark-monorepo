package mailer

import (
	"context"
	"log/slog"
	"net/mail"
	"regexp"
)

// consoleMailer writes messages to the log instead of delivering them. It exists
// so that development and the end-to-end script need no SMTP server, while still
// exercising the real templates and the real message-building code.
type consoleMailer struct {
	from   *mail.Address
	logger *slog.Logger
}

// Send logs the message. The `link=` attribute is the stable, greppable handle
// scripts use to follow an action link.
func (c *consoleMailer) Send(ctx context.Context, msg Message) error {
	msg, err := enrich(msg)
	if err != nil {
		return err
	}

	attrs := []any{
		"from", c.from.String(),
		"to", msg.To,
		"subject", msg.Subject,
	}
	if link := firstLink(msg); link != "" {
		attrs = append(attrs, "link", link)
	}
	c.logger.InfoContext(ctx, "mailer: email sent (console driver)", attrs...)

	// The rendered bodies are only interesting when debugging a template, so
	// they stay at debug level to keep request logs readable.
	c.logger.DebugContext(ctx, "mailer: email body",
		"to", msg.To, "text", msg.Text, "html", msg.HTML)

	return nil
}

var hrefPattern = regexp.MustCompile(`href="([^"]+)"`)

// firstLink prefers the explicit Message.Link and falls back to the first href
// in the HTML body, so callers that forget to set it still produce a usable log.
func firstLink(msg Message) string {
	if msg.Link != "" {
		return msg.Link
	}
	if m := hrefPattern.FindStringSubmatch(msg.HTML); len(m) == 2 {
		return m[1]
	}
	return ""
}
