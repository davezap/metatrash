package service

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"mime/quotedprintable"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

func sendLoginMail(ctx context.Context, cfg accountConfig, password, email, code string) error {
	body := "Your Metatrash login code is: " + code + "\n\nEnter it in the browser where you requested it at " + cfg.Origin + "/login.\nThis code expires in 10 minutes and can be used once.\nDo not share this code. If you did not request it, ignore this email.\n"
	return sendMail(ctx, cfg, password, email, "Your Metatrash login code", body)
}

// invitationMail is the invitation email. The subject is fixed ASCII; owner
// and space names go only in the quoted-printable body, stripped of controls.
type invitationMail struct {
	Owner, SpaceName, SpaceAddress, Email string
	Expires                               time.Time
}

func (m invitationMail) body(origin string) string {
	owner := mailText(m.Owner)
	if owner == "" {
		owner = "A Metatrash user"
	}
	return owner + " invited you to their Metatrash space \"" + mailText(m.SpaceName) + "\" (" + mailText(m.SpaceAddress) + ").\n\n" +
		"To accept:\n" +
		"1. Sign in at " + origin + "/login with this email address (" + m.Email + "). If you do not have an account yet, signing in creates one.\n" +
		"2. On Your account, choose Accept under Invitations.\n\n" +
		"The invitation expires on " + m.Expires.UTC().Format("2 January 2006 15:04 UTC") + ".\n" +
		"Members browse the space read-only and can connect their own AI apps to it.\n\n" +
		"If you were not expecting this, ignore this email. Nothing changes unless you accept.\n"
}

// mailText removes control characters (including CR and LF) from names that
// users chose, so they cannot break lines or add headers.
func mailText(value string) string {
	return strings.Map(func(c rune) rune {
		if c < 32 || c == 127 || (c >= 0x80 && c < 0xa0) || c == 0x2028 || c == 0x2029 {
			return -1
		}
		return c
	}, value)
}

// Always require STARTTLS and verify the server certificate before sending credentials.
// subject must be fixed ASCII text; body is UTF-8 and sent quoted-printable.
func sendMail(ctx context.Context, cfg accountConfig, password, email, subject, body string) error {
	if strings.ContainsAny(subject, "\r\n") || strings.ContainsAny(email, "\r\n<>") {
		return fmt.Errorf("invalid mail header")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	address := net.JoinHostPort(cfg.SMTPHost, strconv.Itoa(cfg.SMTPPort))
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
	if err != nil {
		return err
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	client, err := smtp.NewClient(conn, cfg.SMTPHost)
	if err != nil {
		return err
	}
	defer client.Close()
	if ok, _ := client.Extension("STARTTLS"); !ok {
		return fmt.Errorf("SMTP server requires STARTTLS support")
	}
	if err := client.StartTLS(&tls.Config{ServerName: cfg.SMTPHost, MinVersion: tls.VersionTLS12}); err != nil {
		return err
	}
	if err := client.Auth(smtp.PlainAuth("", cfg.SMTPUsername, password, cfg.SMTPHost)); err != nil {
		return err
	}
	if err := client.Mail(cfg.SMTPFrom); err != nil {
		return err
	}
	if err := client.Rcpt(email); err != nil {
		return err
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	message, err := mailMessage(cfg, email, subject, body)
	if err != nil {
		return err
	}
	if _, err = writer.Write(message); err != nil {
		return err
	}
	if err = writer.Close(); err != nil {
		return err
	}
	// DATA acceptance confirms delivery to the relay; a failed QUIT must not discard a valid send.
	_ = client.Quit()
	return nil
}

func mailMessage(cfg accountConfig, email, subject, body string) ([]byte, error) {
	messageID, err := randomHex(16)
	if err != nil {
		return nil, err
	}
	var encoded bytes.Buffer
	qp := quotedprintable.NewWriter(&encoded)
	if _, err := qp.Write([]byte(strings.ReplaceAll(body, "\n", "\r\n"))); err != nil {
		return nil, err
	}
	if err := qp.Close(); err != nil {
		return nil, err
	}
	header := "From: Metatrash <" + cfg.SMTPFrom + ">\r\nTo: <" + email + ">\r\nSubject: " + subject + "\r\nDate: " + time.Now().UTC().Format(time.RFC1123Z) + "\r\nMessage-ID: <" + messageID + "@" + cfg.SMTPHost + ">\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n"
	return append([]byte(header), encoded.Bytes()...), nil
}
