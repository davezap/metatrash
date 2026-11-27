package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"
	"time"

	"metatrash.com/metatrash/extension"
)

// Receiving email (0.97.0). A folder whose .metatrash.json has an email
// service gets its own address; every message sent to it becomes one
// Markdown file in the folder, its attachments and the original message
// virtual files beside it, and the folder's actions run as for any file
// that arrives:
//
//	{"services": [{"type": "email", "allow": ["no-reply@sns.amazonaws.com"]}]}
//
// Anyone may write to the address unless allow lists who may (addresses
// such as a@example.com, domains such as example.com, or *.example.com for
// its subdomains too); block refuses senders the same way, and wins.
// Raising address (1 by default) gives the folder a new address and retires
// the old one. A reply whose earlier message is already in the space loses
// the quoted history (history "strip", the default; "keep" leaves it).
//
// The address is <space id>-<tag>@<domain>: tag is an HMAC of the space,
// folder and address version under the server's email key, so it can't be
// guessed from the space ID or folder name and needs no table. Renaming or
// moving the folder changes it. Text after a + (sub-addressing) is ignored.
//
// Mail reaches the server from a relay that holds the email secret: the
// mail drop (POST /email/inbound, email_http.go) for one's own mail server,
// or a module (Company's Amazon SES receiver) through extension.EmailReceiver.
// The relay vouches for the sender checks it passes on (SPF, DKIM, DMARC,
// spam, virus); a message it marks as a virus or spam is refused, and a
// sender on allow whose domain's DMARC check failed is refused as forged.

// emailConfig is accounts.json email.
type emailConfig struct {
	Enabled bool `json:"enabled"`
	// Domain is the part after @ of every folder's address, such as
	// in.example.com. Its MX record points at the relay.
	Domain string `json:"domain"`
	// KeyFile holds 64 hex digits (openssl rand -hex 32) that make the
	// addresses. Changing it changes every folder's address.
	KeyFile string `json:"keyFile"`
	// SecretFile holds the bearer secret the mail drop takes (at least 32
	// characters). Without it there is no mail drop; modules still deliver.
	SecretFile string `json:"secretFile"`
}

type emailSettings struct {
	domain string
	key    []byte
	secret []byte
}

const (
	// maxEmailBytes is the largest raw message taken: Dave's 25 MB of
	// attachments, base64 encoded, and the rest of the message.
	maxEmailBytes = 36 << 20
	// maxEmailAttachmentBytes is what a message's kept attachments may add
	// up to, decoded.
	maxEmailAttachmentBytes = 25 << 20
	maxEmailAttachments     = 50
	maxEmailSenderRules     = 200
	maxEmailAddressVersion  = 1000
	emailTagLength          = 16
	emailHistoryStrip       = "strip"
	emailHistoryKeep        = "keep"
	// Per folder and per space, in an hour.
	emailFolderPerHour = 200
	emailSpacePerHour  = 1000
)

