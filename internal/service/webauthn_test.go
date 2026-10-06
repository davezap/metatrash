package service

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"math/big"
	"sort"
	"strings"
	"testing"
	"time"
)

// cborEncode is a test-only CBOR encoder for the shapes authenticators send.
func cborEncode(v any) []byte {
	head := func(major byte, n uint64) []byte {
		switch {
		case n < 24:
			return []byte{major<<5 | byte(n)}
		case n < 1<<8:
			return []byte{major<<5 | 24, byte(n)}
		case n < 1<<16:
			return binary.BigEndian.AppendUint16([]byte{major<<5 | 25}, uint16(n))
		case n < 1<<32:
			return binary.BigEndian.AppendUint32([]byte{major<<5 | 26}, uint32(n))
		}
		return binary.BigEndian.AppendUint64([]byte{major<<5 | 27}, n)
	}
	switch x := v.(type) {
	case int:
		return cborEncode(int64(x))
	case int64:
		if x >= 0 {
			return head(0, uint64(x))
		}
		return head(1, uint64(-1-x))
	case []byte:
		return append(head(2, uint64(len(x))), x...)
	case string:
		return append(head(3, uint64(len(x))), x...)
	case bool:
		if x {
			return []byte{0xf5}
		}
		return []byte{0xf4}
	case []any:
		out := head(4, uint64(len(x)))
		for _, item := range x {
			out = append(out, cborEncode(item)...)
		}
		return out
	case map[any]any:
		keys := make([][]byte, 0, len(x))
		values := map[string][]byte{}
		for k, val := range x {
			kb := cborEncode(k)
			keys = append(keys, kb)
			values[string(kb)] = cborEncode(val)
		}
		sort.Slice(keys, func(i, j int) bool { return bytes.Compare(keys[i], keys[j]) < 0 })
		out := head(5, uint64(len(x)))
		for _, kb := range keys {
			out = append(append(out, kb...), values[string(kb)]...)
		}
		return out
	}
	panic("cborEncode: unsupported type")
}

// softAuthenticator is a software passkey for tests.
type softAuthenticator struct {
	alg      int
	ec       *ecdsa.PrivateKey
	ed       ed25519.PrivateKey
	rsa      *rsa.PrivateKey
	id       []byte
	handle   []byte
	count    uint32
	noUV     bool
	synced   bool
	origin   string
	rpID     string
	tamperCD func(map[string]any)
}

func newSoftAuthenticator(t *testing.T, alg int) *softAuthenticator {
	t.Helper()
	s := &softAuthenticator{alg: alg, origin: "https://metatrash.com"}
	var err error
	switch alg {
	case coseES256:
		s.ec, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	case coseEdDSA:
		_, s.ed, err = ed25519.GenerateKey(rand.Reader)
	case coseRS256:
		s.rsa, err = rsa.GenerateKey(rand.Reader, 2048)
	}
	if err != nil {
		t.Fatal(err)
	}
	s.id = make([]byte, 32)
	_, _ = rand.Read(s.id)
	return s
}

func (s *softAuthenticator) coseKey() []byte {
	switch s.alg {
	case coseES256:
		b, _ := s.ec.PublicKey.Bytes() // 0x04 || X || Y
		return cborEncode(map[any]any{int64(1): int64(2), int64(3): int64(coseES256), int64(-1): int64(1), int64(-2): b[1:33], int64(-3): b[33:]})
	case coseEdDSA:
		return cborEncode(map[any]any{int64(1): int64(1), int64(3): int64(coseEdDSA), int64(-1): int64(6), int64(-2): []byte(s.ed.Public().(ed25519.PublicKey))})
	}
	e := big.NewInt(int64(s.rsa.E)).Bytes()
	return cborEncode(map[any]any{int64(1): int64(3), int64(3): int64(coseRS256), int64(-1): s.rsa.N.Bytes(), int64(-2): e})
}

func (s *softAuthenticator) flags(extra byte) byte {
	f := byte(flagUP|flagUV) | extra
	if s.noUV {
		f &^= flagUV
	}
	if s.synced {
		f |= flagBE | flagBS
	}
	return f
}

