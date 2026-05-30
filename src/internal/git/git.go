package git

import (
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"github.com/go-git/go-git/v5/storage/memory"

	stdssh "golang.org/x/crypto/ssh"
)

func buildAuth(url string, privateKey []byte, password string) (transport.AuthMethod, error) {
	ep, err := transport.NewEndpoint(url)
	if err != nil {
		return nil, err
	}

	publicKeys, err := ssh.NewPublicKeys(ep.User, privateKey, password)
	if err != nil {
		return nil, err
	}
	publicKeys.HostKeyCallback = stdssh.InsecureIgnoreHostKey()

	return publicKeys, nil
}

// setCoreFileModeFalse opens the repo at directory and sets core.fileMode to false
// in its local config to avoid "malformed mode" errors on repos with non-standard
// file permissions (e.g. from Gitee).
func setCoreFileModeFalse(directory string) error {
	r, err := git.PlainOpen(directory)
	if err != nil {
		return err
	}
	cfg, err := r.Config()
	if err != nil {
		return err
	}
	// Set core.fileMode to false
	cfg.Raw.Section("core").SetOption("filemode", "false")
	return r.SetConfig(cfg)
}

// Clone clones a repository. If the initial clone fails (e.g. due to "malformed mode"
// errors from non-standard file permissions on Gitee), it falls back to cloning
// as a bare repository, setting core.fileMode=false, then converting to a normal
// repository with a worktree checkout.
func Clone(url string, directory string, privateKey []byte, password string) error {
	auth, err := buildAuth(url, privateKey, password)
	if err != nil {
		return err
	}

	// Try normal clone first
	_, err = git.PlainClone(directory, false, &git.CloneOptions{
		Auth: auth,
		URL:  url,
	})
	if err == nil {
		// Clone succeeded, set core.fileMode=false for future operations
		_ = setCoreFileModeFalse(directory)
		return nil
	}

	// Normal clone failed - try fallback: bare clone + manual checkout
	// This handles "malformed mode" errors from repos with non-standard permissions
	errStr := err.Error()
	if !strings.Contains(errStr, "malformed") && !strings.Contains(errStr, "filemode") && !strings.Contains(errStr, "mode") {
		// Not a mode-related error, return original error
		return err
	}

	// Clean up any partially created directory
	os.RemoveAll(directory)

	// Step 1: Clone as bare repository (no checkout, so no mode issues)
	repo, err := git.PlainClone(directory, true, &git.CloneOptions{
		Auth: auth,
		URL:  url,
	})
	if err != nil {
		return fmt.Errorf("bare clone failed: %w", err)
	}

	// Step 2: Set core.fileMode=false before checkout
	if err := setCoreFileModeFalse(directory); err != nil {
		return fmt.Errorf("failed to set core.fileMode=false: %w", err)
	}

	// Step 3: Convert from bare to non-bare by adding a worktree
	// go-git doesn't have a direct "unbare" function, so we use Worktree checkout
	wt, err := repo.Worktree()
	if err == nil {
		// If we can get a worktree, the repo is already non-bare (shouldn't happen)
		return nil
	}

	// For bare repos, we need to re-open as non-bare.
	// go-git requires us to write the config to set bare = false
	cfg, err := repo.Config()
	if err != nil {
		return fmt.Errorf("failed to read config: %w", err)
	}
	cfg.Core.IsBare = false
	if err := repo.SetConfig(cfg); err != nil {
		return fmt.Errorf("failed to set bare=false: %w", err)
	}

	// Now open as non-bare and checkout
	repo2, err := git.PlainOpen(directory)
	if err != nil {
		return fmt.Errorf("failed to reopen repo: %w", err)
	}

	wt, err = repo2.Worktree()
	if err != nil {
		return fmt.Errorf("failed to get worktree: %w", err)
	}

	// Get HEAD reference for checkout
	head, err := repo2.Head()
	if err != nil {
		return fmt.Errorf("failed to get HEAD: %w", err)
	}

	err = wt.Checkout(&git.CheckoutOptions{
		Hash: head.Hash(),
	})
	if err != nil {
		return fmt.Errorf("checkout failed: %w", err)
	}

	return nil
}

