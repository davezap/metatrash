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

func sendConfirmMail(ctx context.Context, cfg accountConfig, password, email, code string) error {
	body := "Your Metatrash confirmation code is: " + code + "\n\nYou asked to confirm it’s you before changing how you sign in. Enter it on Your account at " + cfg.Origin + "/account/security.\nThis code expires in 10 minutes and can be used once.\nDo not share this code. If you did not request it, someone may be signed in to your account: sign in yourself and review your sign-in methods.\n"
	return sendMail(ctx, cfg, password, email, "Your Metatrash confirmation code", body)
}

func sendEmailChangeMail(ctx context.Context, cfg accountConfig, password, email, code string) error {
	body := "Your Metatrash code to confirm this email address is: " + code + "\n\nYou asked to make this the email address of your Metatrash account. Enter the code on Security at " + cfg.Origin + "/account/security.\nThis code expires in 10 minutes and can be used once.\nDo not share this code. If you did not ask for this, ignore this email: nothing changes unless the code is entered.\n"
	return sendMail(ctx, cfg, password, email, "Confirm your new Metatrash email address", body)
}

// emailChangeTakenMail is sent instead of a code when someone asks to move
// their account to an address that already has an account.
func emailChangeTakenMail(origin string) string {
	return "Someone signed in to Metatrash asked to change their account's email address to this address. This address already has its own Metatrash account, so no code was sent and nothing changed.\n\n" +
		"If it was you, sign in with this address at " + origin + "/login instead. An address can belong to only one account.\n" +
		"If it was not you, there is nothing to do: your account is unchanged.\n"
}

// emailChangedOldMail is the notice sent to the previous address.
func emailChangedOldMail(origin, from, to string, when time.Time) string {
	return "The email address of your Metatrash account was changed from " + from + " to " + to + ".\n" +
		"When: " + when.UTC().Format("2 January 2006 15:04 UTC") + "\n\n" +
		"This address no longer signs in to the account or receives its notices.\n\n" +
		"If this was you, there is nothing to do.\n" +
		"If it was not, someone is signed in to your account. Sign in at " + origin + "/login with a passkey, your authenticator app or a recovery code (using the new address), change the address back on Security and review your sign-in methods. If you cannot sign in, reply to this email.\n"
}

// emailChangedNewMail is the notice sent to the new address.
func emailChangedNewMail(origin, from, to string, when time.Time) string {
	return "This is now the email address of your Metatrash account (it was " + from + ").\n" +
		"When: " + when.UTC().Format("2 January 2006 15:04 UTC") + "\n\n" +
		"Sign in at " + origin + "/login with " + to + ". Notices about your account and invitations sent to this address come here.\n"
}

// signInMethodMail is the notice sent when a sign-in method is added or
// removed. name is user-chosen and goes only in the body, stripped of controls.
func signInMethodMail(origin, change, name string, when time.Time) string {
	return "A sign-in method was " + change + " on your Metatrash account: " + mailText(name) + "\n" +
		"When: " + when.UTC().Format("2 January 2006 15:04 UTC") + "\n\n" +
		"If this was you, there is nothing to do.\n" +
		"If it was not, sign in at " + origin + "/login, review Sign-in methods on Your account and remove anything you do not recognise.\n"
}

// recoveryUsedMail is the notice sent when a recovery code signs in.
func recoveryUsedMail(origin string, left int, when time.Time) string {
	count := strconv.Itoa(left) + " recovery codes are left."
	if left == 1 {
		count = "1 recovery code is left."
	}
	return "A recovery code was used to sign in to your Metatrash account.\n" +
		"When: " + when.UTC().Format("2 January 2006 15:04 UTC") + "\n" + count + "\n\n" +
		"If this was you, check your passkeys and authenticator app on Security at " + origin + "/account/security, and create new recovery codes there if you are running low.\n" +
		"If it was not, someone has one of your recovery codes: sign in, create new recovery codes (the old ones then stop working) and review your sign-in methods.\n"
}

// emailLoginOffMail is sent instead of a login code when the account has
// turned email sign-in off.
func emailLoginOffMail(origin string) string {
	return "Someone asked for a Metatrash login code for this address, but you have turned off sign-in by emailed code, so no code was sent.\n\n" +
		"To sign in, go to " + origin + "/login and use a passkey, your authenticator app or a recovery code.\n" +
		"If you did not ask for a code, there is nothing to do: your account is unchanged.\n"
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
		"2. Open " + origin + "/account and choose Accept invitation at the top of Spaces. You do not need to choose a username or create a space.\n\n" +
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
