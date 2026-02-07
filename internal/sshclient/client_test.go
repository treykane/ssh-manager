package sshclient

import (
	"reflect"
	"testing"

	"github.com/treykane/ssh-manager/internal/model"
)

func TestBuildTunnelArgs(t *testing.T) {
	c := New()
	args := c.BuildTunnelArgs("prod", model.ForwardSpec{LocalAddr: "127.0.0.1", LocalPort: 8080, RemoteAddr: "localhost", RemotePort: 80})
	want := []string{"-N", "-L", "127.0.0.1:8080:localhost:80", "prod"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args mismatch\nwant=%v\n got=%v", want, args)
	}
}
