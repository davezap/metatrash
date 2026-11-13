package service

import (
	"context"
	"errors"
	"net/textproto"
	"testing"

	"metatrash.com/metatrash/extension"
)

// Extensions send mail through the service's own settings (0.95.27), with
// permanent refusals marked so an outbox stops retrying them.
func TestExtensionSendMail(t *testing.T) {
	var sent []string
	var answer error
	s := &Service{accounts: &accounts{sendNotice: func(_ context.Context, email, subject, body string) error {
		sent = append(sent, email+"|"+subject+"|"+body)
		return answer
	}}}
	var host extension.Host = extensionHost{s}
	mailer, ok := host.(extension.Mailer)
	if !ok {
		t.Fatal("the extension host must be a Mailer")
	}
	ctx := context.Background()
	if err := mailer.SendMail(ctx, " Person@Example.com ", "Your place is ready", "Hello ü"); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 1 || sent[0] != "person@example.com|Your place is ready|Hello ü" {
		t.Fatalf("sent %q", sent)
	}
	for _, c := range []struct{ to, subject string }{
		{"org-1@organisation.invalid", "Hi"},
		{"not an address", "Hi"},
		{"a@example.com", "Line\r\nBcc: x@example.com"},
		{"a@example.com", "Café"},
		{"a@example.com", ""},
	} {
		if err := mailer.SendMail(ctx, c.to, c.subject, "body"); !errors.Is(err, extension.ErrMailRejected) {
			t.Fatalf("%q %q: %v", c.to, c.subject, err)
		}
	}
	if len(sent) != 1 {
		t.Fatalf("refused messages were sent: %q", sent)
	}
	answer = &textproto.Error{Code: 550, Msg: "no such user"}
	if err := mailer.SendMail(ctx, "a@example.com", "Hi", "body"); !errors.Is(err, extension.ErrMailRejected) {
		t.Fatalf("a 5xx answer must be permanent: %v", err)
	}
	answer = &textproto.Error{Code: 451, Msg: "try later"}
	if err := mailer.SendMail(ctx, "a@example.com", "Hi", "body"); err == nil || errors.Is(err, extension.ErrMailRejected) {
		t.Fatalf("a 4xx answer must be temporary: %v", err)
	}
	none := extensionHost{&Service{}}
	if err := none.SendMail(ctx, "a@example.com", "Hi", "body"); err == nil || errors.Is(err, extension.ErrMailRejected) {
		t.Fatalf("without mail settings: %v", err)
	}
}
