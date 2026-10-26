package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"io"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"strings"
	"sync"
	"testing"
	"time"
)

// An SMTP server on this machine that takes every email, signing in with
// PLAIN when it offers to, and keeps what it was sent; refusing whoever
// refuse names.
type fakeSMTP struct {
	addr   string
	auth   bool
	refuse string

	mu     sync.Mutex
	logins []string
	mails  []string
}

func newFakeSMTP(t *testing.T, auth bool, refuse string) *fakeSMTP {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	s := &fakeSMTP{addr: l.Addr().String(), auth: auth, refuse: refuse}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go s.serve(c)
		}
	}()
	return s
}

func (s *fakeSMTP) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	say := func(line string) { io.WriteString(c, line+"\r\n") }
	say("220 fake ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		verb := strings.ToUpper(strings.Fields(line + " x")[0])
		switch {
		case verb == "EHLO":
			if s.auth {
				say("250-fake")
				say("250 AUTH PLAIN")
			} else {
				say("250 fake")
			}
		case verb == "AUTH":
			b, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(line, "AUTH PLAIN "))
			s.mu.Lock()
			s.logins = append(s.logins, string(b))
			s.mu.Unlock()
			say("235 ok")
		case verb == "MAIL":
			say("250 ok")
		case verb == "RCPT":
			if s.refuse != "" && strings.Contains(line, s.refuse) {
				say("550 no such mailbox")
				continue
			}
			say("250 ok")
		case verb == "DATA":
			say("354 go on")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(strings.TrimPrefix(l, "."))
			}
			s.mu.Lock()
			s.mails = append(s.mails, b.String())
			s.mu.Unlock()
			say("250 queued")
		case verb == "QUIT":
			say("221 bye")
			return
		default:
			say("250 ok")
		}
	}
}

func testMailer(t *testing.T, s *fakeSMTP, username string) *mailer {
	host, port, _ := net.SplitHostPort(s.addr)
	return &mailer{host: host, port: port, username: username, password: "pw", from: `"Spoletta" <spoletta@example.com>`, sender: "spoletta@example.com"}
}

// An email arrives with its subject, whatever it holds, on one line, and its
// text as written.
func TestMailerSends(t *testing.T) {
	s := newFakeSMTP(t, false, "")
	m := testMailer(t, s, "")
	text := "Approve “Payment” waits for your review.\n\nOpen the review: https://example.com/#/flows/1/runs/2/steps/3\n" + strings.Repeat("long ", 40)
	if err := m.send(context.Background(), "Bob <bob@example.org>", "Approve “Payment”\r\nBcc: eve@example.com", text); err != nil {
		t.Fatal(err)
	}
	if len(s.mails) != 1 {
		t.Fatalf("mails = %q", s.mails)
	}
	msg, err := mail.ReadMessage(strings.NewReader(s.mails[0]))
	if err != nil {
		t.Fatal(err)
	}
	if msg.Header.Get("Bcc") != "" {
		t.Errorf("the subject added a header: %v", msg.Header)
	}
	subject, err := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
	if err != nil || subject != "Approve “Payment” Bcc: eve@example.com" {
		t.Errorf("subject = %q (%v)", subject, err)
	}
	if msg.Header.Get("To") != `"Bob" <bob@example.org>` || msg.Header.Get("From") != `"Spoletta" <spoletta@example.com>` {
		t.Errorf("headers = %v", msg.Header)
	}
	body, _ := io.ReadAll(quotedprintable.NewReader(msg.Body))
	// Ending the data ends its last line.
	if got := strings.TrimSuffix(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n"); got != text {
		t.Errorf("text = %q, want %q", got, text)
	}
}

// With a username it signs in, and says what the server refused.
func TestMailerSignsInAndFails(t *testing.T) {
	s := newFakeSMTP(t, true, "nobody@")
	m := testMailer(t, s, "spoletta")
	if err := m.send(context.Background(), "bob@example.org", "Hi", "Hello"); err != nil {
		t.Fatal(err)
	}
	if len(s.logins) != 1 || s.logins[0] != "\x00spoletta\x00pw" {
		t.Errorf("logins = %q", s.logins)
	}
	err := m.send(context.Background(), "nobody@example.org", "Hi", "Hello")
	if err == nil || !strings.Contains(err.Error(), "550") || strings.Contains(err.Error(), "pw") {
		t.Errorf("refused: %v", err)
	}
	if err := m.send(context.Background(), "not an address", "Hi", "Hello"); err == nil {
		t.Errorf("sent to what isn't an address")
	}

	// A server taking no sign-in isn't sent the password.
	plain := newFakeSMTP(t, false, "")
	if err := testMailer(t, plain, "spoletta").send(context.Background(), "bob@example.org", "Hi", "Hello"); err == nil || !strings.Contains(err.Error(), "takes no sign-in") {
		t.Errorf("no sign-in offered: %v", err)
	}
}

func TestMailMessageHeaders(t *testing.T) {
	at := time.Date(2026, 10, 26, 10, 4, 0, 0, time.UTC)
	msg := string(mailMessage(`"Spoletta" <spoletta@example.com>`, "bob@example.org", "Hello", "Hi", at))
	for _, want := range []string{"Date: Mon, 26 Oct 2026 10:04:00 +0000\r\n", "Auto-Submitted: auto-generated\r\n", "@example.com>\r\n", "Subject: Hello\r\n"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q:\n%s", want, msg)
		}
	}
}
