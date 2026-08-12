package tag

import (
	"os"
	"os/exec"
	"strings"
)

type InstallCapability struct {
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason,omitempty"`
	Command string `json:"command,omitempty"`
}

type HostInstallPolicy interface {
	Capability(provider string) InstallCapability
}

// EnvironmentHostInstallPolicy deliberately does not model application roles.
// Installation is a host operation: the operator can disable it explicitly,
// while the OS and npm remain the authority on whether the process may write.
type EnvironmentHostInstallPolicy struct{}

func (EnvironmentHostInstallPolicy) Capability(provider string) InstallCapability {
	packageName, supported := ProviderInstallPackage(provider)
	if !supported {
		return InstallCapability{Reason: "unknown provider"}
	}
	if value := strings.TrimSpace(os.Getenv("AGENT_TAG_DISABLE_PROVIDER_INSTALL")); value == "1" || strings.EqualFold(value, "true") {
		return InstallCapability{Reason: "宿主机已通过 AGENT_TAG_DISABLE_PROVIDER_INSTALL 禁用安装"}
	}
	if _, err := exec.LookPath("npm"); err != nil {
		return InstallCapability{Reason: "宿主机未安装 npm"}
	}
	return InstallCapability{Allowed: true, Command: "npm install -g " + packageName}
}
