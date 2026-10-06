package service

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Passkeys (WebAuthn), a focused standard-library implementation in the same
// spirit as the OAuth server: only what Metatrash uses.
//
//   - Registration asks for no attestation ("none"). Whatever format the
//     authenticator still sends, its attestation statement is ignored; the
//     credential is trusted because the signed-in user's own browser delivered
//     it in answer to our challenge, over HTTPS.
//   - Keys: ES256 (P-256), EdDSA (Ed25519) and RS256 (2048 to 4096 bits).
//   - User verification (PIN, fingerprint, face) is required every time.
//   - Discoverable credentials ("resident keys"), so signing in needs no email.
//
// Challenges are stateless: "issued.random.signature", signed with the
// service secret for one purpose ("register", "login" or "confirm") and bound
// to the browser's login cookie or session. A challenge is accepted once and
// for webauthnChallengeLifetime.

var b64 = base64.RawURLEncoding

const maxCredentialIDBytes = 1023
const webauthnChallengeLifetime = 10 * time.Minute

var aaguidPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// COSE algorithm identifiers.
const (
	coseES256 = -7
	coseEdDSA = -8
	coseRS256 = -257
)

// Authenticator data flags.
const (
	flagUP = 0x01 // user present
	flagUV = 0x04 // user verified
	flagBE = 0x08 // backup eligible (a synced passkey)
	flagBS = 0x10 // backed up
	flagAT = 0x40 // attested credential data included
	flagED = 0x80 // extension data included
)

// ---- CBOR (RFC 8949), the subset authenticators use -------------------------

const cborMaxDepth = 16

// cborDecode decodes one data item from the start of b and returns it with the
// number of bytes it used. Maps decode to map[any]any with int64 or string
// keys, unsigned and negative integers to int64, byte strings to []byte, text
// to string, arrays to []any. Indefinite lengths, tags and half-precision
// floats are refused, as are duplicate map keys.
func cborDecode(b []byte) (any, int, error) {
	return cborItem(b, 0)
}

func cborHead(b []byte) (major byte, arg uint64, n int, err error) {
	if len(b) == 0 {
		return 0, 0, 0, fmt.Errorf("cbor: truncated")
	}
	major, info := b[0]>>5, b[0]&0x1f
	switch {
	case info < 24:
		return major, uint64(info), 1, nil
	case info == 24 && len(b) >= 2:
		return major, uint64(b[1]), 2, nil
	case info == 25 && len(b) >= 3:
		return major, uint64(binary.BigEndian.Uint16(b[1:])), 3, nil
	case info == 26 && len(b) >= 5:
		return major, uint64(binary.BigEndian.Uint32(b[1:])), 5, nil
	case info == 27 && len(b) >= 9:
		return major, binary.BigEndian.Uint64(b[1:]), 9, nil
	case info >= 28:
		return 0, 0, 0, fmt.Errorf("cbor: unsupported length encoding")
	}
	return 0, 0, 0, fmt.Errorf("cbor: truncated")
}

