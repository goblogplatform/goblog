// Package mail sends plain-text email over SMTP. Auth uses it to deliver
// one-time login codes; the Sender interface lets tests substitute a fake.
package mail

import (
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"os"
	"strings"
	"time"
)

// Sender delivers a plain-text email.
type Sender interface {
	Send(to, subject, body string) error
}

// SMTPSender sends through a single SMTP server. Port 465 uses implicit TLS;
// any other port uses STARTTLS when the server offers it. PLAIN auth is used
// only when User is set, so an unauthenticated local relay works.
type SMTPSender struct {
	Host     string
	Port     string
	User     string
	Password string
	From     string
}

// NewSMTPSenderFromEnv builds a sender from smtp_host, smtp_port (default
// 587), smtp_user, smtp_password and smtp_from. ok is false when smtp_host or
// smtp_from is unset, meaning email login is not configured.
func NewSMTPSenderFromEnv() (*SMTPSender, bool) {
	host := strings.TrimSpace(os.Getenv("smtp_host"))
	from := strings.TrimSpace(os.Getenv("smtp_from"))
	if host == "" || from == "" {
		return nil, false
	}
	port := strings.TrimSpace(os.Getenv("smtp_port"))
	if port == "" {
		port = "587"
	}
	return &SMTPSender{
		Host:     host,
		Port:     port,
		User:     os.Getenv("smtp_user"),
		Password: os.Getenv("smtp_password"),
		From:     from,
	}, true
}

// Message renders an RFC 5322 plain-text message with CRLF line endings.
// Header values containing a line break are rejected (header injection).
func Message(from, to, subject, body string, now time.Time) ([]byte, error) {
	for _, v := range []string{from, to, subject} {
		if strings.ContainsAny(v, "\r\n") {
			return nil, errors.New("mail: header value contains a line break")
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", to)
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", subject))
	fmt.Fprintf(&b, "Date: %s\r\n", now.Format(time.RFC1123Z))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("\r\n")
	b.WriteString(strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\n", "\r\n"))
	return []byte(b.String()), nil
}

func (s *SMTPSender) auth() smtp.Auth {
	if s.User == "" {
		return nil
	}
	return smtp.PlainAuth("", s.User, s.Password, s.Host)
}

// Send delivers one message to a single recipient.
func (s *SMTPSender) Send(to, subject, body string) error {
	msg, err := Message(s.From, to, subject, body, time.Now())
	if err != nil {
		return err
	}
	addr := net.JoinHostPort(s.Host, s.Port)
	if s.Port != "465" {
		return smtp.SendMail(addr, s.auth(), s.From, []string{to}, msg)
	}

	conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: s.Host})
	if err != nil {
		return err
	}
	defer conn.Close()
	c, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		return err
	}
	defer c.Close()
	if a := s.auth(); a != nil {
		if err := c.Auth(a); err != nil {
			return err
		}
	}
	if err := c.Mail(s.From); err != nil {
		return err
	}
	if err := c.Rcpt(to); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}
