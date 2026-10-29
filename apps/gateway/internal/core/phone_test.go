package core

import "testing"

func TestMaskPhone(t *testing.T) {
	tests := []struct {
		phone string
		want  string
	}{
		{phone: "94770000000", want: "9477*****00"},
		{phone: "15551234567", want: "1555*****67"},
		{phone: "123456", want: "******"},
		{phone: "12", want: "**"},
		{phone: "", want: ""},
	}
	for _, tt := range tests {
		if got := MaskPhone(tt.phone); got != tt.want {
			t.Errorf("MaskPhone(%q) = %q, want %q", tt.phone, got, tt.want)
		}
	}
}
