package bugbot

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func discoveryRepo(t *testing.T) (string, string) {
	t.Helper()
	dir := canonicalTestDir(t)
	remote := filepath.Join(dir, "published.git")
	source := filepath.Join(dir, "source")
	localGit(t, dir, "init", "--bare", remote)
	localGit(t, remote, "symbolic-ref", "HEAD", "refs/heads/main")
	localGit(t, dir, "init", "-b", "main", source)
	writeTestFile(t, filepath.Join(source, "README.md"), "project")
	localGit(t, source, "add", ".")
	localGit(t, source, "commit", "-m", "initial")
	localGit(t, source, "remote", "add", "origin", remote)
	localGit(t, source, "push", "origin", "main")
	localGit(t, source, "switch", "-c", "work-in-progress")
	return source, remote
}
func TestDiscoveryDefaultsAndPersistentWorkspace(t *testing.T) {
	source, remote := discoveryRepo(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	nested := filepath.Join(source, "nested")
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(source, "unfinished.txt"), "user edits")
	cfg, err := Discover(context.Background(), nested, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Remote != remote || cfg.Branch != "main" {
		t.Fatalf("should watch remote main, not current feature branch: %+v", cfg)
	}
	if strings.HasPrefix(cfg.Directory, source+string(os.PathSeparator)) {
		t.Fatal("managed checkout is inside user repository")
	}
	if _, err := os.Stat(cfg.Directory); !os.IsNotExist(err) {
		t.Fatal("discovery should not clone or modify repositories")
	}
	again, err := Discover(context.Background(), source, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Directory != again.Directory || cfg.StateDirectory != again.StateDirectory {
		t.Fatal("restart would lose state")
	}
	urlConfig, err := Discover(context.Background(), "file://"+remote, "")
	if err != nil || urlConfig.Branch != "main" {
		t.Fatalf("URL discovery: %+v %v", urlConfig, err)
	}
	other, err := Discover(context.Background(), source, "release/1.x")
	if err != nil {
		t.Fatal(err)
	}
	if other.Branch != "release/1.x" || other.StateDirectory == cfg.StateDirectory {
		t.Fatal("branch override did not get isolated state")
	}
	data, _ := os.ReadFile(filepath.Join(source, "unfinished.txt"))
	if string(data) != "user edits" {
		t.Fatal("discovery changed source checkout")
	}
}
func TestDiscoveryChangedAdvertisedDefaultBranch(t *testing.T) {
	_, remote := discoveryRepo(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	checkout := filepath.Join(canonicalTestDir(t), "checkout")
	localGit(t, filepath.Dir(checkout), "clone", remote, checkout)
	const cachedHEAD = "refs/remotes/origin/main"
	if got := localGit(t, checkout, "symbolic-ref", "refs/remotes/origin/HEAD"); got != cachedHEAD {
		t.Fatalf("initial cached HEAD = %q, want %q", got, cachedHEAD)
	}
	localGit(t, remote, "update-ref", "refs/heads/new-default", "refs/heads/main")
	localGit(t, remote, "symbolic-ref", "HEAD", "refs/heads/new-default")
	for _, target := range []string{checkout, "file://" + remote, remote} {
		cfg, err := Discover(context.Background(), target, "")
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Branch != "new-default" {
			t.Errorf("Discover(%q) branch = %q, want new-default", target, cfg.Branch)
		}
		cfg, err = Discover(context.Background(), target, "main")
		if err != nil || cfg.Branch != "main" {
			t.Errorf("explicit override: branch = %q, error = %v", cfg.Branch, err)
		}
	}
	if got := localGit(t, checkout, "symbolic-ref", "refs/remotes/origin/HEAD"); got != cachedHEAD {
		t.Fatalf("discovery changed cached HEAD to %q", got)
	}
}

func TestDiscoveryFromCwdAndMissingRemote(t *testing.T) {
	source, _ := discoveryRepo(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Chdir(source)
	if _, err := Discover(context.Background(), "", ""); err != nil {
		t.Fatal(err)
	}
	localGit(t, source, "remote", "remove", "origin")
	if _, err := Discover(context.Background(), "", ""); err == nil || !strings.Contains(err.Error(), "no remote") {
		t.Fatalf("missing remote error: %v", err)
	}
}
func TestAgentFallbackHonorsExplicitCommands(t *testing.T) {
	dir := canonicalTestDir(t)
	t.Setenv("PATH", dir)
	if err := os.WriteFile(filepath.Join(dir, "npx"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	if err := ResolveAgent(&cfg, true); err != nil {
		t.Fatal(err)
	}
	if strings.Join(cfg.Agent.Command, " ") != "npx --yes @agentclientprotocol/codex-acp" {
		t.Fatalf("unexpected fallback %v", cfg.Agent.Command)
	}
	cfg = DefaultConfig()
	if ResolveAgent(&cfg, false) == nil {
		t.Fatal("explicit missing executable was replaced")
	}
	if err := os.WriteFile(filepath.Join(dir, "codex-acp"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	cfg = DefaultConfig()
	if err := ResolveAgent(&cfg, true); err != nil || cfg.Agent.Command[0] != "codex-acp" {
		t.Fatalf("installed adapter not preferred: %v", err)
	}
}
