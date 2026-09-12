package mail_test

import (
	"strings"
	"testing"
	"time"

	"goblog/mail"
)

func TestMessage_HeadersAndCRLF(t *testing.T) {
	now := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	msg, err := mail.Message("blog@example.com", "reader@example.com", "Your code", "line one\nline two\n", now)
	if err != nil {
		t.Fatal(err)
	}
	got := string(msg)
	headers, body, found := strings.Cut(got, "\r\n\r\n")
	if !found {
		t.Fatalf("expected a blank line between headers and body:\n%s", got)
	}
	for _, want := range []string{
		"From: blog@example.com\r\n",
		"To: reader@example.com\r\n",
		"Subject: Your code\r\n",
		"Date: Sat, 12 Sep 2026 10:00:00 +0000\r\n",
		"MIME-Version: 1.0\r\n",
		"Content-Type: text/plain; charset=utf-8\r\n",
	} {
		if !strings.Contains(headers+"\r\n", want) {
			t.Errorf("missing header %q in:\n%s", want, headers)
		}
	}
	if body != "line one\r\nline two\r\n" {
		t.Errorf("body must use CRLF, got %q", body)
	}
	if strings.Contains(got, "\r\n\r\n\r\n") || strings.Contains(strings.ReplaceAll(got, "\r\n", ""), "\n") {
		t.Errorf("bare LF or doubled CRLF in message: %q", got)
	}
}

func TestMessage_NonASCIISubjectIsEncoded(t *testing.T) {
	msg, err := mail.Message("a@example.com", "b@example.com", "Your Café login code", "x", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(msg), "Subject: =?utf-8?q?") {
		t.Errorf("expected RFC 2047 encoded subject, got:\n%s", msg)
	}
}

func TestMessage_RejectsHeaderInjection(t *testing.T) {
	for _, bad := range []string{"x\r\nBcc: victim@example.com", "x\nBcc: y"} {
		if _, err := mail.Message("a@example.com", bad, "s", "b", time.Now()); err == nil {
			t.Errorf("expected error for To=%q", bad)
		}
		if _, err := mail.Message("a@example.com", "b@example.com", bad, "b", time.Now()); err == nil {
			t.Errorf("expected error for Subject=%q", bad)
		}
		if _, err := mail.Message(bad, "b@example.com", "s", "b", time.Now()); err == nil {
			t.Errorf("expected error for From=%q", bad)
		}
	}
}

func TestNewSMTPSenderFromEnv(t *testing.T) {
	clear := func(t *testing.T) {
		for _, k := range []string{"smtp_host", "smtp_port", "smtp_user", "smtp_password", "smtp_from"} {
			t.Setenv(k, "")
		}
	}

	t.Run("unset", func(t *testing.T) {
		clear(t)
		if s, ok := mail.NewSMTPSenderFromEnv(); ok || s != nil {
			t.Fatalf("expected not configured, got %+v %v", s, ok)
		}
	})
	t.Run("host without from", func(t *testing.T) {
		clear(t)
		t.Setenv("smtp_host", "smtp.example.com")
		if _, ok := mail.NewSMTPSenderFromEnv(); ok {
			t.Fatal("smtp_from is required")
		}
	})
	t.Run("defaults port to 587", func(t *testing.T) {
		clear(t)
		t.Setenv("smtp_host", " smtp.example.com ")
		t.Setenv("smtp_from", "blog@example.com")
		s, ok := mail.NewSMTPSenderFromEnv()
		if !ok {
			t.Fatal("expected configured")
		}
		if s.Host != "smtp.example.com" || s.Port != "587" || s.From != "blog@example.com" || s.User != "" {
			t.Fatalf("unexpected sender %+v", s)
		}
	})
	t.Run("all keys", func(t *testing.T) {
		clear(t)
		t.Setenv("smtp_host", "smtp.example.com")
		t.Setenv("smtp_port", "465")
		t.Setenv("smtp_user", "u")
		t.Setenv("smtp_password", "p")
		t.Setenv("smtp_from", "blog@example.com")
		s, _ := mail.NewSMTPSenderFromEnv()
		want := mail.SMTPSender{Host: "smtp.example.com", Port: "465", User: "u", Password: "p", From: "blog@example.com"}
		if *s != want {
			t.Fatalf("want %+v, got %+v", want, *s)
		}
	})
}