var (
	emailDomainPattern = regexp.MustCompile(`^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)
	emailLocalPattern  = regexp.MustCompile(`^([0-9a-f]{32})-([a-z2-7]{16})$`)
	emailTagEncoding   = base32.StdEncoding.WithPadding(base32.NoPadding)
)

func (c *emailConfig) settings() (*emailSettings, error) {
	domain := strings.ToLower(strings.TrimSpace(c.Domain))
	if len(domain) > 200 || !emailDomainPattern.MatchString(domain) {
		return nil, fmt.Errorf("email domain must be a domain name such as in.example.com")
	}
	b, err := os.ReadFile(c.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("email keyFile: cannot read %s", c.KeyFile)
	}
	key, err := hex.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("email keyFile must hold 64 hex digits (openssl rand -hex 32)")
	}
	settings := &emailSettings{domain: domain, key: key}
	if c.SecretFile != "" {
		secret, err := readLimitedFile(c.SecretFile, 4096)
		if err != nil || len(strings.TrimSpace(string(secret))) < 32 {
			return nil, fmt.Errorf("email secretFile must hold a secret of at least 32 characters (openssl rand -hex 32)")
		}
		settings.secret = []byte(strings.TrimSpace(string(secret)))
	}
	return settings, nil
}

// checkEmailService checks an email service's settings (parseFolderConfig).
func checkEmailService(svc folderService) error {
	if svc.Repo != "" || svc.Branch != "" || svc.Push != "" || svc.Pull != "" || svc.Files != nil || svc.NewFiles {
		return folderConfigError("email takes name, allow, block, address and history only.")
	}
	if svc.Name != "" && !actionNamePattern.MatchString(svc.Name) {
		return folderConfigError("email name is up to 40 lowercase letters, digits and hyphens, starting with a letter or digit; it tells a folder's email services apart.")
	}
	for name, rules := range map[string][]string{"allow": svc.Allow, "block": svc.Block} {
		if len(rules) > maxEmailSenderRules {
			return folderConfigError(fmt.Sprintf("email %s is limited to %d entries.", name, maxEmailSenderRules))
		}
		for _, rule := range rules {
			if !validSenderRule(rule) {
				return folderConfigError(fmt.Sprintf("email %s entry %q must be an email address (a@example.com), a domain (example.com) or a domain and its subdomains (*.example.com), in lower case.", name, rule))
			}
		}
	}
	if svc.Address < 0 || svc.Address > maxEmailAddressVersion {
		return folderConfigError(fmt.Sprintf("email address is the address's version, a whole number from 1 to %d: raise it to retire the folder's address and get a new one.", maxEmailAddressVersion))
	}
	if svc.History != "" && svc.History != emailHistoryStrip && svc.History != emailHistoryKeep {
		return folderConfigError(`email history is "strip" (the default: a reply loses the earlier messages it quotes when they are in the space) or "keep".`)
	}
	return nil
}

func validSenderRule(rule string) bool {
	if rule != strings.ToLower(rule) || len(rule) > 254 || strings.ContainsAny(rule, " \t\r\n<>\",;") {
		return false
	}
	if local, domain, ok := strings.Cut(rule, "@"); ok {
		return local != "" && len(local) <= 64 && !strings.Contains(domain, "@") && emailDomainPattern.MatchString(domain)
	}
	return emailDomainPattern.MatchString(strings.TrimPrefix(rule, "*."))
}

// senderMatches reports whether address (lower case) matches one of rules.
func senderMatches(rules []string, address string) bool {
	_, domain, _ := strings.Cut(address, "@")
	for _, rule := range rules {
		switch {
		case strings.Contains(rule, "@"):
			if rule == address {
				return true
			}
		case strings.HasPrefix(rule, "*."):
			base := rule[2:]
			if domain == base || strings.HasSuffix(domain, "."+base) {
				return true
			}
		case domain == rule:
			return true
		}
	}
	return false
}

func (svc folderService) addressVersion() int {
	if svc.Address == 0 {
		return 1
	}
	return svc.Address
}

func (e *emailSettings) tag(spaceID, folder, name string, version int) string {
	h := hmac.New(sha256.New, e.key)
	if name == "" {
		// The unnamed service keeps the address it had before names (0.97.2).
		fmt.Fprintf(h, "metatrash email address\x00%s\x00%s\x00%d", spaceID, folder, version)
	} else {
		fmt.Fprintf(h, "metatrash email address\x00%s\x00%s\x00%d\x00%s", spaceID, folder, version, name)
	}
	return strings.ToLower(emailTagEncoding.EncodeToString(h.Sum(nil)))[:emailTagLength]
}

// address is folder's current address.
func (e *emailSettings) address(spaceID, folder string, svc folderService) string {
	return spaceID + "-" + e.tag(spaceID, folder, svc.Name, svc.addressVersion()) + "@" + e.domain
}

// emailTarget is where a message to an address goes.
type emailTarget struct {
	repo    *repository
	folder  string
	service folderService
}

func emailNotFound() *Error {
	return problem(404, "not_found", "No folder receives email at this address.")
}

// resolveEmail finds the folder an address belongs to. Every failure is the
// same not_found, so an address says nothing about the spaces it isn't.
func (s *Service) resolveEmail(ctx context.Context, recipient string) (emailTarget, error) {
	e := s.emailIn
	if e == nil {
		return emailTarget{}, emailNotFound()
	}
	recipient = strings.ToLower(strings.TrimSpace(strings.Trim(strings.TrimSpace(recipient), "<>")))
	local, domain, ok := strings.Cut(recipient, "@")
	if !ok || domain != e.domain {
		return emailTarget{}, emailNotFound()
	}
	local, _, _ = strings.Cut(local, "+")
	m := emailLocalPattern.FindStringSubmatch(local)
	if m == nil {
		return emailTarget{}, emailNotFound()
	}
	repo := s.ownedRepository(m[1])
	if repo == nil || repo.closed.Load() {
		return emailTarget{}, emailNotFound()
	}
	_, files, err := repo.snapshot(ctx, "")
	if err != nil {
		return emailTarget{}, err
	}
	configs, err := repo.folderConfigs(ctx, files)
	if err != nil {
		return emailTarget{}, err
	}
	for folder, c := range configs {
		for _, svc := range c.Services {
			if svc.Type != "email" {
				continue
			}
			if subtle.ConstantTimeCompare([]byte(e.tag(repo.id, folder, svc.Name, svc.addressVersion())), []byte(m[2])) == 1 {
				return emailTarget{repo: repo, folder: folder, service: svc}, nil
			}
		}
	}
	return emailTarget{}, emailNotFound()
}

// folderEmailAddress is the address of folder's email service called name
// ("" for the unnamed one, or the folder's first when it has no unnamed one)
// in repo, folder being "" for the root, else ending in /, for its owner to see.
func (s *Service) folderEmailAddress(ctx context.Context, repo *repository, folder, name string) (string, error) {
	if s.emailIn == nil {
		return "", problem(404, "not_found", "This server does not receive email (accounts.json email).")
	}
	if folder != "" && (!strings.HasSuffix(folder, "/") || !validPath(strings.TrimSuffix(folder, "/"))) {
		return "", invalid("folder must be a folder of this space ending in /, or empty for its root.")
	}
	_, files, err := repo.snapshot(ctx, "")
	if err != nil {
		return "", err
	}
	configs, err := repo.folderConfigs(ctx, files)
	if err != nil {
		return "", err
	}
	var first *folderService
	for i, svc := range configs[folder].Services {
		if svc.Type != "email" {
			continue
		}
		if svc.Name == name {
			return s.emailIn.address(repo.id, folder, svc), nil
		}
		if first == nil {
			first = &configs[folder].Services[i]
		}
	}
	if name == "" && first != nil {
		return s.emailIn.address(repo.id, folder, *first), nil
	}
	if name != "" {
		return "", problem(404, "not_found", "This folder has no email service named "+name+".")
	}
	return "", problem(404, "not_found", "This folder has no email service: add {\"type\": \"email\"} to services in its "+folderConfigName+".")
}

// receiveEmail files one message sent to recipient. checks are what the
// relay found; an empty value means it did not check.
func (s *Service) receiveEmail(ctx context.Context, recipient string, raw []byte, checks extension.EmailChecks) (extension.EmailReceipt, error) {
	if len(raw) > maxEmailBytes {
		return extension.EmailReceipt{}, problem(413, "payload_too_large", fmt.Sprintf("A message may be at most %d MB.", maxEmailBytes>>20))
	}
	target, err := s.resolveEmail(ctx, recipient)
	if err != nil {
		return extension.EmailReceipt{}, err
	}
	checks = normalChecks(checks)
	if checks.Virus == "fail" {
		return extension.EmailReceipt{}, problem(422, "refused", "The message carries a virus.")
	}
	if checks.Spam == "fail" {
		return extension.EmailReceipt{}, problem(422, "refused", "The message was marked as spam.")
	}
	msg, err := parseEmail(raw)
	if err != nil {
		return extension.EmailReceipt{}, invalid("Not an email message: " + err.Error())
	}
	sender := msg.fromAddress()
	if senderMatches(target.service.Block, sender) {
		return extension.EmailReceipt{}, problem(403, "sender_refused", "This folder does not take email from this sender.")
	}
	if target.service.Allow != nil && (!senderMatches(target.service.Allow, sender) || checks.DMARC == "fail") {
		return extension.EmailReceipt{}, problem(403, "sender_refused", "This folder takes email only from the senders it allows.")
	}
	if err := s.rates.take(allowance{"email:folder:" + target.repo.id + ":" + target.folder, emailFolderPerHour, 3600}, allowance{"email:space:" + target.repo.id, emailSpacePerHour, 3600}); err != nil {
		return extension.EmailReceipt{}, err
	}
	now := s.now().UTC()
	id := msg.idHash()
	// Attachments and the original go to the bucket first, as form photos
	// do: objects are keyed by content, so a filing that then fails leaves
	// only what the sweep removes.
	store, _ := s.spaceFiles()
	var kept []emailStored
	var notes []string
	if store != nil {
		kept, notes, err = s.storeEmailFiles(ctx, target, msg, raw, id)
		if err != nil {
			return extension.EmailReceipt{}, err
		}
	} else {
		if len(msg.attachments) > 0 {
			notes = append(notes, fmt.Sprintf("%d attachment(s) not kept: this server keeps no virtual files.", len(msg.attachments)))
		}
	}
	filingCtx := withAuthor(ctx, func() changeAuthor { return changeAuthor{App: "Email", Kind: "agent"} })
	value, err := s.queued(filingCtx, target.repo, func() (any, error) {
		return fileEmail(filingCtx, target, msg, checks, kept, notes, id, now)
	})
	if err != nil {
		return extension.EmailReceipt{}, err
	}
	receipt := value.(extension.EmailReceipt)
	if !receipt.Duplicate {
		for _, f := range kept {
			s.docling().enqueueUpload(target.repo, f.path, f.info)
		}
		log.Printf("email: filed %d bytes in space %s folder %q as %s", len(raw), target.repo.id, target.folder, receipt.Path)
	}
	return receipt, nil
}

// normalChecks lower-cases the relay's verdicts and keeps known ones.
func normalChecks(c extension.EmailChecks) extension.EmailChecks {
	clean := func(v string) string {
		v = strings.ToLower(strings.TrimSpace(v))
		switch v {
		case "pass", "fail", "gray", "none", "neutral", "softfail", "temperror", "permerror", "processing_failed", "disabled":
			return v
		}
		return ""
	}
	return extension.EmailChecks{SPF: clean(c.SPF), DKIM: clean(c.DKIM), DMARC: clean(c.DMARC), Spam: clean(c.Spam), Virus: clean(c.Virus)}
}

// emailHost lets modules deliver mail (extension.EmailReceiver).
func (h extensionHost) ReceiveEmail(ctx context.Context, recipient string, raw []byte, checks extension.EmailChecks) (extension.EmailReceipt, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	return h.s.receiveEmail(ctx, recipient, raw, checks)
}

// EmailDomain is the domain folder addresses use, or "" when the server
// does not receive email.
func (h extensionHost) EmailDomain() string {
	if h.s.emailIn == nil {
		return ""
	}
	return h.s.emailIn.domain
}
