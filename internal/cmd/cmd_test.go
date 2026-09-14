package cmd

import (
	"reflect"
	"slices"
	"testing"

	"tinysync/internal/buildinfo"
)

// TestNewCommand 验证 CLI 收敛：serve（主）与 version 子命令（同 --version）。
func TestNewCommand(t *testing.T) {
	root := NewCommand()

	if root.Name != "tinysync" {
		t.Errorf("Name = %q, want tinysync", root.Name)
	}
	if root.Version != buildinfo.Version {
		t.Errorf("Version = %q, want %q", root.Version, buildinfo.Version)
	}

	names := make([]string, 0, len(root.Commands))
	for _, c := range root.Commands {
		names = append(names, c.Name)
	}
	// serve 为主命令、version 子命令同 --version。
	if want := []string{"serve", "version"}; !reflect.DeepEqual(names, want) {
		t.Errorf("Commands = %v, want %v", names, want)
	}

	// serve 子命令应带 --datadir / --port flag。
	hasFlag := func(cmdName, want string) bool {
		for _, c := range root.Commands {
			if c.Name != cmdName {
				continue
			}
			for _, f := range c.Flags {
				if slices.Contains(f.Names(), want) {
					return true
				}
			}
		}
		return false
	}
	if !hasFlag("serve", "datadir") {
		t.Error("serve command has no --datadir flag")
	}
	if !hasFlag("serve", "port") {
		t.Error("serve command has no --port flag")
	}

	// serve 作为主命令不应 Hidden。
	for _, c := range root.Commands {
		if c.Name == "serve" && c.Hidden {
			t.Error("serve command should not be hidden (primary command)")
		}
	}
}
