package git

import (
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"os/exec"
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
// errors from non-standard file permissions on Gitee), it falls back to:
// 1. Create a new non-bare git repo in target directory
// 2. Fetch from remote
// 3. Set core.fileMode=false
// 4. Checkout files with force mode to ignore permission errors
func Clone(url string, directory string, privateKey []byte, password string) error {
	auth, err := buildAuth(url, privateKey, password)
	if err != nil {
		return err
	}

	// Try normal clone first
	log.Printf("[go_git_dart] Clone: attempting normal clone of %s to %s", url, directory)
	_, err = git.PlainClone(directory, false, &git.CloneOptions{
		Auth: auth,
		URL:  url,
	})
	if err == nil {
		log.Printf("[go_git_dart] Clone: normal clone succeeded")
		// Clone succeeded, set core.fileMode=false for future operations
		_ = setCoreFileModeFalse(directory)
		return nil
	}

	// Normal clone failed
	errStr := err.Error()
	log.Printf("[go_git_dart] Clone: normal clone failed: %s", errStr)

	// Check if this is a mode-related error that we can retry
	isModeError := strings.Contains(errStr, "malformed") ||
		strings.Contains(errStr, "mode") ||
		strings.Contains(errStr, "filemode") ||
		strings.Contains(errStr, "permission")

	if !isModeError {
		log.Printf("[go_git_dart] Clone: error is not mode-related, returning original error")
		return err
	}

	log.Printf("[go_git_dart] Clone: detected mode error, trying manual init + fetch fallback...")

	// Clean up any partially created directory
	os.RemoveAll(directory)

	// Use git's native error handling for permission issues
	// by using exec.Command to run git commands directly
	return cloneWithGitCommand(url, directory, privateKey, password)
}

// cloneWithGitCommand uses git command line to clone, bypassing go-git's checkout issues
func cloneWithGitCommand(url, directory string, privateKey []byte, password string) error {
	// First init a repo
	log.Printf("[go_git_dart] Clone: git init %s", directory)
	if err := runGit("init", directory); err != nil {
		return fmt.Errorf("git init failed: %w", err)
	}

	// Set core.fileMode=false early
	log.Printf("[go_git_dart] Clone: setting core.fileMode=false")
	_ = runGitInDir(directory, "config", "core.fileMode", "false")

	// Add remote
	log.Printf("[go_git_dart] Clone: adding remote origin %s", url)
	if err := runGitInDir(directory, "remote", "add", "origin", url); err != nil {
		return fmt.Errorf("git remote add failed: %w", err)
	}

	// Fetch all refs
	log.Printf("[go_git_dart] Clone: fetching origin")
	if err := runGitInDirWithAuth(directory, privateKey, password, "fetch", "--all"); err != nil {
		return fmt.Errorf("git fetch failed: %w", err)
	}

	// Get default branch (usually main or master)
	defaultBranch := "main"
	if headErr := runGitInDirWithAuth(directory, privateKey, password, "rev-parse", "--symbolic-full-name", "origin/HEAD"); headErr == nil {
		// Parse output to get branch name
		out, _ := runGitInDirWithAuthOutput(directory, privateKey, password, "symbolic-ref", "refs/remotes/origin/HEAD")
		if len(out) > 0 {
			// Extract branch name from "refs/remotes/origin/main"
			parts := strings.Split(out, "/")
			if len(parts) >= 3 {
				defaultBranch = parts[len(parts)-1]
			}
		}
	} else {
		// Try master as fallback
		if err := runGitInDirWithAuth(directory, privateKey, password, "rev-parse", "--symbolic-full-name", "origin/master"); err == nil {
			defaultBranch = "master"
		}
	}
	log.Printf("[go_git_dart] Clone: default branch is %s", defaultBranch)

	// Checkout the default branch
	log.Printf("[go_git_dart] Clone: checking out %s", defaultBranch)
	if err := runGitInDirWithAuth(directory, privateKey, password, "checkout", "-f", defaultBranch); err != nil {
		return fmt.Errorf("git checkout failed: %w", err)
	}

	// Set HEAD to track the remote branch
	_ = runGitInDir(directory, "branch", "--set-upstream-to", fmt.Sprintf("origin/%s", defaultBranch))

	log.Printf("[go_git_dart] Clone: manual clone succeeded")
	return nil
}

// runGit runs a git command
func runGit(args ...string) error {
	cmd := exec.Command("git", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %v failed: %s", args, string(output))
	}
	return nil
}

// runGitInDir runs git command in specified directory
func runGitInDir(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("[go_git_dart] git %v in %s failed: %s", args, dir, string(output))
		return fmt.Errorf("git %v failed: %s", args, string(output))
	}
	return nil
}

// runGitInDirWithAuth runs git command with SSH key authentication
func runGitInDirWithAuth(dir string, privateKey []byte, password string, args ...string) error {
	_, err := runGitInDirWithAuthOutput(dir, privateKey, password, args...)
	return err
}

// runGitInDirWithAuthOutput runs git command and returns output
func runGitInDirWithAuthOutput(dir string, privateKey []byte, password string, args ...string) (string, error) {
	// Create a temp file for the private key
	tmpKeyFile, err := os.CreateTemp("", "git_key_*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmpKeyFile.Name())
	defer tmpKeyFile.Close()

	if _, err := tmpKeyFile.Write(privateKey); err != nil {
		return "", err
	}
	tmpKeyFile.Chmod(0600)

	// Set up SSH command to use the key
	sshCmd := fmt.Sprintf("ssh -i %s -o StrictHostKeyChecking=no", tmpKeyFile.Name())
	if password != "" {
		// SSH key with password is tricky - use GIT_ASKPASS or expect issues
		log.Printf("[go_git_dart] WARNING: SSH key has password, may fail")
	}

	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_SSH_COMMAND="+sshCmd,
		"GIT_TERMINAL_PROMPT=0",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("[go_git_dart] git %v in %s with auth failed: %s", args, dir, string(output))
		return string(output), fmt.Errorf("git %v failed: %s", args, string(output))
	}
	return string(output), nil
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
