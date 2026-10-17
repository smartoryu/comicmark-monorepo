package mailer

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"
)

// smtpMailer delivers messages over SMTP.
type smtpMailer struct {
	from     *mail.Address
	host     string
	port     int
	user     string
	pass     string
	startTLS bool
	timeout  time.Duration
	logger   *slog.Logger
}

// Send delivers the message. The recipient and sender are both taken from
// validated addresses, which also prevents header injection through a crafted
// display name.
func (s *smtpMailer) Send(ctx context.Context, msg Message) error {
	msg, err := enrich(msg)
	if err != nil {
		return err
	}

	to, err := mail.ParseAddress(msg.To)
	if err != nil {
		return fmt.Errorf("mailer: parse recipient: %w", err)
	}

	addr := net.JoinHostPort(s.host, fmt.Sprintf("%d", s.port))

	// A deadline that respects the caller's context: whichever expires first.
	deadline := time.Now().Add(s.timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}

	conn, err := (&net.Dialer{Deadline: deadline}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("mailer: dial %s: %w", addr, err)
	}
	defer func() { _ = conn.Close() }()

	if err := conn.SetDeadline(deadline); err != nil {
		return fmt.Errorf("mailer: set deadline: %w", err)
	}

	client, err := smtp.NewClient(conn, s.host)
	if err != nil {
		return fmt.Errorf("mailer: smtp handshake: %w", err)
	}
	defer func() { _ = client.Close() }()

	if s.startTLS {
		if err := client.StartTLS(&tls.Config{ServerName: s.host, MinVersion: tls.VersionTLS12}); err != nil {
			return fmt.Errorf("mailer: starttls: %w", err)
		}
	}

	if s.user != "" {
		// PlainAuth itself refuses to send credentials over an unencrypted
		// connection to a non-local host; New() rejects that combination
		// up front.
		auth := smtp.PlainAuth("", s.user, s.pass, s.host)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("mailer: authenticate: %w", err)
		}
	}

	if err := client.Mail(s.from.Address); err != nil {
		return fmt.Errorf("mailer: set envelope sender: %w", err)
	}
	if err := client.Rcpt(to.Address); err != nil {
		return fmt.Errorf("mailer: set recipient: %w", err)
	}

	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("mailer: begin message body: %w", err)
	}
	if _, err := w.Write(s.render(msg, to)); err != nil {
		// Closing the writer also aborts the transaction, so surface the write
		// error rather than the follow-up close error.
		_ = w.Close()
		return fmt.Errorf("mailer: write message body: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("mailer: finish message body: %w", err)
	}

	// Quit flushes the transaction; an error here means the server rejected it.
	if err := client.Quit(); err != nil {
		return fmt.Errorf("mailer: quit: %w", err)
	}

	s.logger.DebugContext(ctx, "mailer: delivered", "to", to.Address, "subject", msg.Subject)
	return nil
}

// render produces a MIME multipart/alternative message carrying both the plain
// text and HTML renderings.
func (s *smtpMailer) render(msg Message, to *mail.Address) []byte {
	const boundary = "comicmark-boundary-8f2c1a"

	var b strings.Builder
	writeHeader(&b, "From", s.from.String())
	writeHeader(&b, "To", to.String())
	// Non-ASCII subjects must be encoded, and encoding also prevents a newline
	// in a subject from injecting extra headers.
	writeHeader(&b, "Subject", mime.QEncoding.Encode("utf-8", msg.Subject))
	writeHeader(&b, "Date", time.Now().UTC().Format(time.RFC1123Z))
	writeHeader(&b, "MIME-Version", "1.0")
	writeHeader(&b, "Content-Type", fmt.Sprintf("multipart/alternative; boundary=%q", boundary))

	b.WriteString("\r\n")

	fmt.Fprintf(&b, "--%s\r\n", boundary)
	writeHeader(&b, "Content-Type", "text/plain; charset=utf-8")
	writeHeader(&b, "Content-Transfer-Encoding", "8bit")
	b.WriteString("\r\n")
	// Normalise to CRLF as required by SMTP.
	b.WriteString(crlf(msg.Text))
	b.WriteString("\r\n")

	fmt.Fprintf(&b, "--%s\r\n", boundary)
	writeHeader(&b, "Content-Type", "text/html; charset=utf-8")
	writeHeader(&b, "Content-Transfer-Encoding", "8bit")
	b.WriteString("\r\n")
	b.WriteString(crlf(msg.HTML))
	b.WriteString("\r\n")

	fmt.Fprintf(&b, "--%s--\r\n", boundary)

	return []byte(b.String())
}

// writeHeader appends a single header, stripping CR/LF from the value so a
// crafted value cannot inject additional headers.
func writeHeader(b *strings.Builder, key, value string) {
	value = strings.NewReplacer("\r", "", "\n", "").Replace(value)
	fmt.Fprintf(b, "%s: %s\r\n", key, value)
}

func crlf(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\n", "\r\n")
}
