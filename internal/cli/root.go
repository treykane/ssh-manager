package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/treykane/ssh-manager/internal/config"
	"github.com/treykane/ssh-manager/internal/model"
	"github.com/treykane/ssh-manager/internal/sshclient"
	"github.com/treykane/ssh-manager/internal/tunnel"
	"github.com/treykane/ssh-manager/internal/ui"
)

func NewRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   "ssh-manager",
		Short: "Modern SSH config and tunnel manager",
		RunE: func(cmd *cobra.Command, args []string) error {
			return ui.Run()
		},
	}

	root.AddCommand(newListCmd())
	root.AddCommand(newTunnelCmd())
	return root
}

func newListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List parsed hosts from ~/.ssh/config",
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := config.ParseDefault()
			if err != nil {
				return err
			}
			sort.Slice(res.Hosts, func(i, j int) bool { return res.Hosts[i].Alias < res.Hosts[j].Alias })
			fmt.Printf("%-24s %-24s %-8s %-16s %s\n", "ALIAS", "HOSTNAME", "PORT", "USER", "FORWARDS")
			for _, h := range res.Hosts {
				fmt.Printf("%-24s %-24s %-8d %-16s %d\n", h.Alias, h.DisplayTarget(), h.Port, defaultString(h.User, "-"), len(h.Forwards))
			}
			if len(res.Warnings) > 0 {
				fmt.Fprintln(os.Stderr, "warnings:")
				for _, w := range res.Warnings {
					fmt.Fprintf(os.Stderr, "  - %s\n", w)
				}
			}
			return nil
		},
	}
}

func newTunnelCmd() *cobra.Command {
	client := sshclient.New()
	mgr := tunnel.NewManager(client)
	_ = mgr.LoadRuntime()
	var root = &cobra.Command{Use: "tunnel", Short: "Manage SSH tunnels"}

	var forwardArg string
	up := &cobra.Command{
		Use:   "up <host>",
		Short: "Start tunnel(s) for host",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := sshclient.EnsureSSHBinary(); err != nil {
				return err
			}
			host, err := findHost(args[0])
			if err != nil {
				return err
			}
			forwards, err := resolveForwards(host, forwardArg)
			if err != nil {
				return err
			}
			for _, fwd := range forwards {
				rt, err := mgr.Start(host, fwd)
				if err != nil {
					return err
				}
				fmt.Printf("started %s pid=%d %s -> %s\n", rt.ID, rt.PID, rt.Local, rt.Remote)
			}
			return nil
		},
	}
	up.Flags().StringVar(&forwardArg, "forward", "", "forward index (0-based) or explicit spec localPort:remoteHost:remotePort")

	down := &cobra.Command{
		Use:   "down <tunnel-id|host>",
		Short: "Stop a tunnel by id or all tunnels for host",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			idOrHost := args[0]
			if strings.Contains(idOrHost, "|") {
				if err := mgr.Stop(idOrHost); err != nil {
					return err
				}
				fmt.Printf("stopped %s\n", idOrHost)
				return nil
			}
			if err := mgr.StopByHost(idOrHost); err != nil {
				return err
			}
			fmt.Printf("stopped tunnels for host %s\n", idOrHost)
			return nil
		},
	}

	var jsonOut bool
	status := &cobra.Command{
		Use:   "status",
		Short: "Show tunnel status",
		RunE: func(cmd *cobra.Command, args []string) error {
			sn := mgr.Snapshot()
			sort.Slice(sn, func(i, j int) bool { return sn[i].ID < sn[j].ID })
			if jsonOut {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(sn)
			}
			fmt.Printf("%-42s %-16s %-22s %-22s %-10s %-8s %-10s\n", "ID", "HOST", "LOCAL", "REMOTE", "STATE", "PID", "LAT(ms)")
			for _, rt := range sn {
				fmt.Printf("%-42s %-16s %-22s %-22s %-10s %-8d %-10d\n", rt.ID, rt.HostAlias, rt.Local, rt.Remote, rt.State, rt.PID, rt.LatencyMS)
			}
			return nil
		},
	}
	status.Flags().BoolVar(&jsonOut, "json", false, "output JSON")

	root.AddCommand(up, down, status)
	return root
}

func findHost(alias string) (model.HostEntry, error) {
	res, err := config.ParseDefault()
	if err != nil {
		return model.HostEntry{}, err
	}
	for _, h := range res.Hosts {
		if h.Alias == alias {
			return h, nil
		}
	}
	return model.HostEntry{}, fmt.Errorf("host not found: %s", alias)
}

func resolveForwards(host model.HostEntry, forwardArg string) ([]model.ForwardSpec, error) {
	if strings.TrimSpace(forwardArg) == "" {
		if len(host.Forwards) == 0 {
			return nil, fmt.Errorf("host %s has no LocalForward entries", host.Alias)
		}
		return host.Forwards, nil
	}
	if idx, err := strconv.Atoi(forwardArg); err == nil {
		if idx < 0 || idx >= len(host.Forwards) {
			return nil, fmt.Errorf("forward index out of range")
		}
		return []model.ForwardSpec{host.Forwards[idx]}, nil
	}
	fwd, err := tunnel.ParseForwardArg(forwardArg)
	if err != nil {
		return nil, err
	}
	return []model.ForwardSpec{fwd}, nil
}

func defaultString(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

func ConnectOnce(host model.HostEntry) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	c := sshclient.New()
	return c.RunInteractive(ctx, host)
}
