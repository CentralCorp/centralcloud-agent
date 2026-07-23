package logging

import (
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	got := Redact("password=hunter2 token: abc authorization=BearerXYZ Authorization: Bearer separated-token")
	if strings.Contains(got, "hunter2") || strings.Contains(got, "BearerXYZ") || strings.Contains(got, "separated-token") || strings.Contains(got, " abc") {
		t.Fatalf("secret leaked: %s", got)
	}
}
func TestScrubJSON(t *testing.T) {
	got := string(ScrubJSON([]byte(`{"nested":{"password":"secret"},"safe":"ok"}`)))
	if strings.Contains(got, "secret") || !strings.Contains(got, "ok") {
		t.Fatalf("unexpected scrub: %s", got)
	}
}
