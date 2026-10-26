package main

// Email, as people are told what waits on them (notify.go): sent over SMTP
// to the server SMTP_HOST names, on SMTP_PORT (587 by default), from
// SMTP_FROM, signing in as SMTP_USERNAME with SMTP_PASSWORD, or the
// password in the file SMTP_PASSWORD_FILE names, when a username is set.
// The connection turns to TLS whenever the server offers STARTTLS, and a
// password is never sent over one that didn't, but to this machine. Without
// SMTP_HOST nobody is emailed.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"
)

// How long sending one email may take, connecting included.
const mailTimeout = time.Minute

type mailer struct {
	host, port         string
	username, password string
	// Who the emails are from, as the From header has it, and the address alone.
	from, sender string
}

// The mailer the environment sets up; nil without SMTP_HOST.
func newMailer() (*mailer, error) {
	host := strings.TrimSpace(env("SMTP_HOST", ""))
	if host == "" {
		return nil, nil
	}
	m := &mailer{host: host, port: env("SMTP_PORT", "587"), username: env("SMTP_USERNAME", "")}
	var err error
	if m.password, err = secretKeyFromEnv("SMTP_PASSWORD"); err != nil {
		return nil, err
	}
	from := env("SMTP_FROM", "")
	if from == "" {
		return nil, errors.New("SMTP_FROM must be set with SMTP_HOST, such as Spoletta <spoletta@example.com>")
	}
	a, err := mail.ParseAddress(from)
	if err != nil {
		return nil, fmt.Errorf("SMTP_FROM isn't an email address: %w", err)
	}
	m.from, m.sender = a.String(), a.Address
	return m, nil
}

// Sends an email to the address, saying text under subject. What goes
// wrong is what the server said, never the password.
func (m *mailer) send(ctx context.Context, to, subject, text string) error {
	ctx, cancel := context.WithTimeout(ctx, mailTimeout)
	defer cancel()
	addr, err := mail.ParseAddress(to)
	if err != nil {
		return fmt.Errorf("%q isn't an email address", to)
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(m.host, m.port))
	if err != nil {
		return fmt.Errorf("SMTP: %w", err)
	}
	deadline, _ := ctx.Deadline()
	conn.SetDeadline(deadline)
	c, err := smtp.NewClient(conn, m.host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("SMTP: %w", err)
	}
	defer c.Close()
	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(&tls.Config{ServerName: m.host}); err != nil {
			return fmt.Errorf("SMTP STARTTLS: %w", err)
		}
	}
	if m.username != "" {
		if ok, _ := c.Extension("AUTH"); !ok {
			return errors.New("SMTP: the server takes no sign-in, but SMTP_USERNAME is set")
		}
		// Refused over a connection in the clear, but to this machine.
		if err := c.Auth(smtp.PlainAuth("", m.username, m.password, m.host)); err != nil {
			return fmt.Errorf("SMTP sign-in: %w", err)
		}
	}
	if err := c.Mail(m.sender); err != nil {
		return fmt.Errorf("SMTP MAIL: %w", err)
	}
	if err := c.Rcpt(addr.Address); err != nil {
		return fmt.Errorf("SMTP RCPT: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("SMTP DATA: %w", err)
	}
	if _, err := w.Write(mailMessage(m.from, addr.String(), subject, text, time.Now())); err != nil {
		return fmt.Errorf("SMTP DATA: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("SMTP DATA: %w", err)
	}
	return c.Quit()
}

// An email as it goes over SMTP: its headers, the subject on one line
// whatever it held, and the text, quoted-printable so any line of it
// arrives whole.
func mailMessage(from, to, subject, text string, at time.Time) []byte {
	subject = strings.Join(strings.Fields(subject), " ")
	id := make([]byte, 12)
	rand.Read(id)
	domain := "localhost"
	if a, err := mail.ParseAddress(from); err == nil {
		if _, d, ok := strings.Cut(a.Address, "@"); ok {
			domain = d
		}
	}
	var b bytes.Buffer
	for _, h := range [][2]string{
		{"From", from},
		{"To", to},
		{"Subject", mime.QEncoding.Encode("utf-8", subject)},
		{"Date", at.Format(time.RFC1123Z)},
		{"Message-ID", "<" + hex.EncodeToString(id) + "@" + domain + ">"},
		{"Auto-Submitted", "auto-generated"},
		{"MIME-Version", "1.0"},
		{"Content-Type", "text/plain; charset=utf-8"},
		{"Content-Transfer-Encoding", "quoted-printable"},
	} {
		b.WriteString(h[0] + ": " + h[1] + "\r\n")
	}
	b.WriteString("\r\n")
	qp := quotedprintable.NewWriter(&b)
	qp.Write([]byte(strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\n", "\r\n")))
	qp.Close()
	return b.Bytes()
}
