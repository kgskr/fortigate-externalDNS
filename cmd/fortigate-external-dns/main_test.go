package main

import (
	"testing"

	"github.com/gilsu/fortigate-external-dns/internal/config"
)

func TestUseLeaderElection(t *testing.T) {
	cases := []struct {
		name string
		cfg  config.Config
		want bool
	}{
		{name: "default long-running", cfg: config.Config{LeaderElection: true}, want: true},
		{name: "once bypasses election", cfg: config.Config{LeaderElection: true, Once: true}, want: false},
		{name: "disabled for local testing", cfg: config.Config{LeaderElection: false}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := useLeaderElection(tc.cfg); got != tc.want {
				t.Fatalf("useLeaderElection() = %v, want %v", got, tc.want)
			}
		})
	}
}
