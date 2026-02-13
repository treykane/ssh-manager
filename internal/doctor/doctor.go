package doctor

import (
	"fmt"
	"sort"

	"github.com/treykane/ssh-manager/internal/config"
	"github.com/treykane/ssh-manager/internal/model"
	"github.com/treykane/ssh-manager/internal/security"
	"github.com/treykane/ssh-manager/internal/sshclient"
	"github.com/treykane/ssh-manager/internal/tunnel"
	"github.com/treykane/ssh-manager/internal/util"
)

type Severity string

const (
	SeverityLow    Severity = "low"
	SeverityMedium Severity = "medium"
	SeverityHigh   Severity = "high"
)

type Issue struct {
	Severity       Severity `json:"severity"`
	Check          string   `json:"check"`
	Target         string   `json:"target"`
	Message        string   `json:"message"`
	Recommendation string   `json:"recommendation"`
}

type Report struct {
	Issues []Issue `json:"issues"`
}

// Run executes local diagnostics for ssh-manager operations.
func Run() (Report, error) {
	var issues []Issue

	if err := sshclient.EnsureSSHBinary(); err != nil {
		issues = append(issues, Issue{
			Severity:       SeverityHigh,
			Check:          "ssh-binary",
			Target:         "PATH",
			Message:        err.Error(),
			Recommendation: "install OpenSSH client and ensure `ssh` is on PATH",
		})
	}

	res, err := config.ParseDefault()
	if err == nil {
		for _, w := range res.Warnings {
			issues = append(issues, Issue{
				Severity:       SeverityMedium,
				Check:          "config-warning",
				Target:         "~/.ssh/config",
				Message:        w,
				Recommendation: "fix malformed/unsupported SSH config directives",
			})
		}
		issues = append(issues, duplicateBindIssues(res.Hosts)...)
	}

	mgr := tunnel.NewManager(sshclient.New())
	if err := mgr.LoadRuntime(); err == nil {
		for _, rt := range mgr.Snapshot() {
			if rt.State == model.TunnelQuarantined {
				issues = append(issues, Issue{
					Severity:       SeverityMedium,
					Check:          "runtime-quarantine",
					Target:         rt.ID,
					Message:        "tunnel is quarantined",
					Recommendation: "inspect with `tunnel status` and run `tunnel recover` when safe",
				})
			}
			if rt.State == model.TunnelUp && rt.PID == 0 {
				issues = append(issues, Issue{
					Severity:       SeverityMedium,
					Check:          "runtime-stale",
					Target:         rt.ID,
					Message:        "runtime shows up state with missing PID",
					Recommendation: "restart the tunnel to refresh runtime state",
				})
			}
		}
	}

	if audit, err := security.RunLocalAudit(); err == nil {
		for _, f := range audit.Findings {
			sev := SeverityLow
			if f.Severity == security.SeverityMedium {
				sev = SeverityMedium
			}
			if f.Severity == security.SeverityHigh {
				sev = SeverityHigh
			}
			issues = append(issues, Issue{
				Severity:       sev,
				Check:          "security-audit",
				Target:         f.Target,
				Message:        f.Message,
				Recommendation: f.Recommendation,
			})
		}
	}

	sort.Slice(issues, func(i, j int) bool {
		ri := severityRank(issues[i].Severity)
		rj := severityRank(issues[j].Severity)
		if ri != rj {
			return ri > rj
		}
		if issues[i].Check != issues[j].Check {
			return issues[i].Check < issues[j].Check
		}
		if issues[i].Target != issues[j].Target {
			return issues[i].Target < issues[j].Target
		}
		return issues[i].Message < issues[j].Message
	})
	return Report{Issues: issues}, nil
}

func duplicateBindIssues(hosts []model.HostEntry) []Issue {
	type bindRef struct {
		host string
	}
	seen := map[string][]bindRef{}
	for _, h := range hosts {
		for _, fwd := range h.Forwards {
			key := fmt.Sprintf("%s:%d", util.NormalizeAddr(fwd.LocalAddr, "127.0.0.1"), fwd.LocalPort)
			seen[key] = append(seen[key], bindRef{host: h.Alias})
		}
	}
	var issues []Issue
	for bind, refs := range seen {
		if len(refs) < 2 {
			continue
		}
		issues = append(issues, Issue{
			Severity:       SeverityHigh,
			Check:          "duplicate-local-bind",
			Target:         bind,
			Message:        fmt.Sprintf("local bind is configured by %d hosts", len(refs)),
			Recommendation: "use unique local ports per host/forward to avoid tunnel startup conflicts",
		})
	}
	return issues
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
