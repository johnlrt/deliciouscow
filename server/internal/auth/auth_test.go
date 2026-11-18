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
