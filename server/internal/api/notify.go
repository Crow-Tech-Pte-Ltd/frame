package api

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"net/url"
	"strings"
	"time"

	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/events"
	"github.com/Crow-Tech-Pte-Ltd/frame/server/internal/push"
)

// emit publishes one SSE event to each user.
func (s *Server) emit(typ string, data any, userIDs ...string) {
	ev, err := events.New(typ, data)
	if err != nil {
		s.log.Error("event encode failed", "type", typ, "err", err)
		return
	}
	s.hub.PublishMany(userIDs, ev)
}

// notify queues a push (never blocks the request).
func (s *Server) notify(n push.Notification) {
	if n.Payload.V == 0 {
		n.Payload.V = 1
	}
	if n.Payload.CreatedAt == 0 {
		n.Payload.CreatedAt = s.now().UnixMilli()
	}
	n.Payload.Title = push.Truncate(n.Payload.Title, 64)
	n.Payload.Body = push.Truncate(n.Payload.Body, 200)
	s.push.Notify(n)
}

func strp(s string) *string { return &s }

// Mailer sends plain-text email (password reset codes).
type Mailer interface {
	Send(ctx context.Context, to, subject, body string) error
}

// smtpMailer sends via smtps:// (implicit TLS) or smtp:// (STARTTLS required).
type smtpMailer struct {
	url  string
	from string
}

func (m *smtpMailer) Send(ctx context.Context, to, subject, body string) error {
	u, err := url.Parse(m.url)
	if err != nil {
		return err
	}
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = "587"
		if u.Scheme == "smtps" {
			port = "465"
		}
	}
	addr := net.JoinHostPort(host, port)
	d := net.Dialer{Timeout: 15 * time.Second}
	var conn net.Conn
	tlsCfg := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	if u.Scheme == "smtps" {
		conn, err = tls.DialWithDialer(&d, "tcp", addr, tlsCfg)
	} else {
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return err
	}
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return err
	}
	defer c.Close()
	if u.Scheme == "smtp" {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("smtp: server does not support STARTTLS")
		}
		if err := c.StartTLS(tlsCfg); err != nil {
			return err
		}
	}
	if u.User != nil {
		pass, _ := u.User.Password()
		if err := c.Auth(smtp.PlainAuth("", u.User.Username(), pass, host)); err != nil {
			return err
		}
	}
	if err := c.Mail(m.from); err != nil {
		return err
	}
	if err := c.Rcpt(to); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	clean := func(s string) string { return strings.NewReplacer("\r", "", "\n", "").Replace(s) }
	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\nDate: %s\r\n\r\n%s\r\n",
		clean(m.from), clean(to), clean(subject), time.Now().UTC().Format(time.RFC1123Z), strings.ReplaceAll(body, "\n", "\r\n"))
	if _, err := w.Write([]byte(msg)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}
