package exec

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLookPathInUsesTheGivenPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("executable bits differ on windows")
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, "tool")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plain"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := []string{"PATH=relative:" + dir}
	if got, err := lookPathIn(env, "tool"); err != nil || got != exe {
		t.Errorf("tool: %q %v", got, err)
	}
	if _, err := lookPathIn(env, "plain"); err == nil {
		t.Error("a file that is not executable is not a command")
	}
	if _, err := lookPathIn([]string{"PATH=/nonexistent"}, "tool"); err == nil {
		t.Error("the identity's PATH is used, not Starpoint's")
	}
	if got, err := lookPathIn(nil, exe); err != nil || got != exe {
		t.Errorf("an absolute path is used as given: %q %v", got, err)
	}
}

func TestGetenvReadsTheRunnersEnvironment(t *testing.T) {
	t.Setenv("STARPOINT_TEST_VAR", "starpoint")
	id := &Identity{Env: []string{"STARPOINT_TEST_VAR=user", "OTHER=1"}}
	if got := Getenv(RealRunner{As: id}, "STARPOINT_TEST_VAR"); got != "user" {
		t.Errorf("a dropping runner reads the user's environment, got %q", got)
	}
	if got := Getenv(RealRunner{}, "STARPOINT_TEST_VAR"); got != "starpoint" {
		t.Errorf("an ordinary runner reads Starpoint's, got %q", got)
	}
	if got := Getenv(RealRunner{As: id}, "MISSING"); got != "" {
		t.Errorf("a variable the user does not have is empty, not Starpoint's: %q", got)
	}
	c := Chooser{Choose: func(context.Context) Runner { return RealRunner{As: id} }}
	if got := Getenv(c, "OTHER"); got != "1" {
		t.Errorf("a chooser reads the chosen runner's environment, got %q", got)
	}
}

func TestChooserDelegates(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell invocation differs on windows")
	}
	calls := 0
	c := Chooser{Choose: func(context.Context) Runner { calls++; return RealRunner{} }}
	res, err := c.Run(context.Background(), "sh", "-c", "echo hi")
	if err != nil || res.Stdout != "hi\n" {
		t.Fatalf("%+v %v", res, err)
	}
	if _, err := c.LookPath("sh"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("every call asks which runner carries it, got %d", calls)
	}
}

func TestRunAsAnotherUserWithoutPrivilegeIsADropError(t *testing.T) {
	if !DropSupported {
		t.Skip("no privilege dropping on this platform")
	}
	if os.Geteuid() == 0 {
		t.Skip("root can drop privileges; see the root tests")
	}
	id := &Identity{User: "nobody", UID: 65534, GID: 65534, Env: []string{"PATH=/usr/bin:/bin"}}
	_, err := RealRunner{As: id}.Run(context.Background(), "sh", "-c", "true")
	if !IsDropError(err) {
		t.Fatalf("starting a process as another user without the right to is a DropError, got %v", err)
	}
	var de *DropError
	errors.As(err, &de)
	if de.User != "nobody" || de.Command != "sh" {
		t.Errorf("the error names the user and the command: %+v", de)
	}
}

func TestRunAsRefusesRootAndUnknownIds(t *testing.T) {
	if !DropSupported {
		t.Skip("no privilege dropping on this platform")
	}
	for _, id := range []*Identity{{User: "root", UID: 0, GID: 0}, {User: "x", UID: -1, GID: 1}, {User: "y", UID: 5, GID: -1}} {
		id.Env = []string{"PATH=/usr/bin:/bin"}
		if _, err := Command(context.Background(), id, "sh", "-c", "true"); !IsDropError(err) {
			t.Errorf("%+v: an identity that is root or unknown is refused, got %v", id, err)
		}
	}
}

func TestDropUnsupportedOffUnix(t *testing.T) {
	if DropSupported {
		t.Skip("this platform drops privileges")
	}
	_, err := Command(context.Background(), &Identity{User: "x", UID: 5, GID: 5, Env: []string{"PATH=" + os.Getenv("PATH")}}, "cmd")
	if err == nil {
		t.Fatal("expected an error")
	}
}
