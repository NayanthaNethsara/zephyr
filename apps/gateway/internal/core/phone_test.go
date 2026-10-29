package core

import "testing"

func TestMaskPhone(t *testing.T) {
	tests := []struct {
		name  string
		phone string
		want  string
	}{
		{name: "sri lankan number", phone: "94770000000", want: "9477*****00"},
		{name: "us number", phone: "15551234567", want: "1555*****67"},
		{name: "exactly six digits", phone: "123456", want: "******"},
		{name: "very short", phone: "12", want: "**"},
		{name: "empty", phone: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MaskPhone(tt.phone); got != tt.want {
				t.Errorf("MaskPhone(%q) = %q, want %q", tt.phone, got, tt.want)
			}
		})
	}
}
