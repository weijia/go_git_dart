package git

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func TestMergeCurrentBranchFastForward(t *testing.T) {
	dir, repo, w := newTestRepo(t)
	writeFile(t, dir, "note.md", "base\n")
	base := commitFileAt(t, w, "note.md", time.Unix(1, 0))

	remoteHash := commitOnBranchAt(t, dir, w, "remote-src", base, "note.md", "remote\n", time.Unix(2, 0))
	checkoutBranch(t, w, "master")

	setBranchUpstream(t, repo, "master", "origin", plumbing.Master)
	setRemoteTrackingRef(t, repo, "origin", "master", remoteHash)

	if err := MergeCurrentBranch(dir); err != nil {
		t.Fatalf("MergeCurrentBranch() error = %v", err)
	}

	head := headCommit(t, repo)
	if got := head.Hash; got != remoteHash {
		t.Fatalf("HEAD = %s, want %s", got, remoteHash)
	}
	if got := readFile(t, dir, "note.md"); got != "remote\n" {
		t.Fatalf("file content = %q, want %q", got, "remote\n")
	}
}

func TestMergeCurrentBranchCreatesMergeCommit(t *testing.T) {
	dir, repo, w := newTestRepo(t)
	writeFile(t, dir, "note.md", "one\ntwo\n")
	base := commitFileAt(t, w, "note.md", time.Unix(1, 0))

	writeFile(t, dir, "note.md", "local\ntwo\n")
	localHash := commitFileAt(t, w, "note.md", time.Unix(2, 0))

	remoteHash := commitOnBranchAt(t, dir, w, "remote-src", base, "note.md", "one\nremote\n", time.Unix(3, 0))
	checkoutBranch(t, w, "master")

	setBranchUpstream(t, repo, "master", "origin", plumbing.Master)
	setRemoteTrackingRef(t, repo, "origin", "master", remoteHash)

	if err := MergeCurrentBranch(dir); err != nil {
		t.Fatalf("MergeCurrentBranch() error = %v", err)
	}

	head := headCommit(t, repo)
	if head.NumParents() != 2 {
		t.Fatalf("NumParents = %d, want 2", head.NumParents())
	}
	if head.ParentHashes[0] != localHash || head.ParentHashes[1] != remoteHash {
		t.Fatalf("parents = %v, want [%s %s]", head.ParentHashes, localHash, remoteHash)
	}
	if got := readFile(t, dir, "note.md"); got != "local\nremote\n" {
		t.Fatalf("file content = %q, want %q", got, "local\nremote\n")
	}
}

func TestMergeCurrentBranchPrefersLatestConflictingEdit(t *testing.T) {
	dir, repo, w := newTestRepo(t)
	writeFile(t, dir, "note.md", "base\n")
	base := commitFileAt(t, w, "note.md", time.Unix(1, 0))

	writeFile(t, dir, "note.md", "local\n")
	commitFileAt(t, w, "note.md", time.Unix(2, 0))

	remoteHash := commitOnBranchAt(t, dir, w, "remote-src", base, "note.md", "remote\n", time.Unix(3, 0))
	checkoutBranch(t, w, "master")

	setBranchUpstream(t, repo, "master", "origin", plumbing.Master)
	setRemoteTrackingRef(t, repo, "origin", "master", remoteHash)

	if err := MergeCurrentBranch(dir); err != nil {
		t.Fatalf("MergeCurrentBranch() error = %v", err)
	}

	if got := readFile(t, dir, "note.md"); got != "remote\n" {
		t.Fatalf("file content = %q, want %q", got, "remote\n")
	}
}