func cborItem(b []byte, depth int) (any, int, error) {
	if depth > cborMaxDepth {
		return nil, 0, fmt.Errorf("cbor: nested too deeply")
	}
	major, arg, n, err := cborHead(b)
	if err != nil {
		return nil, 0, err
	}
	rest := uint64(len(b) - n)
	switch major {
	case 0:
		if arg > math.MaxInt64 {
			return nil, 0, fmt.Errorf("cbor: integer too large")
		}
		return int64(arg), n, nil
	case 1:
		if arg > math.MaxInt64 {
			return nil, 0, fmt.Errorf("cbor: integer too large")
		}
		return -1 - int64(arg), n, nil
	case 2, 3:
		if arg > rest {
			return nil, 0, fmt.Errorf("cbor: truncated string")
		}
		value := b[n : n+int(arg)]
		if major == 3 {
			if !utf8.Valid(value) {
				return nil, 0, fmt.Errorf("cbor: invalid text")
			}
			return string(value), n + int(arg), nil
		}
		return bytes.Clone(value), n + int(arg), nil
	case 4:
		if arg > rest {
			return nil, 0, fmt.Errorf("cbor: truncated array")
		}
		list := make([]any, 0, arg)
		for i := uint64(0); i < arg; i++ {
			item, used, err := cborItem(b[n:], depth+1)
			if err != nil {
				return nil, 0, err
			}
			list = append(list, item)
			n += used
		}
		return list, n, nil
	case 5:
		if arg > rest/2 {
			return nil, 0, fmt.Errorf("cbor: truncated map")
		}
		m := make(map[any]any, arg)
		for i := uint64(0); i < arg; i++ {
			key, used, err := cborItem(b[n:], depth+1)
			if err != nil {
				return nil, 0, err
			}
			n += used
			switch key.(type) {
			case int64, string:
			default:
				return nil, 0, fmt.Errorf("cbor: unsupported map key")
			}
			if _, dup := m[key]; dup {
				return nil, 0, fmt.Errorf("cbor: duplicate map key")
			}
			value, used, err := cborItem(b[n:], depth+1)
			if err != nil {
				return nil, 0, err
			}
			n += used
			m[key] = value
		}
		return m, n, nil
	case 7:
		info := b[0] & 0x1f
		switch {
		case info == 20:
			return false, n, nil
		case info == 21:
			return true, n, nil
		case info == 22 || info == 23:
			return nil, n, nil
		case info == 26:
			return float64(math.Float32frombits(uint32(arg))), n, nil
		case info == 27:
			return math.Float64frombits(arg), n, nil
		}
	}
	return nil, 0, fmt.Errorf("cbor: unsupported item")
}

// cborDecodeAll decodes b, which must hold exactly one item.
func cborDecodeAll(b []byte) (any, error) {
	value, n, err := cborDecode(b)
	if err != nil {
		return nil, err
	}
	if n != len(b) {
		return nil, fmt.Errorf("cbor: trailing data")
	}
	return value, nil
}

// ---- COSE keys (RFC 9052/9053) ----------------------------------------------

type coseKey struct {
	alg int
	ec  *ecdsa.PublicKey
	rsa *rsa.PublicKey
	ed  ed25519.PublicKey
}

// parseCOSEKey parses a credential public key. want, when not zero, is the
// algorithm the key must have.
func parseCOSEKey(b []byte, want int) (coseKey, error) {
	bad := fmt.Errorf("unsupported or invalid public key")
	value, err := cborDecodeAll(b)
	if err != nil {
		return coseKey{}, bad
	}
	m, ok := value.(map[any]any)
	if !ok {
		return coseKey{}, bad
	}
	kty, _ := m[int64(1)].(int64)
	alg, _ := m[int64(3)].(int64)
	if want != 0 && alg != int64(want) {
		return coseKey{}, bad
	}
	key := coseKey{alg: int(alg)}
	switch {
	case kty == 2 && alg == coseES256:
		crv, _ := m[int64(-1)].(int64)
		x, _ := m[int64(-2)].([]byte)
		y, _ := m[int64(-3)].([]byte)
		if crv != 1 || len(x) != 32 || len(y) != 32 {
			return coseKey{}, bad
		}
		point := append(append([]byte{4}, x...), y...)
		if key.ec, err = ecdsa.ParseUncompressedPublicKey(elliptic.P256(), point); err != nil {
			return coseKey{}, bad
		}
	case kty == 1 && alg == coseEdDSA:
		crv, _ := m[int64(-1)].(int64)
		x, _ := m[int64(-2)].([]byte)
		if crv != 6 || len(x) != ed25519.PublicKeySize {
			return coseKey{}, bad
		}
		key.ed = ed25519.PublicKey(x)
	case kty == 3 && alg == coseRS256:
		n, _ := m[int64(-1)].([]byte)
		e, _ := m[int64(-2)].([]byte)
		if len(n) == 0 || n[0] == 0 || len(e) == 0 || len(e) > 4 || e[0] == 0 {
			return coseKey{}, bad
		}
		modulus := new(big.Int).SetBytes(n)
		exponent := int(new(big.Int).SetBytes(e).Int64())
		if modulus.BitLen() < 2048 || modulus.BitLen() > 4096 || exponent < 3 || exponent%2 == 0 {
			return coseKey{}, bad
		}
		key.rsa = &rsa.PublicKey{N: modulus, E: exponent}
	default:
		return coseKey{}, bad
	}
	return key, nil
}

