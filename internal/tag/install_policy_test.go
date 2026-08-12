package tag

import "testing"

func TestHostCanDisableProviderInstallation(t *testing.T) {
	t.Setenv("AGENT_TAG_DISABLE_PROVIDER_INSTALL", "true")
	capability := (EnvironmentHostInstallPolicy{}).Capability("codex")
	if capability.Allowed || capability.Reason == "" {
		t.Fatalf("capability=%+v", capability)
	}
}
