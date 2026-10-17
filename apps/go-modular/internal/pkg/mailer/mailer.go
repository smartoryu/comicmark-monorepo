// Package mailer sends transactional email for account verification and
// password reset.
//
// Two drivers exist:
//
//   - "console" renders the message and writes it to the log, so local
//     development and the end-to-end script need no mail server at all. It logs
//     the action link in a stable, greppable form (link=...).
//   - "smtp" delivers over SMTP, optionally upgrading with STARTTLS.
//
// Delivery failures are never returned to the caller through the Async wrapper:
// a slow or unreachable mail server must not fail an HTTP request, and users can
// always ask for another link.
package mailer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Driver names accepted by New.
const (
	DriverConsole = "console"
	DriverSMTP    = "smtp"
)

// Message is a single transactional email. HTML and Text are alternative
// renderings of the same content; Link is the primary call to action, surfaced
// by the console driver so scripts can follow it without parsing HTML.
type Message struct {
	To      string
	Subject string
	HTML    string
	Text    string
	Link    string
}

// Mailer delivers messages.
type Mailer interface {
	Send(ctx context.Context, msg Message) error
}

// AsyncMailer is a Mailer that also needs shutting down to flush its queue.
type AsyncMailer interface {
	Mailer
	// Shutdown stops accepting messages and waits for queued ones to be
	// delivered, or until ctx is done.
	Shutdown(ctx context.Context) error
}

// Options configures a driver.
type Options struct {
	// Driver is DriverConsole or DriverSMTP.
	Driver string
	// From is the RFC 5322 sender, e.g. `ComicMark <no-reply@example.com>`.
	From string

	SMTPHost     string
	SMTPPort     int
	SMTPUser     string
	SMTPPass     string
	SMTPStartTLS bool
	SMTPTimeout  time.Duration

	Logger *slog.Logger
}

// New returns the driver selected by Options.Driver.
func New(opts Options) (Mailer, error) {
	from, err := parseFrom(opts.From)
	if err != nil {
		return nil, err
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	switch strings.ToLower(strings.TrimSpace(opts.Driver)) {
	case DriverConsole:
		return &consoleMailer{from: from, logger: logger}, nil
	case DriverSMTP:
		if opts.SMTPHost == "" {
			return nil, errors.New("mailer: SMTPHost is required for the smtp driver")
		}
		if opts.SMTPPort < 1 || opts.SMTPPort > 65535 {
			return nil, fmt.Errorf("mailer: SMTPPort %d is not a valid port", opts.SMTPPort)
		}
		timeout := opts.SMTPTimeout
		if timeout <= 0 {
			timeout = 10 * time.Second
		}
		if opts.SMTPUser != "" && !opts.SMTPStartTLS && !isLocalhost(opts.SMTPHost) {
			// net/smtp refuses to send credentials in the clear to a remote
			// host. Failing here beats failing at the first password reset.
			return nil, fmt.Errorf(
				"mailer: SMTP_USERNAME is set but SMTP_STARTTLS is false and %q is not localhost; "+
					"credentials would be sent in the clear", opts.SMTPHost)
		}
		return &smtpMailer{
			from:     from,
			host:     opts.SMTPHost,
			port:     opts.SMTPPort,
			user:     opts.SMTPUser,
			pass:     opts.SMTPPass,
			startTLS: opts.SMTPStartTLS,
			timeout:  timeout,
			logger:   logger,
		}, nil
	case "":
		return nil, errors.New("mailer: driver must not be empty")
	default:
		return nil, fmt.Errorf("mailer: unknown driver %q (want %q or %q)",
			opts.Driver, DriverConsole, DriverSMTP)
	}
}

// parseFrom validates the configured sender once, at startup.
func parseFrom(from string) (*mail.Address, error) {
	if strings.TrimSpace(from) == "" {
		return nil, errors.New("mailer: From must not be empty")
	}
	addr, err := mail.ParseAddress(from)
	if err != nil {
		return nil, fmt.Errorf("mailer: From %q is not a valid address: %w", from, err)
	}
	return addr, nil
}

func isLocalhost(host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	return h == "localhost" || h == "127.0.0.1" || h == "::1"
}

// enrich validates a message and fills in the plain-text alternative when the
// caller only supplied HTML.
func enrich(msg Message) (Message, error) {
	if strings.TrimSpace(msg.To) == "" {
		return Message{}, errors.New("mailer: recipient must not be empty")
	}
	if _, err := mail.ParseAddress(msg.To); err != nil {
		return Message{}, fmt.Errorf("mailer: recipient %q is not a valid address: %w", msg.To, err)
	}
	if strings.TrimSpace(msg.Subject) == "" {
		return Message{}, errors.New("mailer: subject must not be empty")
	}
	if strings.TrimSpace(msg.Text) == "" {
		// A text alternative is not optional: it is what many clients and spam
		// filters prefer, and it is the only part the console driver can show
		// readably.
		msg.Text = plainTextFallback(msg)
	}
	return msg, nil
}

// defaultQueueSize bounds the async delivery queue.
const defaultQueueSize = 64

// Async wraps a Mailer so Send never blocks on delivery. Messages are handed to
// a single background worker; if the queue fills up they are dropped and logged
// rather than stalling the request that produced them.
func Async(inner Mailer, logger *slog.Logger, queueSize int) AsyncMailer {
	if queueSize <= 0 {
		queueSize = defaultQueueSize
	}
	if logger == nil {
		logger = slog.Default()
	}
	a := &asyncMailer{
		inner:  inner,
		logger: logger,
		queue:  make(chan Message, queueSize),
		done:   make(chan struct{}),
	}
	go a.run()
	return a
}

type asyncMailer struct {
	inner  Mailer
	logger *slog.Logger
	queue  chan Message
	done   chan struct{}

	mu      sync.Mutex
	stopped bool
	// dropped counts messages discarded because the queue was full.
	dropped atomic.Int64
}

// Send enqueues a message. It only returns an error for an invalid message; a
// delivery failure is logged by the worker instead.
func (a *asyncMailer) Send(_ context.Context, msg Message) error {
	msg, err := enrich(msg)
	if err != nil {
		return err
	}

	a.mu.Lock()
	stopped := a.stopped
	a.mu.Unlock()
	if stopped {
		return errors.New("mailer: closed")
	}

	select {
	case a.queue <- msg:
		return nil
	default:
		n := a.dropped.Add(1)
		a.logger.Error("mailer: queue full, email dropped",
			"to", msg.To, "subject", msg.Subject, "total_dropped", n)
		return nil
	}
}

// Shutdown stops the worker after draining queued messages.
func (a *asyncMailer) Shutdown(ctx context.Context) error {
	a.mu.Lock()
	if !a.stopped {
		a.stopped = true
		close(a.queue)
	}
	a.mu.Unlock()

	select {
	case <-a.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Dropped reports how many messages were discarded because the queue was full.
func (a *asyncMailer) Dropped() int64 { return a.dropped.Load() }

func (a *asyncMailer) run() {
	defer close(a.done)

	for msg := range a.queue {
		// A fresh context: the request that produced this message has long
		// since finished, and delivery should not be cancelled with it.
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		err := a.inner.Send(ctx, msg)
		cancel()
		if err != nil {
			a.logger.Error("mailer: delivery failed",
				"to", msg.To, "subject", msg.Subject, "error", err)
		}
	}
}