func (k coseKey) verify(data, signature []byte) bool {
	switch {
	case k.ec != nil:
		sum := sha256.Sum256(data)
		return ecdsa.VerifyASN1(k.ec, sum[:], signature)
	case k.ed != nil:
		return ed25519.Verify(k.ed, data, signature)
	case k.rsa != nil:
		sum := sha256.Sum256(data)
		return rsa.VerifyPKCS1v15(k.rsa, crypto.SHA256, sum[:], signature) == nil
	}
	return false
}

// ---- Authenticator data and client data -------------------------------------

type authenticatorData struct {
	rpIDHash     []byte
	flags        byte
	signCount    uint32
	aaguid       []byte
	credentialID []byte
	publicKey    []byte
}

// parseAuthenticatorData parses authenticator data. Registration includes the
// attested credential (attested true); sign-in must not.
func parseAuthenticatorData(b []byte, attested bool) (authenticatorData, error) {
	bad := fmt.Errorf("invalid authenticator data")
	if len(b) < 37 {
		return authenticatorData{}, bad
	}
	d := authenticatorData{rpIDHash: b[:32], flags: b[32], signCount: binary.BigEndian.Uint32(b[33:37])}
	rest := b[37:]
	if (d.flags&flagAT != 0) != attested {
		return authenticatorData{}, bad
	}
	if attested {
		if len(rest) < 18 {
			return authenticatorData{}, bad
		}
		d.aaguid = rest[:16]
		idLen := int(binary.BigEndian.Uint16(rest[16:18]))
		rest = rest[18:]
		if idLen == 0 || idLen > maxCredentialIDBytes || len(rest) < idLen {
			return authenticatorData{}, bad
		}
		d.credentialID, rest = rest[:idLen], rest[idLen:]
		_, used, err := cborDecode(rest)
		if err != nil {
			return authenticatorData{}, bad
		}
		d.publicKey, rest = rest[:used], rest[used:]
	}
	if d.flags&flagED != 0 {
		ext, used, err := cborDecode(rest)
		if _, isMap := ext.(map[any]any); err != nil || !isMap {
			return authenticatorData{}, bad
		}
		rest = rest[used:]
	}
	if len(rest) != 0 {
		return authenticatorData{}, bad
	}
	// Backed up without being backup eligible is invalid.
	if d.flags&flagBS != 0 && d.flags&flagBE == 0 {
		return authenticatorData{}, bad
	}
	return d, nil
}

type collectedClientData struct {
	Type        string `json:"type"`
	Challenge   string `json:"challenge"`
	Origin      string `json:"origin"`
	CrossOrigin bool   `json:"crossOrigin"`
	TopOrigin   string `json:"topOrigin"`
}

// rpID is the WebAuthn relying party ID: the site's host name.
func (a *accounts) rpID() string {
	origin, err := url.Parse(a.config.Origin)
	if err != nil {
		return ""
	}
	return origin.Hostname()
}

// userHandle is the WebAuthn user ID for an account: its 16-byte ID.
func userHandle(userID string) []byte {
	b, _ := hex.DecodeString(userID)
	return b
}

// checkClientData parses clientDataJSON, checks type and origin, and spends
// its challenge for purpose and binding.
func (a *accounts) checkClientData(raw []byte, kind, purpose, binding string, now time.Time) error {
	var c collectedClientData
	if len(raw) == 0 || len(raw) > 4096 || json.Unmarshal(raw, &c) != nil {
		return passkeyFailed()
	}
	if c.Type != kind || c.Origin != a.config.Origin || c.CrossOrigin || c.TopOrigin != "" {
		return passkeyFailed()
	}
	return a.spendWebAuthnChallenge(purpose, binding, c.Challenge, now)
}

func passkeyFailed() error {
	return problem(400, "invalid_passkey", "The passkey could not be verified. Reload the page and try again.")
}

// ---- Challenges ---------------------------------------------------------------

// newWebAuthnChallenge returns a challenge (base64url, as the browser API and
// clientDataJSON carry it) for purpose, bound to binding.
func (a *accounts) newWebAuthnChallenge(purpose, binding string, now time.Time) string {
	random, err := randomHex(16)
	if err != nil {
		return ""
	}
	body := strconv.FormatInt(now.Unix(), 10) + "." + random
	return b64.EncodeToString([]byte(body + "." + a.webauthnSignature(purpose, binding, body)))
}