func TestMergeCurrentBranchPrefersLatestDelete(t *testing.T) {
	dir, repo, w := newTestRepo(t)
	writeFile(t, dir, "note.md", "base\n")
	base := commitFileAt(t, w, "note.md", time.Unix(1, 0))

	writeFile(t, dir, "note.md", "local\n")
	commitFileAt(t, w, "note.md", time.Unix(2, 0))

	remoteHash := commitDeleteOnBranchAt(t, dir, w, "remote-src", base, "note.md", time.Unix(3, 0))
	checkoutBranch(t, w, "master")

	setBranchUpstream(t, repo, "master", "origin", plumbing.Master)
	setRemoteTrackingRef(t, repo, "origin", "master", remoteHash)

	if err := MergeCurrentBranch(dir); err != nil {
		t.Fatalf("MergeCurrentBranch() error = %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "note.md")); !os.IsNotExist(err) {
		t.Fatalf("note.md stat error = %v, want not exist", err)
	}
}

func TestMergeCurrentBranchSkipsEmptyMergeCommit(t *testing.T) {
	dir, repo, w := newTestRepo(t)
	writeFile(t, dir, "note.md", "base\n")
	base := commitFileAt(t, w, "note.md", time.Unix(1, 0))

	writeFile(t, dir, "note.md", "local\n")
	localHash := commitFileAt(t, w, "note.md", time.Unix(3, 0))

	remoteHash := commitOnBranchAt(t, dir, w, "remote-src", base, "note.md", "remote\n", time.Unix(2, 0))
	checkoutBranch(t, w, "master")

	setBranchUpstream(t, repo, "master", "origin", plumbing.Master)
	setRemoteTrackingRef(t, repo, "origin", "master", remoteHash)

	if err := MergeCurrentBranch(dir); err != nil {
		t.Fatalf("MergeCurrentBranch() error = %v", err)
	}

	head := headCommit(t, repo)
	if got := head.Hash; got != localHash {
		t.Fatalf("HEAD = %s, want %s", got, localHash)
	}
	if head.NumParents() != 1 {
		t.Fatalf("NumParents = %d, want 1", head.NumParents())
	}
	if got := readFile(t, dir, "note.md"); got != "local\n" {
		t.Fatalf("file content = %q, want %q", got, "local\n")
	}
}

func TestMergeCurrentBranchFailsOnDirtyWorktree(t *testing.T) {
	dir, repo, w := newTestRepo(t)
	writeFile(t, dir, "note.md", "base\n")
	base := commitFileAt(t, w, "note.md", time.Unix(1, 0))
	remoteHash := commitOnBranchAt(t, dir, w, "remote-src", base, "note.md", "remote\n", time.Unix(2, 0))
	checkoutBranch(t, w, "master")

	setBranchUpstream(t, repo, "master", "origin", plumbing.Master)
	setRemoteTrackingRef(t, repo, "origin", "master", remoteHash)

	writeFile(t, dir, "note.md", "dirty\n")

	if err := MergeCurrentBranch(dir); err == nil {
		t.Fatal("MergeCurrentBranch() error = nil, want error")
	}
}

func TestMergeCurrentBranchFailsWithoutUpstream(t *testing.T) {
	dir, _, w := newTestRepo(t)
	writeFile(t, dir, "note.md", "base\n")
	commitFileAt(t, w, "note.md", time.Unix(1, 0))

	if err := MergeCurrentBranch(dir); err == nil {
		t.Fatal("MergeCurrentBranch() error = nil, want error")
	}
}

func commitFileAt(t *testing.T, w *gogit.Worktree, path string, when time.Time) plumbing.Hash {
	t.Helper()

	if _, err := w.Add(path); err != nil {
		t.Fatalf("Add(%q) error = %v", path, err)
	}

	hash, err := w.Commit("commit "+path, &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "GitJournal",
			Email: "gitjournal@example.com",
			When:  when,
		},
	})
	if err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	return hash
}

func commitOnBranchAt(t *testing.T, dir string, w *gogit.Worktree, branch string, base plumbing.Hash, path string, content string, when time.Time) plumbing.Hash {
	t.Helper()

	if err := w.Checkout(&gogit.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName(branch),
		Hash:   base,
		Create: true,
		Force:  true,
	}); err != nil {
		t.Fatalf("Checkout(create %s) error = %v", branch, err)
	}

	writeFile(t, dir, path, content)
	return commitFileAt(t, w, path, when)
}

func commitDeleteOnBranchAt(t *testing.T, dir string, w *gogit.Worktree, branch string, base plumbing.Hash, path string, when time.Time) plumbing.Hash {
	t.Helper()

	if err := w.Checkout(&gogit.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName(branch),
		Hash:   base,
		Create: true,
		Force:  true,
	}); err != nil {
		t.Fatalf("Checkout(create %s) error = %v", branch, err)
	}

	if err := os.Remove(filepath.Join(dir, path)); err != nil {
		t.Fatalf("Remove(%q) error = %v", path, err)
	}
	if _, err := w.Remove(path); err != nil {
		t.Fatalf("Worktree.Remove(%q) error = %v", path, err)
	}

	hash, err := w.Commit("delete "+path, &gogit.CommitOptions{
		AllowEmptyCommits: true,
		Author: &object.Signature{
			Name:  "GitJournal",
			Email: "gitjournal@example.com",
			When:  when,
		},
	})
	if err != nil {
		t.Fatalf("Commit(delete %q) error = %v", path, err)
	}

	return hash
}

func checkoutBranch(t *testing.T, w *gogit.Worktree, branch string) {
	t.Helper()

	if err := w.Checkout(&gogit.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName(branch),
		Force:  true,
	}); err != nil {
		t.Fatalf("Checkout(%s) error = %v", branch, err)
	}
}

func setBranchUpstream(t *testing.T, repo *gogit.Repository, branch string, remote string, merge plumbing.ReferenceName) {
	t.Helper()

	cfg, err := repo.Config()
	if err != nil {
		t.Fatalf("Config() error = %v", err)
	}
	cfg.Branches[branch] = &config.Branch{
		Name:   branch,
		Remote: remote,
		Merge:  merge,
	}
	if err := repo.SetConfig(cfg); err != nil {
		t.Fatalf("SetConfig() error = %v", err)
	}
}

func setRemoteTrackingRef(t *testing.T, repo *gogit.Repository, remote string, branch string, hash plumbing.Hash) {
	t.Helper()

	if err := repo.Storer.SetReference(
		plumbing.NewHashReference(plumbing.NewRemoteReferenceName(remote, branch), hash),
	); err != nil {
		t.Fatalf("SetReference() error = %v", err)
	}
}

func headCommit(t *testing.T, repo *gogit.Repository) *object.Commit {
	t.Helper()

	head, err := repo.Head()
	if err != nil {
		t.Fatalf("Head() error = %v", err)
	}
	commit, err := repo.CommitObject(head.Hash())
	if err != nil {
		t.Fatalf("CommitObject() error = %v", err)
	}

	return commit
}
