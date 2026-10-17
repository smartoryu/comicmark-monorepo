package mailer

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/mail"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- test doubles -----------------------------------------------------------

// recordingMailer captures messages instead of delivering them.
type recordingMailer struct {
	mu       sync.Mutex
	received []Message
	// block, when non-nil, makes Send wait until it is closed.
	block chan struct{}
	err   error
}

func (r *recordingMailer) Send(_ context.Context, msg Message) error {
	if r.block != nil {
		<-r.block
	}
	if r.err != nil {
		return r.err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.received = append(r.received, msg)
	return nil
}

func (r *recordingMailer) messages() []Message {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Message(nil), r.received...)
}

// --- New --------------------------------------------------------------------

func TestNewRejectsInvalidOptions(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)

	for name, opts := range map[string]Options{
		"unknown driver":    {Driver: "carrier-pigeon", From: "a@b.com", Logger: logger},
		"empty driver":      {Driver: "", From: "a@b.com", Logger: logger},
		"empty from":        {Driver: DriverConsole, From: "", Logger: logger},
		"malformed from":    {Driver: DriverConsole, From: "not-an-address", Logger: logger},
		"smtp bad port":     {Driver: DriverSMTP, From: "a@b.com", SMTPHost: "localhost", SMTPPort: 0, Logger: logger},
		"smtp missing host": {Driver: DriverSMTP, From: "a@b.com", SMTPPort: 1025, Logger: logger},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := New(opts)
			require.Error(t, err)
		})
	}
}

