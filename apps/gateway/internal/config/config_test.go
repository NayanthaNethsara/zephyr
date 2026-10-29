package config

import "testing"

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    Config
		wantErr bool
	}{
		{
			name: "defaults",
			want: Config{Port: 8080, LogLevel: "info"},
		},
		{
			name: "environment overrides defaults",
			env:  map[string]string{"GATEWAY_PORT": "9090", "LOG_LEVEL": "debug"},
			want: Config{Port: 9090, LogLevel: "debug"},
		},
		{
			name:    "port out of range",
			env:     map[string]string{"GATEWAY_PORT": "70000"},
			wantErr: true,
		},
		{
			name:    "unknown log level",
			env:     map[string]string{"LOG_LEVEL": "verbose"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("GATEWAY_PORT", "")
			t.Setenv("LOG_LEVEL", "")
			for key, value := range tt.env {
				t.Setenv(key, value)
			}

			got, err := Load()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Load() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Fatalf("Load() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