func (a *accounts) webauthnSignature(purpose, binding, body string) string {
	return a.mac("webauthn:" + purpose + ":" + secretDigest(binding) + ":" + body)[:32]
}

func (a *accounts) spendWebAuthnChallenge(purpose, binding, encoded string, now time.Time) error {
	expired := problem(400, "invalid_passkey", "This page expired. Reload it and try again.")
	raw, err := b64.DecodeString(encoded)
	if err != nil || binding == "" {
		return passkeyFailed()
	}
	parts := strings.Split(string(raw), ".")
	if len(parts) != 3 || len(parts[1]) != 32 || len(parts[2]) != 32 {
		return passkeyFailed()
	}
	issued, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return passkeyFailed()
	}
	body := parts[0] + "." + parts[1]
	if subtle.ConstantTimeCompare([]byte(parts[2]), []byte(a.webauthnSignature(purpose, binding, body))) != 1 {
		return passkeyFailed()
	}
	age := now.Sub(time.Unix(issued, 0))
	if age < -time.Minute || age > webauthnChallengeLifetime {
		return expired
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cleanup(now)
	if a.powUsed == nil {
		a.powUsed = map[string]time.Time{}
	}
	key := "webauthn:" + body
	if _, used := a.powUsed[key]; used {
		return expired
	}
	if len(a.powUsed) >= 16384 {
		return problem(503, "busy", "Please try again shortly.")
	}
	a.powUsed[key] = time.Unix(issued, 0).Add(webauthnChallengeLifetime)
	return nil
}

// ---- Registration and sign-in -------------------------------------------------

// verifyPasskeyRegistration checks a new credential created for userID in
// answer to a "register" challenge bound to the session, and returns it ready
// to store (without ID, name or creation time).
func (a *accounts) verifyPasskeyRegistration(session, userID string, clientDataJSON, attestationObject []byte, transports string, now time.Time) (passkey, error) {
	if err := a.checkClientData(clientDataJSON, "webauthn.create", "register", session, now); err != nil {
		return passkey{}, err
	}
	value, err := cborDecodeAll(attestationObject)
	m, ok := value.(map[any]any)
	if err != nil || !ok {
		return passkey{}, passkeyFailed()
	}
	format, _ := m["fmt"].(string)
	raw, _ := m["authData"].([]byte)
	if _, ok := m["attStmt"].(map[any]any); !ok || format == "" || len(format) > 32 {
		return passkey{}, passkeyFailed()
	}
	d, err := parseAuthenticatorData(raw, true)
	if err != nil {
		return passkey{}, passkeyFailed()
	}
	rp := sha256.Sum256([]byte(a.rpID()))
	if subtle.ConstantTimeCompare(d.rpIDHash, rp[:]) != 1 || d.flags&flagUP == 0 {
		return passkey{}, passkeyFailed()
	}
	if d.flags&flagUV == 0 {
		return passkey{}, problem(400, "invalid_passkey", "Your device did not verify you (with a PIN, fingerprint or face). Try again, or use another device.")
	}
	key, err := parseCOSEKey(d.publicKey, 0)
	if err != nil {
		return passkey{}, problem(400, "invalid_passkey", "This passkey uses a kind of key Metatrash does not support. Try another device or password manager.")
	}
	if !transportsPattern.MatchString(transports) || len(transports) > 128 {
		transports = ""
	}
	return passkey{
		UserID:         userID,
		CredentialID:   bytes.Clone(d.credentialID),
		PublicKey:      bytes.Clone(d.publicKey),
		Algorithm:      key.alg,
		SignCount:      d.signCount,
		AAGUID:         hex.EncodeToString(d.aaguid),
		Transports:     transports,
		BackupEligible: d.flags&flagBE != 0,
		BackedUp:       d.flags&flagBS != 0,
	}, nil
}

