package identity

// End-to-end encryption between identity keys.
//
// Every identity key also has an X25519 key for encryption, derived from
// the same secret seed (so one key file is still the whole identity). The
// X25519 public key travels in signed messages, so it is bound to the
// Ed25519 identity that peers pin.
//
// Seal encrypts one message for one recipient:
//
//	e            = fresh X25519 key pair (per message)
//	shared       = X25519(e, recipient) || X25519(sender, recipient)
//	key, nonce   = HKDF-SHA256(shared, salt = e.pub || sender.pub || recipient.pub, "magnetk-seal-v1")
//	ciphertext   = AES-256-GCM(key, nonce, plaintext, aad)
//
// The ephemeral part means a captured message cannot be decrypted with the
// sender's key alone; the static part authenticates the sender (only the
// owner of the sender key can produce it). The relay only sees ciphertext.
//
// Limitation (documented): there is no ratchet yet, so someone who steals
// a device's identity key can decrypt messages previously sent TO that
// device if they also recorded them. A Double Ratchet (as in Signal or
// WhatsApp) is the next step.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// xPrivate returns the X25519 key derived from the identity seed.
func (k *Key) xPrivate() *ecdh.PrivateKey {
	k.xOnce.Do(func() {
		b := hkdf(k.priv.Seed(), nil, "magnetk-x25519-v1", 32)
		priv, err := ecdh.X25519().NewPrivateKey(b)
		if err != nil {
			panic(err)
		}
		k.x = priv
	})
	return k.x
}

// XPublic returns the X25519 public key (32 bytes).
func (k *Key) XPublic() []byte { return k.xPrivate().PublicKey().Bytes() }

// XPublicString returns XPublic in base64.
func (k *Key) XPublicString() string { return base64.StdEncoding.EncodeToString(k.XPublic()) }

// ParseXPublic decodes a base64 X25519 public key.
func ParseXPublic(s string) ([]byte, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b) != 32 {
		return nil, errors.New("identity: bad encryption key")
	}
	return b, nil
}

// Seal encrypts plaintext from k to the recipient's X25519 public key. It
// returns the ephemeral public key and the ciphertext.
func (k *Key) Seal(recipientX, aad, plaintext []byte) (epk, ciphertext []byte, err error) {
	rpub, err := ecdh.X25519().NewPublicKey(recipientX)
	if err != nil {
		return nil, nil, err
	}
	e, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	dh1, err := e.ECDH(rpub)
	if err != nil {
		return nil, nil, err
	}
	dh2, err := k.xPrivate().ECDH(rpub)
	if err != nil {
		return nil, nil, err
	}
	epk = e.PublicKey().Bytes()
	aead, nonce, err := sealKey(dh1, dh2, epk, k.XPublic(), recipientX)
	if err != nil {
		return nil, nil, err
	}
	return epk, aead.Seal(nil, nonce, plaintext, aad), nil
}

// Open decrypts a message sealed for k by the sender's X25519 key.
func (k *Key) Open(senderX, epk, aad, ciphertext []byte) ([]byte, error) {
	curve := ecdh.X25519()
	epub, err := curve.NewPublicKey(epk)
	if err != nil {
		return nil, err
	}
	spub, err := curve.NewPublicKey(senderX)
	if err != nil {
		return nil, err
	}
	dh1, err := k.xPrivate().ECDH(epub)
	if err != nil {
		return nil, err
	}
	dh2, err := k.xPrivate().ECDH(spub)
	if err != nil {
		return nil, err
	}
	aead, nonce, err := sealKey(dh1, dh2, epk, senderX, k.XPublic())
	if err != nil {
		return nil, err
	}
	pt, err := aead.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return nil, errors.New("identity: cannot decrypt (not for this key, or modified)")
	}
	return pt, nil
}

func sealKey(dh1, dh2, epk, senderX, recipientX []byte) (cipher.AEAD, []byte, error) {
	salt := append(append(append([]byte(nil), epk...), senderX...), recipientX...)
	okm := hkdf(append(append([]byte(nil), dh1...), dh2...), salt, "magnetk-seal-v1", 32+12)
	block, err := aes.NewCipher(okm[:32])
	if err != nil {
		return nil, nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, err
	}
	return aead, okm[32:], nil
}

// hkdf is HKDF-SHA256 (RFC 5869).
func hkdf(secret, salt []byte, info string, n int) []byte {
	if salt == nil {
		salt = make([]byte, sha256.Size)
	}
	ext := hmac.New(sha256.New, salt)
	ext.Write(secret)
	prk := ext.Sum(nil)
	var out, prev []byte
	for i := byte(1); len(out) < n; i++ {
		h := hmac.New(sha256.New, prk)
		h.Write(prev)
		h.Write([]byte(info))
		h.Write([]byte{i})
		prev = h.Sum(nil)
		out = append(out, prev...)
	}
	return out[:n]
}

// SafetyNumber is the number two people compare (read aloud, or side by
// side) to be sure no one is in the middle, like WhatsApp's security code.
// It is the same on both sides: 12 groups of 5 digits.
func SafetyNumber(fpA, fpB string) string {
	pair := []string{NormalizeFingerprint(fpA), NormalizeFingerprint(fpB)}
	sort.Strings(pair)
	sum := sha512.Sum512([]byte("magnetk-safety-v1|" + pair[0] + "|" + pair[1]))
	groups := make([]string, 12)
	for i := range groups {
		v := binary.BigEndian.Uint32(sum[i*4:]) % 100000
		groups[i] = fmt.Sprintf("%05d", v)
	}
	return strings.Join(groups, " ")
}
