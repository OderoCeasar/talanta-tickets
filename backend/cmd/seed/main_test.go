package main

import "testing"

func TestIsLocalDevelopment(t *testing.T) {
	const local = "postgres://talanta:talanta@localhost:5434/talanta?sslmode=disable"
	const remote = "postgres://talanta:secret@talanta-db.example.com:5432/talanta"

	tests := []struct {
		name   string
		appEnv string
		url    string
		want   bool
	}{
		{"development on localhost", "development", local, true},
		{"test on loopback", "test", "postgres://u:p@127.0.0.1:5432/x", true},
		{"compose service name", "development", "postgres://u:p@db:5432/x", true},
		{"unset env fails closed", "", local, false},
		{"production", "production", local, false},
		{"wrong case is not development", "Development", local, false},
		{"development against a remote database", "development", remote, false},
		{"production service name is not local", "development", "postgres://u:p@talanta-db:5432/x", false},
		{"unparseable url", "development", "://nope", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isLocalDevelopment(tt.appEnv, tt.url); got != tt.want {
				t.Fatalf("isLocalDevelopment(%q, %q) = %v, want %v", tt.appEnv, tt.url, got, tt.want)
			}
		})
	}
}
