package bugbot

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestConfiguredLocalMirror(t *testing.T) {
	for _, name := range []string{"published.git", "published:mirror.git"} {
		t.Run(name, func(t *testing.T) {
			for _, cwd := range []string{"config directory", "other directory"} {
				t.Run(cwd, func(t *testing.T) {
					_, remote := discoveryRepo(t)
					base := filepath.Dir(remote)
					renamed := filepath.Join(base, name)
					if remote != renamed {
						if err := os.Rename(remote, renamed); err != nil {
							t.Fatal(err)
						}
					}
					remote = renamed
					if cwd == "other directory" {
						t.Chdir(canonicalTestDir(t))
					} else {
						t.Chdir(base)
					}
					p := filepath.Join(base, "config.json")
					writeTestFile(t, p, `{"remote":"./`+name+`","branch":"main","github":{"repo":"o/r"}}`)
					cfg, err := ReadConfig(p)
					if err != nil {
						t.Fatal(err)
					}
					g := checkout{cfg}
					if err := g.open(context.Background()); err != nil {
						t.Fatalf("open configured mirror: %v", err)
					}
					if cfg.Remote != remote {
						t.Fatalf("remote = %q, want %q", cfg.Remote, remote)
					}
					if err := g.open(context.Background()); err != nil {
						t.Fatalf("reopen configured mirror: %v", err)
					}
				})
			}
		})
	}
}

func TestConfigRemotePaths(t *testing.T) {
	base := canonicalTestDir(t)
	localRemotes := []string{"mirror.git", "./mirror.git", "../mirror.git", "./published:mirror.git", "../published:mirror.git", "mirrors/published:mirror.git"}
	for _, remote := range append(localRemotes, filepath.Join(base, "mirror.git"), "https://github.com/o/r.git", "ssh://git@github.com/o/r.git", "git@github.com:o/r.git", "github.com:o/r.git", "file:///tmp/mirror.git", "ext::helper") {
		t.Run(remote, func(t *testing.T) {
			raw, err := json.Marshal(map[string]string{"remote": remote})
			if err != nil {
				t.Fatal(err)
			}
			p := filepath.Join(base, "config.json")
			writeTestFile(t, p, string(raw))
			cfg, err := ReadConfig(p)
			if err != nil {
				t.Fatal(err)
			}
			want := remote
			for _, local := range localRemotes {
				if remote == local {
					want = filepath.Join(base, remote)
				}
			}
			if cfg.Remote != want {
				t.Fatalf("remote = %q, want %q", cfg.Remote, want)
			}
		})
	}
}

func TestStrictConfigAndPaths(t *testing.T) {
	p := filepath.Join(canonicalTestDir(t), "config.json")
	for _, raw := range []string{
		`{"remote":"","github":{"repo":"o/r"}}`,
		`{"remote":"-mirror.git","github":{"repo":"o/r"}}`,
		`{"remote":"https://github.com/o/r.git","unknown":true}`,
		`{"remote":"https://github.com/o/r.git"} {}`,
		`{"remote":"https://github.com/o/r.git","max_issues":0}`,
		`{"remote":"https://github.com/o/r.git","max_issues":21}`,
		`{"remote":"https://github.com/o/r.git","poll":"0s"}`,
		`{"remote":"https://github.com/o/r.git","directory":"checkout","state_directory":"checkout-scans/sub"}`,
	} {
		writeTestFile(t, p, raw)
		if _, err := ReadConfig(p); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	writeTestFile(t, p, `{"remote":"https://github.com/o/r.git"}`)
	c, err := ReadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.GitHubRepo() != "o/r" || !filepath.IsAbs(c.Directory) || c.MaxIssues != 3 || c.DryRun {
		t.Fatalf("bad defaults %+v", c)
	}
	if err := os.Symlink("checkout", filepath.Join(filepath.Dir(p), "alias")); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, p, `{"remote":"https://github.com/o/r.git","directory":"checkout","state_directory":"alias/state"}`)
	if _, err := ReadConfig(p); err == nil {
		t.Fatal("dangling symlink overlap accepted")
	}
}
func TestRepositoryLockAcrossBranches(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", canonicalTestDir(t))
	c := DefaultConfig()
	c.Remote = "https://github.com/o/r.git"
	dir := canonicalTestDir(t)
	c.Directory = filepath.Join(dir, "a")
	c.StateDirectory = filepath.Join(dir, "a-state")
	unlock, err := lockConfig(c)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	c.Branch = "other"
	c.Directory = filepath.Join(dir, "b")
	c.StateDirectory = filepath.Join(dir, "b-state")
	if release, err := lockConfig(c); err == nil {
		release()
		t.Fatal("same repository can run concurrently across branches")
	}
}
