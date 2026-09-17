package cmd

import (
	"context"
	"io"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/urfave/cli/v3"

	"tinysync/internal/buildinfo"
	"tinysync/internal/config"
)

// TestNewCommand 验证 CLI 收敛：serve（主）、auth set-password 与
// version 子命令（同 --version）。
func TestNewCommand(t *testing.T) {
	root := NewCommand(fstest.MapFS{})

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
	// serve 为主命令、auth 承载凭据管理、version 子命令同 --version。
	if want := []string{"serve", "auth", "version"}; !reflect.DeepEqual(names, want) {
		t.Errorf("Commands = %v, want %v", names, want)
	}

	// serve 子命令应带 --datadir / --port flag。
	hasFlag := func(cmd *cli.Command, want string) bool {
		for _, f := range cmd.Flags {
			if slices.Contains(f.Names(), want) {
				return true
			}
		}
		return false
	}
	find := func(path ...string) *cli.Command {
		t.Helper()
		current := root
		for _, name := range path {
			var next *cli.Command
			for _, c := range current.Commands {
				if c.Name == name {
					next = c
					break
				}
			}
			if next == nil {
				t.Fatalf("command %v not found", path)
			}
			current = next
		}
		return current
	}

	serve := find("serve")
	if !hasFlag(serve, "datadir") {
		t.Error("serve command has no --datadir flag")
	}
	if !hasFlag(serve, "port") {
		t.Error("serve command has no --port flag")
	}
	if !hasFlag(serve, "max-concurrent-jobs") {
		t.Error("serve command has no --max-concurrent-jobs flag")
	}
	if !hasFlag(serve, "max-concurrent-transfers") {
		t.Error("serve command has no --max-concurrent-transfers flag")
	}

	// auth set-password 支持 --datadir 与 --password-stdin；
	// 绝不提供 --password 明文参数。
	setPassword := find("auth", "set-password")
	if !hasFlag(setPassword, "datadir") {
		t.Error("auth set-password has no --datadir flag")
	}
	if !hasFlag(setPassword, "password-stdin") {
		t.Error("auth set-password has no --password-stdin flag")
	}
	for _, f := range setPassword.Flags {
		if slices.Contains(f.Names(), "password") {
			t.Error("auth set-password must not accept plaintext --password flag")
		}
	}

	// serve 作为主命令不应 Hidden。
	for _, c := range root.Commands {
		if c.Name == "serve" && c.Hidden {
			t.Error("serve command should not be hidden (primary command)")
		}
	}
}

// --password-stdin 从管道读取密码完成 bootstrap；密码过短被拒绝。
func TestRunSetPasswordFromStdin(t *testing.T) {
	dataDir := t.TempDir()
	ctx := context.Background()

	run := func(password string) error {
		return execSetPassword(ctx, setPasswordInput{
			DataDir:       dataDir,
			PasswordStdin: true,
		}, strings.NewReader(password), io.Discard, io.Discard)
	}

	// 过短密码被策略拒绝，且不留任何凭据。
	if err := run("short\n"); err == nil {
		t.Fatal("set-password with short password = nil, want policy error")
	}

	if err := run("automation-secret-42\n"); err != nil {
		t.Fatalf("set-password from stdin: %v", err)
	}

	// rotation：再次执行成功并替换密码。
	if err := run("automation-secret-43\n"); err != nil {
		t.Fatalf("set-password rotation: %v", err)
	}

	// 空 stdin 报错。
	if err := run("\n"); err == nil {
		t.Fatal("set-password with empty stdin = nil, want error")
	}
}

// 非 TTY 且未指定 --password-stdin 时拒绝读取，防止静默挂起。
func TestRunSetPasswordRejectsNonTTY(t *testing.T) {
	err := execSetPassword(context.Background(), setPasswordInput{
		DataDir:       t.TempDir(),
		PasswordStdin: false,
	}, strings.NewReader("ignored"), io.Discard, io.Discard)
	if err == nil {
		t.Fatal("set-password on non-TTY stdin without flag = nil, want error")
	}
	if !strings.Contains(err.Error(), "--password-stdin") {
		t.Errorf("error = %v, want hint to use --password-stdin", err)
	}
}

// set-password 默认 datadir 与 serve 一致。
func TestSetPasswordDefaultDataDir(t *testing.T) {
	root := NewCommand(fstest.MapFS{})
	for _, c := range root.Commands {
		if c.Name != "auth" {
			continue
		}
		for _, sub := range c.Commands {
			if sub.Name != "set-password" {
				continue
			}
			for _, f := range sub.Flags {
				if slices.Contains(f.Names(), "datadir") {
					if got := sub.String("datadir"); got != config.DefaultDataDir {
						t.Errorf("default datadir = %q, want %q", got, config.DefaultDataDir)
					}
				}
			}
			return
		}
	}
	t.Fatal("auth set-password command not found")
}
