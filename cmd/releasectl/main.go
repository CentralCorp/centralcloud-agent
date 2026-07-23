package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

type asset struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

type manifest struct {
	SchemaVersion   int              `json:"schema_version"`
	Component       string           `json:"component"`
	Version         string           `json:"version"`
	ProtocolVersion string           `json:"protocol_version"`
	PublishedAt     string           `json:"published_at"`
	Assets          map[string]asset `json:"assets"`
	Compatibility   struct {
		MinimumDashboardVersion string `json:"minimum_dashboard_version"`
		MinimumInstallerVersion string `json:"minimum_installer_version"`
	} `json:"compatibility"`
}

func main() {
	if len(os.Args) < 2 {
		fatal(errors.New("generate or sign command is required"))
	}
	var err error
	switch os.Args[1] {
	case "generate":
		err = generate(os.Args[2:])
	case "sign":
		err = sign(os.Args[2:])
	default:
		err = errors.New("unknown command")
	}
	if err != nil {
		fatal(err)
	}
}

func generate(args []string) error {
	flags := flag.NewFlagSet("generate", flag.ContinueOnError)
	version := flags.String("version", "", "release version")
	protocol := flags.String("protocol", "1", "Agent protocol")
	baseURL := flags.String("base-url", "", "immutable release URL")
	amd64Sum := flags.String("amd64-sha256", "", "amd64 sha256")
	arm64Sum := flags.String("arm64-sha256", "", "arm64 sha256")
	output := flags.String("output", "agent-release-manifest.json", "output file")
	published := flags.String("published-at", "", "RFC3339 publication date")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *version == "" || *baseURL == "" || !validSum(*amd64Sum) || !validSum(*arm64Sum) {
		return errors.New("version, base-url and both SHA-256 values are required")
	}
	date := *published
	if date == "" {
		date = time.Now().UTC().Format(time.RFC3339)
	}
	value := manifest{SchemaVersion: 1, Component: "centralcloud-agent", Version: *version, ProtocolVersion: *protocol, PublishedAt: date}
	value.Assets = map[string]asset{
		"linux-amd64": {URL: strings.TrimRight(*baseURL, "/") + "/centralcloud-agent_" + *version + "_linux_amd64", SHA256: strings.ToLower(*amd64Sum)},
		"linux-arm64": {URL: strings.TrimRight(*baseURL, "/") + "/centralcloud-agent_" + *version + "_linux_arm64", SHA256: strings.ToLower(*arm64Sum)},
	}
	value.Compatibility.MinimumDashboardVersion = "1.0.0"
	value.Compatibility.MinimumInstallerVersion = "1.0.0"
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(*output, data, 0o600)
}

func sign(args []string) error {
	flags := flag.NewFlagSet("sign", flag.ContinueOnError)
	input := flags.String("input", "agent-release-manifest.json", "manifest")
	output := flags.String("output", "agent-release-manifest.json.sig", "signature")
	keyFile := flags.String("key-file", "", "base64 Ed25519 private key file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *keyFile == "" {
		return errors.New("key-file is required")
	}
	encoded, err := os.ReadFile(*keyFile) // #nosec G304 -- explicit release secret path.
	if err != nil {
		return err
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(encoded)))
	for i := range encoded {
		encoded[i] = 0
	}
	if err != nil || len(raw) != ed25519.PrivateKeySize {
		return errors.New("private key must be a base64-encoded 64-byte Ed25519 key")
	}
	data, err := os.ReadFile(*input) // #nosec G304 -- explicit release artifact path.
	if err != nil {
		return err
	}
	signature := ed25519.Sign(ed25519.PrivateKey(raw), data)
	for i := range raw {
		raw[i] = 0
	}
	return os.WriteFile(*output, []byte(base64.StdEncoding.EncodeToString(signature)+"\n"), 0o600) // #nosec G703 -- explicit release artifact output path.
}

func validSum(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
