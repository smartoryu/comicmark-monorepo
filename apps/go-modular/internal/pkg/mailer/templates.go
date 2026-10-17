package mailer

import (
	"bytes"
	"embed"
	"fmt"
	"html"
	"regexp"
	"strings"
	"sync"
	"text/template"
)

// AppName is the product name used in email copy.
const AppName = "ComicMark"

//go:embed templates/*.html templates/*.txt
var templateFS embed.FS

// Templates are parsed once, on first use, rather than in init so that a
// packaging mistake surfaces as an error instead of a panic at startup.
var (
	templatesOnce sync.Once
	templates     *template.Template
	templatesErr  error
)

func loadTemplates() (*template.Template, error) {
	templatesOnce.Do(func() {
		templates, templatesErr = template.ParseFS(templateFS, "templates/*.html", "templates/*.txt")
	})
	return templates, templatesErr
}

// ActionData is the payload shared by the account emails: they all ask the user
// to follow one link before it expires.
type ActionData struct {
	// AppName is the product name shown in the copy. Defaults to AppName.
	AppName string
	// DisplayName is the user's name when known; templates fall back to a
	// generic greeting when empty.
	DisplayName string
	// Email is the address the message was sent to, repeated in the body so the
	// user can spot a misaddressed link.
	Email string
	// Link is the action URL.
	Link string
	// ExpiresIn is a human-readable lifetime, e.g. "24 hours".
	ExpiresIn string
}

func (d ActionData) withDefaults() ActionData {
	if strings.TrimSpace(d.AppName) == "" {
		d.AppName = AppName
	}
	return d
}

// greeting returns a personalised or generic salutation.
func (d ActionData) greeting() string {
	if strings.TrimSpace(d.DisplayName) != "" {
		return d.DisplayName
	}
	return "there"
}

// VerificationMessage builds the "confirm your email" email.
func VerificationMessage(to string, data ActionData) (Message, error) {
	return buildActionMessage("verification", to, data, "Confirm your "+data.withDefaults().AppName+" email address")
}

// PasswordResetMessage builds the "reset your password" email.
func PasswordResetMessage(to string, data ActionData) (Message, error) {
	return buildActionMessage("password_reset", to, data, "Reset your "+data.withDefaults().AppName+" password")
}

func buildActionMessage(kind, to string, data ActionData, subject string) (Message, error) {
	data = data.withDefaults()
	if data.DisplayName == "" {
		data.DisplayName = data.greeting()
	}
	if data.Email == "" {
		data.Email = to
	}
	if strings.TrimSpace(data.Link) == "" {
		return Message{}, fmt.Errorf("mailer: %s email requires a link", kind)
	}

	htmlBody, err := render(kind+".html", data)
	if err != nil {
		return Message{}, err
	}
	textBody, err := render(kind+".txt", data)
	if err != nil {
		return Message{}, err
	}

	return Message{
		To:      to,
		Subject: subject,
		HTML:    htmlBody,
		Text:    textBody,
		Link:    data.Link,
	}, nil
}

func render(name string, data ActionData) (string, error) {
	tmpls, err := loadTemplates()
	if err != nil {
		return "", fmt.Errorf("mailer: parse templates: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpls.ExecuteTemplate(&buf, name, data); err != nil {
		return "", fmt.Errorf("mailer: render %s: %w", name, err)
	}
	return buf.String(), nil
}

var (
	// script and style content is never useful as plain text. RE2 has no
	// backreferences, so the two element types are spelled out separately.
	htmlBlockPattern = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script>|<style\b[^>]*>.*?</style>`)
	htmlBreakPattern = regexp.MustCompile(`(?i)<br\s*/?>|</p>|</div>|</tr>|</h[1-6]>`)
	htmlTagPattern   = regexp.MustCompile(`(?s)<[^>]*>`)
	blankLines       = regexp.MustCompile(`\n{3,}`)
)

// plainTextFallback derives a readable text alternative from an HTML body, used
// when a caller supplies only HTML. It is deliberately simple: it keeps text and
// link targets, and drops markup.
func plainTextFallback(msg Message) string {
	if strings.TrimSpace(msg.HTML) == "" {
		// Nothing to derive from; fall back to the link so the message is still
		// actionable.
		return msg.Link
	}

	s := htmlBlockPattern.ReplaceAllString(msg.HTML, "")
	s = htmlBreakPattern.ReplaceAllString(s, "\n")
	s = htmlTagPattern.ReplaceAllString(s, "")
	s = html.UnescapeString(s)

	lines := strings.Split(s, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if line = strings.TrimSpace(line); line != "" {
			kept = append(kept, line)
		}
	}
	out := blankLines.ReplaceAllString(strings.Join(kept, "\n"), "\n\n")

	if msg.Link != "" && !strings.Contains(out, msg.Link) {
		out = strings.TrimSpace(out) + "\n\n" + msg.Link
	}
	return strings.TrimSpace(out)
}
