package crypto

import "testing"

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