func buildAuthForRemote(repo *git.Repository, remoteName string, privateKey []byte, password string) (transport.AuthMethod, error) {
	rem, err := repo.Remote(remoteName)
	if err != nil {
		return nil, err
	}

	urls := rem.Config().URLs
	if len(urls) == 0 {
		return nil, fmt.Errorf("no remote url")
	}

	return buildAuth(urls[0], privateKey, password)
}

func Fetch(remote string, directory string, privateKey []byte, password string) error {
	r, err := git.PlainOpen(directory)
	if err != nil {
		return err
	}

	auth, err := buildAuthForRemote(r, remote, privateKey, password)
	if err != nil {
		return err
	}

	err = r.Fetch(&git.FetchOptions{RemoteName: remote, Auth: auth})
	if err == git.NoErrAlreadyUpToDate {
		return nil
	}

	if err != nil {
		return err
	}

	return nil
}

func Pull(remote string, directory string, privateKey []byte, password string) error {
	return pull(Fetch, MergeCurrentBranch, remote, directory, privateKey, password)
}

func pull(
	fetchFn func(string, string, []byte, string) error,
	mergeFn func(string) error,
	remote string,
	directory string,
	privateKey []byte,
	password string,
) error {
	if err := fetchFn(remote, directory, privateKey, password); err != nil {
		return err
	}

	return mergeFn(directory)
}

func Push(remote string, directory string, privateKey []byte, password string) error {
	r, err := git.PlainOpen(directory)
	if err != nil {
		return err
	}

	auth, err := buildAuthForRemote(r, remote, privateKey, password)
	if err != nil {
		return err
	}

	err = r.Push(&git.PushOptions{RemoteName: remote, Auth: auth})
	if err == git.NoErrAlreadyUpToDate {
		return nil
	}

	if err != nil {
		return err
	}

	return nil
}

func DefaultBranch(remoteUrl string, privateKey []byte, password string) (string, error) {
	auth, err := buildAuth(remoteUrl, privateKey, password)
	if err != nil {
		return "", err
	}

	remote := git.NewRemote(memory.NewStorage(), &config.RemoteConfig{
		Name: "origin",
		URLs: []string{remoteUrl},
	})

	refs, err := remote.List(&git.ListOptions{Auth: auth})
	if err != nil {
		return "", err
	}

	defaultBranch := ""
	for _, ref := range refs {
		if ref.Name() == "HEAD" {
			defaultBranch = ref.Target().Short()
			break
		}
	}

	return defaultBranch, nil
}

func Add(directory string, path string) error {
	w, err := openWorktree(directory)
	if err != nil {
		return err
	}

	_, err = w.Add(path)
	return err
}

func Remove(directory string, path string) error {
	w, err := openWorktree(directory)
	if err != nil {
		return err
	}

	_, err = w.Remove(path)
	return err
}

func ResetHard(directory string) error {
	_, w, err := openRepositoryAndWorktree(directory)
	if err != nil {
		return err
	}

	return w.Reset(&git.ResetOptions{Mode: git.HardReset})
}

func ResetHardTo(directory string, commitHash string) error {
	_, w, err := openRepositoryAndWorktree(directory)
	if err != nil {
		return err
	}

	commitHash = strings.TrimSpace(commitHash)
	if len(commitHash) != 40 {
		return fmt.Errorf("commit hash must be 40 hexadecimal characters")
	}
	if _, err := hex.DecodeString(commitHash); err != nil {
		return fmt.Errorf("invalid commit hash: %w", err)
	}

	return w.Reset(&git.ResetOptions{
		Commit: plumbing.NewHash(commitHash),
		Mode:   git.HardReset,
	})
}

func Checkout(directory string, branch string) error {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return fmt.Errorf("branch name is required")
	}

	r, w, err := openRepositoryAndWorktree(directory)
	if err != nil {
		return err
	}

	branchRef := plumbing.NewBranchReferenceName(branch)
	if _, err := r.Reference(branchRef, true); err != nil {
		return err
	}

	return w.Checkout(&git.CheckoutOptions{Branch: branchRef})
}

func openWorktree(directory string) (*git.Worktree, error) {
	_, w, err := openRepositoryAndWorktree(directory)
	return w, err
}

func openRepositoryAndWorktree(directory string) (*git.Repository, *git.Worktree, error) {
	r, err := git.PlainOpen(directory)
	if err != nil {
		return nil, nil, err
	}

	w, err := r.Worktree()
	if err != nil {
		return nil, nil, err
	}

	return r, w, nil
}
