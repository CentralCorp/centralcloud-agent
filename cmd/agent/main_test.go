package main

import "testing"

func TestParseCommandsAndLegacyFlags(t *testing.T) {
	tests := []struct {
		args    []string
		command string
		path    string
	}{
		{[]string{"version"}, "version", "/etc/centralcloud-agent/config.yaml"},
		{[]string{"--version"}, "version", ""},
		{[]string{"validate-config", "--config", "/tmp/config.yaml"}, "validate-config", "/tmp/config.yaml"},
		{[]string{"-config", "/tmp/legacy.yaml"}, "serve", "/tmp/legacy.yaml"},
		{[]string{"serve", "--config", "/tmp/config.yaml"}, "serve", "/tmp/config.yaml"},
	}
	for _, test := range tests {
		command, path, err := parseArgs(test.args)
		if err != nil || command != test.command || path != test.path {
			t.Fatalf("%v: got %q %q %v", test.args, command, path, err)
		}
	}
}
