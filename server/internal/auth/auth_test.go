package auth_test

import (
	"testing"
	"time"

	"github.com/johnlrt/deliciouscow/server/internal/auth"
)

func TestPasswordAndRecoveryAndTOTPAndJWT(t *testing.T) {
	cfg, err := auth.NewConfig("pepper-secret", true)
	if err != nil {
		t.Fatal(err)
	}

	// Password hashing
	h, err := auth.HashPassword("correcthorsebatterystaple")
	if err != nil {
		t.Fatal(err)
	}
	if !auth.VerifyPassword("correcthorsebatterystaple", h) {
		t.Fatal("password verify failed")
	}
	if auth.VerifyPassword("wrong", h) {
		t.Fatal("wrong password should fail")
	}

	// TOTP
	sec, _ := auth.RandomBase32(20)
	now := time.Unix(1700000000, 0).UTC()
	code, err := auth.TOTPCode(sec, now)
	if err != nil || len(code) != 6 {
		t.Fatalf("totp code failed: %q err=%v", code, err)
	}
	if !auth.VerifyTOTP(sec, code, now) {
		t.Fatal("totp verify failed")
	}
	if auth.VerifyTOTP(sec, "000000", now) && code != "000000" {
		t.Fatal("wrong totp should fail")
	}

	// Ed25519 JWT
	jwt, _, err := cfg.MintNodeJWT("node-1", 1, "jti-1", now)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := cfg.VerifyNodeJWT(jwt, now.Add(1*time.Minute))
	if err != nil || claims.Sub != "node-1" || claims.JTI != "jti-1" {
		t.Fatalf("jwt verify failed: claims=%+v err=%v", claims, err)
	}
	if _, err := cfg.VerifyNodeJWT(jwt, now.Add(10*time.Minute)); err == nil {
		t.Fatal("expired jwt should fail")
	}

	// Node HMAC proof
	proof := auth.ComputeNodeProof("node-sec", "node-1", "nonce-1")
	if !auth.VerifyNodeProof("node-sec", "node-1", "nonce-1", proof) {
		t.Fatal("node proof failed")
	}
}

func TestEncryptDecryptNodeSecret(t *testing.T) {
	t.Setenv("NODE_SECRET_MASTER_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	cfg1, err := auth.NewConfig("pepper-1", true)
	if err != nil {
		t.Fatal(err)
	}
	enc1, err := cfg1.EncryptNodeSecret("super-secret-node-key")
	if err != nil {
		t.Fatal(err)
	}
	enc2, err := cfg1.EncryptNodeSecret("super-secret-node-key")
	if err != nil {
		t.Fatal(err)
	}
	if enc1 == enc2 {
		t.Fatal("expected random nonces to produce distinct ciphertexts")
	}
	if enc1 == "super-secret-node-key" {
		t.Fatal("secret must not be stored in plaintext")
	}
	dec, err := cfg1.DecryptNodeSecret(enc1)
	if err != nil || dec != "super-secret-node-key" {
		t.Fatalf("decrypt failed: got=%q err=%v", dec, err)
	}

	t.Setenv("NODE_SECRET_MASTER_KEY", "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210")
	cfg2, err := auth.NewConfig("pepper-1", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg2.DecryptNodeSecret(enc1); err == nil {
		t.Fatal("expected decryption with wrong master key to fail")
	}
}
