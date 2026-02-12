package security

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/treykane/ssh-manager/internal/appconfig"
	"github.com/treykane/ssh-manager/internal/config"
)

type Severity string

const (
	SeverityLow    Severity = "low"
	SeverityMedium Severity = "medium"
	SeverityHigh   Severity = "high"
)

type Finding struct {
	Severity       Severity `json:"severity"`
	Target         string   `json:"target"`
	Message        string   `json:"message"`
	Recommendation string   `json:"recommendation"`
}

type AuditReport struct {
	Findings []Finding `json:"findings"`
}

func (r AuditReport) HasHigh() bool {
	for _, f := range r.Findings {
		if f.Severity == SeverityHigh {
			return true
		}
	}
	return false
}

// RunLocalAudit inspects local ssh-manager and OpenSSH file posture.
func RunLocalAudit() (AuditReport, error) {
	cfg, err := appconfig.Load()
	if err != nil {
		return AuditReport{}, err
	}

	var findings []Finding
	if cfg.Security.BindPolicy == appconfig.BindPolicyAllowPublic {
		findings = append(findings, Finding{
			Severity:       SeverityMedium,
			Target:         "config.yaml",
			Message:        "public tunnel binds are allowed by default",
			Recommendation: "set security.bind_policy to loopback-only",
		})
	}
	if cfg.Security.HostKeyPolicy == appconfig.HostKeyPolicyInsecure {
		findings = append(findings, Finding{
			Severity:       SeverityHigh,
			Target:         "config.yaml",
			Message:        "host key policy is insecure",
			Recommendation: "set security.host_key_policy to strict or accept-new",
		})
	}

	home, err := os.UserHomeDir()
	if err == nil {
		checkPathPerm(&findings, filepath.Join(home, ".ssh"), 0o700, false)
		checkPathPerm(&findings, filepath.Join(home, ".ssh", "config"), 0o600, true)
	}

	cfgDir, err := appconfig.ConfigDir()
	if err == nil {
		checkPathPerm(&findings, cfgDir, 0o700, false)
		checkPathPerm(&findings, filepath.Join(cfgDir, "config.yaml"), 0o600, true)
		checkPathPerm(&findings, filepath.Join(cfgDir, "runtime.json"), 0o600, true)
	}

	res, err := config.ParseDefault()
	if err == nil {
		seen := map[string]struct{}{}
		for _, h := range res.Hosts {
			if strings.TrimSpace(h.IdentityFile) == "" {
				continue
			}
			identity := h.IdentityFile
			if strings.HasPrefix(identity, "~/") && home != "" {
				identity = filepath.Join(home, identity[2:])
			}
			if _, ok := seen[identity]; ok {
				continue
			}
			seen[identity] = struct{}{}
			checkPathPerm(&findings, identity, 0o600, true)
		}
	}

	sort.Slice(findings, func(i, j int) bool {
		if findings[i].Severity != findings[j].Severity {
			return severityRank(findings[i].Severity) > severityRank(findings[j].Severity)
		}
		if findings[i].Target != findings[j].Target {
			return findings[i].Target < findings[j].Target
		}
		return findings[i].Message < findings[j].Message
	})
	return AuditReport{Findings: findings}, nil
}

func severityRank(s Severity) int {
	switch s {
	case SeverityHigh:
		return 3
	case SeverityMedium:
		return 2
	default:
		return 1
	}
}

func checkPathPerm(findings *[]Finding, path string, max os.FileMode, isFile bool) {
	st, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		*findings = append(*findings, Finding{
			Severity:       SeverityLow,
			Target:         path,
			Message:        fmt.Sprintf("unable to inspect permissions: %v", err),
			Recommendation: "verify path and permissions manually",
		})
		return
	}
	mode := st.Mode().Perm()
	if mode > max {
		kind := "directory"
		if isFile {
			kind = "file"
		}
		*findings = append(*findings, Finding{
			Severity:       SeverityMedium,
			Target:         path,
			Message:        fmt.Sprintf("%s permissions are too broad (%#o)", kind, mode),
			Recommendation: fmt.Sprintf("restrict permissions to %#o or tighter", max),
		})
	}
}