func (s *softAuthenticator) clientData(kind, challenge string) []byte {
	c := map[string]any{"type": kind, "challenge": challenge, "origin": s.origin, "crossOrigin": false}
	if s.tamperCD != nil {
		s.tamperCD(c)
	}
	b, _ := json.Marshal(c)
	return b
}

func (s *softAuthenticator) rpHash(fallback string) []byte {
	rp := s.rpID
	if rp == "" {
		rp = fallback
	}
	sum := sha256.Sum256([]byte(rp))
	return sum[:]
}

// create answers creation options (JSON) and returns clientDataJSON and the
// attestation object.
func (s *softAuthenticator) create(t *testing.T, optionsJSON string) ([]byte, []byte) {
	t.Helper()
	var o struct {
		Challenge string              `json:"challenge"`
		RP        struct{ ID string } `json:"rp"`
		User      struct{ ID string } `json:"user"`
	}
	if err := json.Unmarshal([]byte(optionsJSON), &o); err != nil {
		t.Fatal(err)
	}
	s.handle, _ = b64.DecodeString(o.User.ID)
	auth := append(s.rpHash(o.RP.ID), s.flags(flagAT))
	auth = binary.BigEndian.AppendUint32(auth, s.count)
	auth = append(auth, bytes.Repeat([]byte{0xab}, 16)...)
	auth = binary.BigEndian.AppendUint16(auth, uint16(len(s.id)))
	auth = append(append(auth, s.id...), s.coseKey()...)
	att := cborEncode(map[any]any{"fmt": "none", "attStmt": map[any]any{}, "authData": auth})
	return s.clientData("webauthn.create", o.Challenge), att
}

type softAssertion struct{ credential, clientData, authData, signature, handle []byte }

func (s *softAuthenticator) get(t *testing.T, optionsJSON string) softAssertion {
	t.Helper()
	var o struct {
		Challenge string `json:"challenge"`
		RPID      string `json:"rpId"`
	}
	if err := json.Unmarshal([]byte(optionsJSON), &o); err != nil {
		t.Fatal(err)
	}
	if s.count > 0 {
		s.count++
	}
	auth := append(s.rpHash(o.RPID), s.flags(0))
	auth = binary.BigEndian.AppendUint32(auth, s.count)
	cd := s.clientData("webauthn.get", o.Challenge)
	sum := sha256.Sum256(cd)
	signed := append(bytes.Clone(auth), sum[:]...)
	var sig []byte
	var err error
	switch s.alg {
	case coseES256:
		digest := sha256.Sum256(signed)
		sig, err = ecdsa.SignASN1(rand.Reader, s.ec, digest[:])
	case coseEdDSA:
		sig = ed25519.Sign(s.ed, signed)
	case coseRS256:
		digest := sha256.Sum256(signed)
		sig, err = rsa.SignPKCS1v15(rand.Reader, s.rsa, crypto.SHA256, digest[:])
	}
	if err != nil {
		t.Fatal(err)
	}
	return softAssertion{credential: s.id, clientData: cd, authData: auth, signature: sig, handle: s.handle}
}

func testWebAuthnAccounts() *accounts {
	return &accounts{config: accountConfig{Origin: "https://metatrash.com"}, challenges: map[string]loginChallenge{}, sessions: map[string]accountSession{}, powUsed: map[string]time.Time{}, secret: []byte("test-only-secret")}
}

func TestCBORDecode(t *testing.T) {
	value, err := cborDecodeAll(cborEncode(map[any]any{"a": []any{int64(1), int64(-500), []byte{1, 2}, true}, int64(-3): "x"}))
	m, ok := value.(map[any]any)
	if err != nil || !ok || m[int64(-3)] != "x" || len(m["a"].([]any)) != 4 || m["a"].([]any)[1] != int64(-500) {
		t.Fatal(value, err)
	}
	for name, b := range map[string][]byte{
		"trailing":         append(cborEncode(int64(1)), 0),
		"indefinite":       {0x9f, 0x01, 0xff},
		"tag":              {0xc0, 0x01},
		"duplicate key":    {0xa2, 0x01, 0x01, 0x01, 0x02},
		"bad text":         {0x62, 0xff, 0xfe},
		"short string":     {0x45, 0x01},
		"huge array":       {0x9b, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff},
		"huge map":         {0xba, 0xff, 0xff, 0xff, 0xff},
		"array key":        {0xa1, 0x80, 0x01},
		"half float":       {0xf9, 0x3c, 0x00},
		"too deep":         bytes.Repeat([]byte{0x81}, 40),
		"integer too big":  {0x1b, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff},
		"reserved length":  {0x1c},
		"empty":            {},
		"truncated length": {0x19, 0x01},
	} {
		if _, err := cborDecodeAll(b); err == nil {
			t.Error(name, "accepted")
		}
	}
}

