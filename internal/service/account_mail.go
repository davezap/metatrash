package service

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strconv"
	"time"
)

// Always require STARTTLS and verify the server certificate before sending credentials.
func sendLoginMail(ctx context.Context, cfg accountConfig, password, email, code string) error {
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
	messageID, err := randomHex(16)
	if err != nil {
		return err
	}
	message := "From: Metatrash <" + cfg.SMTPFrom + ">\r\nTo: <" + email + ">\r\nSubject: Your Metatrash login code\r\nDate: " + time.Now().UTC().Format(time.RFC1123Z) + "\r\nMessage-ID: <" + messageID + "@" + cfg.SMTPHost + ">\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: 7bit\r\n\r\nYour Metatrash login code is: " + code + "\r\n\r\nEnter it in the browser where you requested it at " + cfg.Origin + "/login.\r\nThis code expires in 10 minutes and can be used once.\r\nDo not share this code. If you did not request it, ignore this email.\r\n"
	if _, err = writer.Write([]byte(message)); err != nil {
		return err
	}
	if err = writer.Close(); err != nil {
		return err
	}
	// DATA acceptance confirms delivery to the relay; a failed QUIT must not discard a valid code.
	_ = client.Quit()
	return nil
}
