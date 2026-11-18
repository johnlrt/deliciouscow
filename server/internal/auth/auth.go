package auth

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"golang.org/x/crypto/scrypt"
)

type Config struct {
	Pepper          []byte
	Ed25519Priv     ed25519.PrivateKey
	Ed25519Pub      ed25519.PublicKey
	InsecureCookies bool
}

func NewConfig(pepperHex string, insecureCookies bool) (*Config, error) {
	pepper := []byte(pepperHex)
	if len(pepper) == 0 {
		pepper = make([]byte, 32)
		if _, err := rand.Read(pepper); err != nil {
			return nil, err
		}
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return &Config{
		Pepper:          pepper,
		Ed25519Priv:     priv,
		Ed25519Pub:      pub,
		InsecureCookies: insecureCookies,
	}, nil
}

func RandomHex(nBytes int) (string, error) {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func RandomBase32(nBytes int) (string, error) {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b), nil
}

func HashPassword(pw string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	dk, err := scrypt.Key([]byte(pw), salt, 32768, 8, 1, 32)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(salt) + ":" + hex.EncodeToString(dk), nil
}

func VerifyPassword(pw, stored string) bool {
	parts := strings.SplitN(stored, ":", 2)
	if len(parts) != 2 {
		return false
	}
	salt, err1 := hex.DecodeString(parts[0])
	want, err2 := hex.DecodeString(parts[1])
	if err1 != nil || err2 != nil {
		return false
	}
	dk, err := scrypt.Key([]byte(pw), salt, 32768, 8, 1, len(want))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(dk, want) == 1
}

func (c *Config) HashWithPepper(secret string) string {
	mac := hmac.New(sha256.New, c.Pepper)
	mac.Write([]byte(secret))
	return hex.EncodeToString(mac.Sum(nil))
}

func SHA256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func GenerateRecoveryCodes(n int) (plain []string, hashes []string, err error) {
	plain = make([]string, n)
	hashes = make([]string, n)
	for i := 0; i < n; i++ {
		raw, err := RandomHex(8)
		if err != nil {
			return nil, nil, err
		}
		code := raw[:4] + "-" + raw[4:8] + "-" + raw[8:12] + "-" + raw[12:16]
		plain[i] = code
		hashes[i] = SHA256Hex(code)
	}
	return plain, hashes, nil
}

func VerifyNodeProof(nodeSecret, nodeID, nonce, proofHex string) bool {
	msg := "cordova-node-auth-v1\n" + nodeID + "\n" + nonce
	mac := hmac.New(sha256.New, []byte(nodeSecret))
	mac.Write([]byte(msg))
	expected := hex.EncodeToString(mac.Sum(nil))
	return subtle.ConstantTimeCompare([]byte(expected), []byte(strings.ToLower(proofHex))) == 1
}

func ComputeNodeProof(nodeSecret, nodeID, nonce string) string {
	msg := "cordova-node-auth-v1\n" + nodeID + "\n" + nonce
	mac := hmac.New(sha256.New, []byte(nodeSecret))
	mac.Write([]byte(msg))
	return hex.EncodeToString(mac.Sum(nil))
}

type NodeJWTClaims struct {
	Iss           string `json:"iss"`
	Aud           string `json:"aud"`
	Sub           string `json:"sub"`
	SecretVersion int    `json:"secret_version"`
	JTI           string `json:"jti"`
	Iat           int64  `json:"iat"`
	Exp           int64  `json:"exp"`
}

func (c *Config) MintNodeJWT(nodeID string, secretVersion int, jti string, now time.Time) (string, time.Time, error) {
	exp := now.Add(5 * time.Minute)
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"EdDSA","typ":"JWT"}`))
	claims := NodeJWTClaims{
		Iss:           "cordova",
		Aud:           "cordova-node-ws",
		Sub:           nodeID,
		SecretVersion: secretVersion,
		JTI:           jti,
		Iat:           now.Unix(),
		Exp:           exp.Unix(),
	}
	cb, err := json.Marshal(claims)
	if err != nil {
		return "", time.Time{}, err
	}
	payload := base64.RawURLEncoding.EncodeToString(cb)
	signingInput := hdr + "." + payload
	sig := ed25519.Sign(c.Ed25519Priv, []byte(signingInput))
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), exp, nil
}

func (c *Config) VerifyNodeJWT(token string, now time.Time) (*NodeJWTClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("malformed jwt")
	}
	hb, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, err
	}
	var hdr struct {
		Alg string `json:"alg"`
	}
	if err := json.Unmarshal(hb, &hdr); err != nil || hdr.Alg != "EdDSA" {
		return nil, errors.New("unsupported jwt alg")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, err
	}
	if !ed25519.Verify(c.Ed25519Pub, []byte(parts[0]+"."+parts[1]), sig) {
		return nil, errors.New("invalid jwt signature")
	}
	cb, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, err
	}
	var claims NodeJWTClaims
	if err := json.Unmarshal(cb, &claims); err != nil {
		return nil, err
	}
	if claims.Iss != "cordova" || claims.Aud != "cordova-node-ws" {
		return nil, errors.New("invalid iss/aud")
	}
	const skew = 30
	if now.Unix() > claims.Exp+skew || now.Unix() < claims.Iat-skew {
		return nil, errors.New("jwt expired or not yet valid")
	}
	return &claims, nil
}

// TOTP (RFC 6238, SHA1, 6 digits, 30s period)
func TOTPCode(secretBase32 string, at time.Time) (string, error) {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(strings.TrimSpace(secretBase32)))
	if err != nil {
		return "", err
	}
	counter := uint64(at.Unix() / 30)
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], counter)
	mac := hmac.New(sha1.New, key)
	mac.Write(buf[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	code := (uint32(sum[offset]&0x7f) << 24) |
		(uint32(sum[offset+1]) << 16) |
		(uint32(sum[offset+2]) << 8) |
		uint32(sum[offset+3])
	return fmt.Sprintf("%06d", code%1000000), nil
}

func VerifyTOTP(secretBase32, code string, now time.Time) bool {
	code = strings.TrimSpace(code)
	if len(code) != 6 {
		return false
	}
	for _, delta := range []time.Duration{-30 * time.Second, 0, 30 * time.Second} {
		want, err := TOTPCode(secretBase32, now.Add(delta))
		if err == nil && subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			return true
		}
	}
	return false
}

func BuildOTPAuthURI(username, secret string) string {
	return fmt.Sprintf("otpauth://totp/Cordova:%s?secret=%s&issuer=Cordova", url.PathEscape(username), url.QueryEscape(secret))
}
