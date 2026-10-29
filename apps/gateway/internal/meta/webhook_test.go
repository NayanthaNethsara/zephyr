package meta

import (
	"errors"
	"testing"

	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/crypto"
)

func TestVerifySignature(t *testing.T) {
	body := []byte(`{"object":"whatsapp_business_account"}`)
	valid := "sha256=" + crypto.SignHMACSHA256([]byte("app-secret"), body)

	tests := []struct {
		name    string
		secret  string
		header  string
		body    []byte
		wantErr bool
	}{
		{name: "valid signature", secret: "app-secret", header: valid, body: body},
		{name: "missing header", secret: "app-secret", header: "", body: body, wantErr: true},
		{name: "missing prefix", secret: "app-secret", header: valid[len("sha256="):], body: body, wantErr: true},
		{name: "wrong secret", secret: "other", header: valid, body: body, wantErr: true},
		{name: "tampered body", secret: "app-secret", header: valid, body: []byte(`{}`), wantErr: true},
		{name: "invalid hex", secret: "app-secret", header: "sha256=nothex", body: body, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := VerifySignature(tt.secret, tt.header, tt.body)
			if (err != nil) != tt.wantErr {
				t.Fatalf("VerifySignature() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr && !errors.Is(err, ErrInvalidSignature) {
				t.Fatalf("error = %v, want ErrInvalidSignature", err)
			}
		})
	}
}

func TestVerifyHandshake(t *testing.T) {
	tests := []struct {
		name  string
		mode  string
		token string
		want  bool
	}{
		{name: "subscribe with matching token", mode: "subscribe", token: "secret", want: true},
		{name: "wrong token", mode: "subscribe", token: "other", want: false},
		{name: "wrong mode", mode: "unsubscribe", token: "secret", want: false},
		{name: "empty values", mode: "", token: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := VerifyHandshake(tt.mode, tt.token, "secret"); got != tt.want {
				t.Fatalf("VerifyHandshake() = %v, want %v", got, tt.want)
			}
		})
	}
}
