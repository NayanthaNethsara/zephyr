package crypto

import (
	"encoding/hex"
	"testing"
)

func TestSHA256Digest(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "empty string", value: "", want: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
		{name: "abc", value: "abc", want: "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			digest := SHA256Digest(tt.value)
			if got := hex.EncodeToString(digest[:]); got != tt.want {
				t.Fatalf("SHA256Digest(%q) = %s, want %s", tt.value, got, tt.want)
			}
		})
	}
}

func TestVerifyHMACSHA256(t *testing.T) {
	secret := []byte("secret")
	signature := SignHMACSHA256(secret, []byte("12345"), []byte("."), []byte("body"))

	tests := []struct {
		name      string
		secret    []byte
		signature string
		parts     [][]byte
		want      bool
	}{
		{name: "matching parts", secret: secret, signature: signature, parts: [][]byte{[]byte("12345"), []byte("."), []byte("body")}, want: true},
		{name: "parts joined", secret: secret, signature: signature, parts: [][]byte{[]byte("12345.body")}, want: true},
		{name: "tampered body", secret: secret, signature: signature, parts: [][]byte{[]byte("12345.other")}, want: false},
		{name: "wrong secret", secret: []byte("other"), signature: signature, parts: [][]byte{[]byte("12345.body")}, want: false},
		{name: "not hex", secret: secret, signature: "zzzz", parts: [][]byte{[]byte("12345.body")}, want: false},
		{name: "empty signature", secret: secret, signature: "", parts: [][]byte{[]byte("12345.body")}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := VerifyHMACSHA256(tt.secret, tt.signature, tt.parts...); got != tt.want {
				t.Fatalf("VerifyHMACSHA256() = %v, want %v", got, tt.want)
			}
		})
	}
}