func TestCOSEKeys(t *testing.T) {
	for _, alg := range []int{coseES256, coseEdDSA, coseRS256} {
		s := newSoftAuthenticator(t, alg)
		key, err := parseCOSEKey(s.coseKey(), 0)
		if err != nil || key.alg != alg {
			t.Fatal(alg, err)
		}
		if _, err := parseCOSEKey(s.coseKey(), coseES256+coseEdDSA); err == nil {
			t.Fatal("algorithm mismatch accepted")
		}
	}
	// A P-256 point that is not on the curve.
	bad := cborEncode(map[any]any{int64(1): int64(2), int64(3): int64(coseES256), int64(-1): int64(1), int64(-2): bytes.Repeat([]byte{1}, 32), int64(-3): bytes.Repeat([]byte{2}, 32)})
	if _, err := parseCOSEKey(bad, 0); err == nil {
		t.Fatal("off-curve key accepted")
	}
	small, _ := rsa.GenerateKey(rand.Reader, 1024)
	weak := cborEncode(map[any]any{int64(1): int64(3), int64(3): int64(coseRS256), int64(-1): small.N.Bytes(), int64(-2): []byte{1, 0, 1}})
	if _, err := parseCOSEKey(weak, 0); err == nil {
		t.Fatal("1024-bit RSA key accepted")
	}
	es384 := cborEncode(map[any]any{int64(1): int64(2), int64(3): int64(-35), int64(-1): int64(2), int64(-2): bytes.Repeat([]byte{1}, 48), int64(-3): bytes.Repeat([]byte{2}, 48)})
	if _, err := parseCOSEKey(es384, 0); err == nil {
		t.Fatal("unsupported algorithm accepted")
	}
}

func TestPasskeyRegistrationAndAssertion(t *testing.T) {
	now := time.Now()
	user := userAccount{ID: strings.Repeat("ab", 16), Email: "person@example.com", Username: "person"}
	session := strings.Repeat("5", 64)
	for _, alg := range []int{coseES256, coseEdDSA, coseRS256} {
		a := testWebAuthnAccounts()
		s := newSoftAuthenticator(t, alg)
		s.count = 7
		cd, att := s.create(t, a.creationOptions(session, user, nil, now))
		p, err := a.verifyPasskeyRegistration(session, user.ID, cd, att, "internal,hybrid", now)
		if err != nil || p.Algorithm != alg || !bytes.Equal(p.CredentialID, s.id) || p.SignCount != 7 || p.Transports != "internal,hybrid" || p.AAGUID != strings.Repeat("ab", 16) {
			t.Fatal(alg, err, p)
		}
		if !bytes.Equal(s.handle, userHandle(user.ID)) {
			t.Fatal("user handle is not the account ID")
		}
		// The same answer cannot be used twice.
		if _, err := a.verifyPasskeyRegistration(session, user.ID, cd, att, "", now); err == nil {
			t.Fatal("registration challenge reused")
		}
		browser := strings.Repeat("b", 64)
		in := s.get(t, a.requestOptions("login", browser, nil, now))
		count, backedUp, err := a.verifyPasskeyAssertion("login", browser, p, in.clientData, in.authData, in.signature, in.handle, now)
		if err != nil || count != 8 || backedUp {
			t.Fatal(alg, err, count)
		}
		if _, _, err := a.verifyPasskeyAssertion("login", browser, p, in.clientData, in.authData, in.signature, in.handle, now); err == nil {
			t.Fatal("login challenge reused")
		}
	}
}