// Credentials must never be sent unencrypted to a remote host.
func TestNewRejectsPlaintextAuthToRemoteHost(t *testing.T) {
	_, err := New(Options{
		Driver:   DriverSMTP,
		From:     "ComicMark <no-reply@comicmark.local>",
		SMTPHost: "smtp.example.com",
		SMTPPort: 587,
		SMTPUser: "user",
		SMTPPass: "pass",
		// STARTTLS deliberately off
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "in the clear")

	// STARTTLS makes it acceptable.
	_, err = New(Options{
		Driver:       DriverSMTP,
		From:         "ComicMark <no-reply@comicmark.local>",
		SMTPHost:     "smtp.example.com",
		SMTPPort:     587,
		SMTPUser:     "user",
		SMTPPass:     "pass",
		SMTPStartTLS: true,
	})
	require.NoError(t, err)
}

func TestConsoleDriverLogsGreppableLink(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	m, err := New(Options{Driver: DriverConsole, From: "ComicMark <no-reply@comicmark.local>", Logger: logger})
	require.NoError(t, err)

	msg, err := VerificationMessage("reader@example.com", ActionData{
		DisplayName: "Risf",
		Link:        "http://localhost:5173/?verify_token=abc123",
		ExpiresIn:   "24 hours",
	})
	require.NoError(t, err)
	require.NoError(t, m.Send(context.Background(), msg))

	out := buf.String()
	// slog quotes values containing characters like '?' and '=', so match the
	// attribute rather than assuming an unquoted form. scripts/e2e.sh uses the
	// same tolerant pattern.
	linkPattern := regexp.MustCompile(`link="?http://localhost:5173/\?verify_token=abc123"?`)
	assert.Regexp(t, linkPattern, out,
		"the console driver must emit a greppable link= attribute for scripts")
	assert.Contains(t, out, "reader@example.com")
	assert.Contains(t, out, "Confirm your ComicMark email address")
}

// --- message building -------------------------------------------------------

func TestVerificationMessageRendersBothAlternatives(t *testing.T) {
	const link = "https://app.example.com/?verify_token=tok-1"

	msg, err := VerificationMessage("reader@example.com", ActionData{
		DisplayName: "Risf",
		Link:        link,
		ExpiresIn:   "24 hours",
	})
	require.NoError(t, err)

	assert.Equal(t, "reader@example.com", msg.To)
	assert.Equal(t, link, msg.Link)
	assert.Equal(t, "Confirm your ComicMark email address", msg.Subject)

	assert.Contains(t, msg.HTML, link)
	assert.Contains(t, msg.HTML, "Risf")
	assert.Contains(t, msg.HTML, "24 hours")

	assert.Contains(t, msg.Text, link)
	assert.Contains(t, msg.Text, "Risf")
	assert.NotContains(t, msg.Text, "<", "the text alternative must not contain markup")
}

func TestPasswordResetMessageWarnsAboutSigningOut(t *testing.T) {
	msg, err := PasswordResetMessage("reader@example.com", ActionData{
		Link:      "https://app.example.com/?reset_token=tok-2",
		ExpiresIn: "1 hour",
	})
	require.NoError(t, err)

	assert.Equal(t, "Reset your ComicMark password", msg.Subject)
	// The greeting falls back when no display name is known.
	assert.Contains(t, msg.HTML, "Hi there")
	assert.Contains(t, msg.Text, "Hi there")
	assert.Contains(t, msg.Text, "signs you out on every device")
}

func TestActionMessagesRequireALink(t *testing.T) {
	_, err := VerificationMessage("reader@example.com", ActionData{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires a link")

	_, err = PasswordResetMessage("reader@example.com", ActionData{})
	require.Error(t, err)
}

func TestSendRejectsInvalidMessage(t *testing.T) {
	m, err := New(Options{Driver: DriverConsole, From: "a@example.com", Logger: slog.New(slog.DiscardHandler)})
	require.NoError(t, err)

	require.Error(t, m.Send(context.Background(), Message{To: "", Subject: "s", Text: "t"}))
	require.Error(t, m.Send(context.Background(), Message{To: "not-an-email", Subject: "s", Text: "t"}))
	require.Error(t, m.Send(context.Background(), Message{To: "a@example.com", Subject: "", Text: "t"}))

	// An HTML-only message is accepted, with the text alternative derived.
	require.NoError(t, m.Send(context.Background(), Message{
		To: "a@example.com", Subject: "s",
		HTML: `<p>Hello</p><p><a href="https://example.com/x">Go</a></p>`,
	}))
}

func TestPlainTextFallbackStripsMarkupAndKeepsLink(t *testing.T) {
	got := plainTextFallback(Message{
		HTML: `<html><head><style>p{color:red}</style></head><body>
			<p>Hello <strong>world</strong></p>
			<script>alert('x')</script>
			<p><a href="https://example.com/go">Open</a></p>
			</body></html>`,
		Link: "https://example.com/go",
	})

	assert.Contains(t, got, "Hello world")
	assert.Contains(t, got, "Open")
	assert.Contains(t, got, "https://example.com/go")
	assert.NotContains(t, got, "<p>")
	assert.NotContains(t, got, "color:red", "style content must be dropped")
	assert.NotContains(t, got, "alert", "script content must be dropped")
}

// --- async wrapper ----------------------------------------------------------

func TestAsyncDeliversQueuedMessagesOnShutdown(t *testing.T) {
	rec := &recordingMailer{}
	a := Async(rec, slog.New(slog.DiscardHandler), 8)

	for i := range 3 {
		require.NoError(t, a.Send(context.Background(), Message{
			To: "reader@example.com", Subject: fmt.Sprintf("subject %d", i), Text: "body",
		}))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, a.Shutdown(ctx))

	assert.Len(t, rec.messages(), 3)
}

func TestAsyncNeverBlocksTheCallerWhenTheQueueIsFull(t *testing.T) {
	release := make(chan struct{})
	rec := &recordingMailer{block: release}

	var buf bytes.Buffer
	a := Async(rec, slog.New(slog.NewTextHandler(&buf, nil)), 1).(*asyncMailer)
	t.Cleanup(func() {
		close(release)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = a.Shutdown(ctx)
	})

	// The worker is blocked on the first message, so the queue holds one more
	// and everything after that is dropped.
	for range 6 {
		require.NoError(t, a.Send(context.Background(), Message{
			To: "reader@example.com", Subject: "s", Text: "t",
		}), "a full queue must not surface as an error to the request")
	}

	assert.Positive(t, a.Dropped())
	assert.Contains(t, buf.String(), "queue full")
}

func TestAsyncRejectsSendAfterShutdown(t *testing.T) {
	a := Async(&recordingMailer{}, slog.New(slog.DiscardHandler), 4)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, a.Shutdown(ctx))

	err := a.Send(context.Background(), Message{To: "a@example.com", Subject: "s", Text: "t"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "closed")
}

// --- smtp driver against a real (in-process) SMTP conversation --------------

// fakeSMTPServer speaks just enough SMTP for net/smtp to deliver a message.
type fakeSMTPServer struct {
	addr     string
	mu       sync.Mutex
	messages []string
	received chan struct{}
}

func startFakeSMTPServer(t *testing.T) *fakeSMTPServer {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	s := &fakeSMTPServer{addr: ln.Addr().String(), received: make(chan struct{}, 8)}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				s.serve(conn)
			}()
		}
	}()

	t.Cleanup(func() {
		_ = ln.Close()
		wg.Wait()
	})

	return s
}

func (s *fakeSMTPServer) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	r := bufio.NewReader(conn)
	w := bufio.NewWriter(conn)
	reply := func(line string) bool {
		if _, err := w.WriteString(line + "\r\n"); err != nil {
			return false
		}
		return w.Flush() == nil
	}

	if !reply("220 fake.local SMTP ready") {
		return
	}

	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))

		switch {
		case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
			if !reply("250-fake.local") || !reply("250-8BITMIME") || !reply("250 OK") {
				return
			}
		case strings.HasPrefix(cmd, "MAIL FROM"), strings.HasPrefix(cmd, "RCPT TO"), strings.HasPrefix(cmd, "RSET"):
			if !reply("250 OK") {
				return
			}
		case strings.HasPrefix(cmd, "DATA"):
			if !reply("354 End data with <CR><LF>.<CR><LF>") {
				return
			}
			var body strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if strings.TrimRight(l, "\r\n") == "." {
					break
				}
				body.WriteString(l)
			}
			s.mu.Lock()
			s.messages = append(s.messages, body.String())
			s.mu.Unlock()
			select {
			case s.received <- struct{}{}:
			default:
			}
			if !reply("250 OK: queued") {
				return
			}
		case strings.HasPrefix(cmd, "QUIT"):
			_ = reply("221 Bye")
			return
		default:
			if !reply("250 OK") {
				return
			}
		}
	}
}