// verifyPasskeyAssertion checks a sign-in with stored passkey p in answer to a
// challenge for purpose bound to binding. It returns the new signature counter
// and backup state to store.
func (a *accounts) verifyPasskeyAssertion(purpose, binding string, p passkey, clientDataJSON, authData, signature, handle []byte, now time.Time) (uint32, bool, error) {
	if err := a.checkClientData(clientDataJSON, "webauthn.get", purpose, binding, now); err != nil {
		return 0, false, err
	}
	d, err := parseAuthenticatorData(authData, false)
	if err != nil {
		return 0, false, passkeyFailed()
	}
	rp := sha256.Sum256([]byte(a.rpID()))
	if subtle.ConstantTimeCompare(d.rpIDHash, rp[:]) != 1 || d.flags&flagUP == 0 || d.flags&flagUV == 0 {
		return 0, false, passkeyFailed()
	}
	if (d.flags&flagBE != 0) != p.BackupEligible {
		return 0, false, passkeyFailed()
	}
	if len(handle) > 0 && !bytes.Equal(handle, userHandle(p.UserID)) {
		return 0, false, passkeyFailed()
	}
	key, err := parseCOSEKey(p.PublicKey, p.Algorithm)
	if err != nil {
		return 0, false, passkeyFailed()
	}
	sum := sha256.Sum256(clientDataJSON)
	if len(signature) == 0 || len(signature) > 1024 || !key.verify(append(bytes.Clone(authData), sum[:]...), signature) {
		return 0, false, passkeyFailed()
	}
	// A counter that does not move forward suggests a cloned authenticator.
	// Synced passkeys usually keep it at zero, which is allowed.
	if (d.signCount != 0 || p.SignCount != 0) && d.signCount <= p.SignCount {
		return 0, false, problem(400, "invalid_passkey", "This passkey could not be verified. If this keeps happening, remove it from Your account and add it again.")
	}
	return d.signCount, d.flags&flagBS != 0, nil
}

// ---- Options for the browser --------------------------------------------------

type webauthnCredentialRef struct {
	Type       string   `json:"type"`
	ID         string   `json:"id"`
	Transports []string `json:"transports,omitempty"`
}

func passkeyRefs(list []passkey) []webauthnCredentialRef {
	refs := []webauthnCredentialRef{}
	for _, p := range list {
		ref := webauthnCredentialRef{Type: "public-key", ID: b64.EncodeToString(p.CredentialID)}
		if p.Transports != "" {
			ref.Transports = strings.Split(p.Transports, ",")
		}
		refs = append(refs, ref)
	}
	return refs
}

// creationOptions is PublicKeyCredentialCreationOptions as JSON (binary values
// in base64url), for passkey.js.
func (a *accounts) creationOptions(session string, user userAccount, existing []passkey, now time.Time) string {
	display := user.Email
	if user.Username != "" {
		display = user.Username
	}
	options := map[string]any{
		"challenge": a.newWebAuthnChallenge("register", session, now),
		"rp":        map[string]string{"id": a.rpID(), "name": "Metatrash"},
		"user":      map[string]string{"id": b64.EncodeToString(userHandle(user.ID)), "name": user.Email, "displayName": display},
		"pubKeyCredParams": []map[string]any{
			{"type": "public-key", "alg": coseEdDSA},
			{"type": "public-key", "alg": coseES256},
			{"type": "public-key", "alg": coseRS256},
		},
		"timeout":            300000,
		"attestation":        "none",
		"excludeCredentials": passkeyRefs(existing),
		"authenticatorSelection": map[string]any{
			"residentKey":        "required",
			"requireResidentKey": true,
			"userVerification":   "required",
		},
	}
	b, _ := json.Marshal(options)
	return string(b)
}

// requestOptions is PublicKeyCredentialRequestOptions as JSON. allowed limits
// the choice to those passkeys (confirming on Your account); sign-in passes
// none, so the browser offers every passkey it has for this site.
func (a *accounts) requestOptions(purpose, binding string, allowed []passkey, now time.Time) string {
	options := map[string]any{
		"challenge":        a.newWebAuthnChallenge(purpose, binding, now),
		"rpId":             a.rpID(),
		"timeout":          300000,
		"userVerification": "required",
	}
	if allowed != nil {
		options["allowCredentials"] = passkeyRefs(allowed)
	}
	b, _ := json.Marshal(options)
	return string(b)
}