func TestPasskeyRejections(t *testing.T) {
	now := time.Now()
	user := userAccount{ID: strings.Repeat("cd", 16), Email: "person@example.com"}
	session, browser := strings.Repeat("6", 64), strings.Repeat("7", 64)
	register := func(s *softAuthenticator) (passkey, error) {
		a := testWebAuthnAccounts()
		cd, att := s.create(t, a.creationOptions(session, user, nil, now))
		return a.verifyPasskeyRegistration(session, user.ID, cd, att, "", now)
	}
	// Registration refusals.
	cases := map[string]func(s *softAuthenticator){
		"no user verification": func(s *softAuthenticator) { s.noUV = true },
		"wrong origin":         func(s *softAuthenticator) { s.origin = "https://evil.example" },
		"wrong rp id":          func(s *softAuthenticator) { s.rpID = "evil.example" },
		"wrong type":           func(s *softAuthenticator) { s.tamperCD = func(c map[string]any) { c["type"] = "webauthn.get" } },
		"cross origin":         func(s *softAuthenticator) { s.tamperCD = func(c map[string]any) { c["crossOrigin"] = true } },
		"forged challenge": func(s *softAuthenticator) {
			s.tamperCD = func(c map[string]any) {
				c["challenge"] = b64.EncodeToString([]byte("1." + strings.Repeat("0", 32) + "." + strings.Repeat("0", 32)))
			}
		},
	}
	for name, change := range cases {
		s := newSoftAuthenticator(t, coseES256)
		change(s)
		if _, err := register(s); err == nil {
			t.Error(name, "accepted")
		}
	}
	// A challenge for another purpose, another session or too old.
	a := testWebAuthnAccounts()
	s := newSoftAuthenticator(t, coseES256)
	cd, att := s.create(t, a.creationOptions("other-session", user, nil, now))
	if _, err := a.verifyPasskeyRegistration(session, user.ID, cd, att, "", now); err == nil {
		t.Fatal("challenge for another session accepted")
	}
	cd, att = s.create(t, a.requestOptions("login", session, nil, now))
	if _, err := a.verifyPasskeyRegistration(session, user.ID, cd, att, "", now); err == nil {
		t.Fatal("login challenge accepted for registration")
	}
	cd, att = s.create(t, a.creationOptions(session, user, nil, now.Add(-11*time.Minute)))
	if _, err := a.verifyPasskeyRegistration(session, user.ID, cd, att, "", now); err == nil {
		t.Fatal("expired challenge accepted")
	}

	// Assertion refusals.
	s = newSoftAuthenticator(t, coseES256)
	p, err := register(s)
	if err != nil {
		t.Fatal(err)
	}
	try := func(change func(*softAssertion), purpose string) error {
		a := testWebAuthnAccounts()
		in := s.get(t, a.requestOptions(purpose, browser, nil, now))
		if change != nil {
			change(&in)
		}
		_, _, err := a.verifyPasskeyAssertion("login", browser, p, in.clientData, in.authData, in.signature, in.handle, now)
		return err
	}
	if err := try(nil, "login"); err != nil {
		t.Fatal(err)
	}
	if try(nil, "confirm") == nil {
		t.Fatal("confirm challenge accepted for login")
	}
	if try(func(in *softAssertion) { in.signature[len(in.signature)-1] ^= 1 }, "login") == nil {
		t.Fatal("bad signature accepted")
	}
	if try(func(in *softAssertion) { in.authData[32] |= flagAT }, "login") == nil {
		t.Fatal("attested data in an assertion accepted")
	}
	if try(func(in *softAssertion) { in.handle = []byte("someone else") }, "login") == nil {
		t.Fatal("other user handle accepted")
	}
	s.noUV = true
	if try(nil, "login") == nil {
		t.Fatal("assertion without user verification accepted")
	}
	s.noUV = false
	// Counters: zero stays allowed; a counter that goes back is refused.
	p.SignCount = 10
	s.count = 5
	if try(nil, "login") == nil {
		t.Fatal("counter going back accepted")
	}
	s.count = 10
	if try(nil, "login") != nil {
		t.Fatal("counter moving forward refused")
	}
	// Backup eligibility cannot change after registration.
	s.synced = true
	if try(nil, "login") == nil {
		t.Fatal("backup eligibility change accepted")
	}
}