func (s *fakeSMTPServer) lastMessage(t *testing.T) string {
	t.Helper()
	select {
	case <-s.received:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the message to be delivered")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	require.NotEmpty(t, s.messages)
	return s.messages[len(s.messages)-1]
}

func TestSMTPDriverDeliversMultipartMessage(t *testing.T) {
	srv := startFakeSMTPServer(t)
	host, portStr, err := net.SplitHostPort(srv.addr)
	require.NoError(t, err)

	var port int
	_, err = fmt.Sscanf(portStr, "%d", &port)
	require.NoError(t, err)

	m, err := New(Options{
		Driver:      DriverSMTP,
		From:        "ComicMark <no-reply@comicmark.local>",
		SMTPHost:    host,
		SMTPPort:    port,
		SMTPTimeout: 5 * time.Second,
		Logger:      slog.New(slog.DiscardHandler),
	})
	require.NoError(t, err)

	msg, err := VerificationMessage("reader@example.com", ActionData{
		DisplayName: "Risf",
		Link:        "https://app.example.com/?verify_token=tok-3",
		ExpiresIn:   "24 hours",
	})
	require.NoError(t, err)
	require.NoError(t, m.Send(context.Background(), msg))

	raw := srv.lastMessage(t)

	// mail.Address.String() quotes the display name; that quoting is valid
	// RFC 5322 and is what keeps odd display names safe.
	assert.Contains(t, raw, `From: "ComicMark" <no-reply@comicmark.local>`)
	assert.Contains(t, raw, "To: <reader@example.com>")
	assert.Contains(t, raw, "Subject: Confirm your ComicMark email address")
	assert.Contains(t, raw, "MIME-Version: 1.0")
	assert.Contains(t, raw, "multipart/alternative")
	assert.Contains(t, raw, "text/plain; charset=utf-8")
	assert.Contains(t, raw, "text/html; charset=utf-8")
	assert.Contains(t, raw, "https://app.example.com/?verify_token=tok-3")
	// Every line must end with CRLF, which is what the SMTP DATA phase requires.
	assert.NotContains(t, strings.ReplaceAll(raw, "\r\n", ""), "\n")
}

func TestSMTPDriverPreventsHeaderInjection(t *testing.T) {
	// A newline in a header value must not be able to start a new header.
	msg := Message{
		To:      "reader@example.com",
		Subject: "Hello\r\nBcc: attacker@example.com",
		Text:    "body",
	}
	m := &smtpMailer{from: mustAddress(t, "ComicMark <no-reply@comicmark.local>"), logger: slog.New(slog.DiscardHandler)}

	rendered := string(m.render(msg, mustAddress(t, "reader@example.com")))

	// The value may still contain the text "Bcc:" inside an encoded word, which
	// is inert; what matters is that no header line was created.
	for _, line := range strings.Split(rendered, "\r\n") {
		assert.False(t, strings.HasPrefix(line, "Bcc:"),
			"header injection must be impossible, got line %q", line)
	}

	// A recipient name containing a newline is the other injection route.
	injected := Message{
		To:      "reader@example.com",
		Subject: "hi",
		Text:    "body",
	}
	rendered = string(m.render(injected, &mail.Address{
		Name:    "Reader\r\nBcc: attacker@example.com",
		Address: "reader@example.com",
	}))
	for _, line := range strings.Split(rendered, "\r\n") {
		assert.False(t, strings.HasPrefix(line, "Bcc:"),
			"a crafted display name must not inject a header, got line %q", line)
	}
}

func mustAddress(t *testing.T, s string) *mail.Address {
	t.Helper()
	a, err := parseFrom(s)
	require.NoError(t, err)
	return a
}

func TestNewSMTPDefaultsTimeout(t *testing.T) {
	m, err := New(Options{
		Driver:   DriverSMTP,
		From:     "a@example.com",
		SMTPHost: "localhost",
		SMTPPort: 1025,
		Logger:   slog.New(slog.DiscardHandler),
	})
	require.NoError(t, err)

	sm, ok := m.(*smtpMailer)
	require.True(t, ok)
	assert.Equal(t, 10*time.Second, sm.timeout)
}

func TestAsyncPropagatesRecipientValidationErrors(t *testing.T) {
	a := Async(&recordingMailer{}, slog.New(slog.DiscardHandler), 4)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = a.Shutdown(ctx)
	}()

	err := a.Send(context.Background(), Message{To: "", Subject: "s", Text: "t"})
	require.Error(t, err)
	assert.False(t, errors.Is(err, context.Canceled))
}
