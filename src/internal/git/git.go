package git

import (
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/format/index"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"github.com/go-git/go-git/v5/storage/memory"

	stdssh "golang.org/x/crypto/ssh"
)

func logMsg(msg string) {
	fmt.Fprintf(os.Stderr, "[go_git_dart] %s\n", msg)
}

// normalizeFileMode converts non-standard file modes to standard git modes.
// Some git servers (especially gitee) store files with modes like 0100600
// which are not valid git file modes and cause "unpack error" on push.
func normalizeFileMode(mode filemode.FileMode) filemode.FileMode {
	switch {
	case mode == 0120000:
		return 0120000 // symlink
	case mode == 0160000:
		return 0160000 // submodule
	case mode&0140000 != 0:
		// Any kind of regular file - normalize to 0100644 (regular file, 644 perms)
		// This covers: 0100644, 0100664, 0100755, 0100600, etc.
		return 0100644
	default:
		return mode
	}
}

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
func setCoreFileModeFalse(directory string) error {
	r, err := git.PlainOpen(directory)
	if err != nil {
		return err
	}
	cfg, err := r.Config()
	if err != nil {
		return err
	}
	cfg.Raw.Section("core").SetOption("filemode", "false")
	return r.SetConfig(cfg)
}

// Clone clones a repository with malformed mode fallback
func Clone(url string, directory string, privateKey []byte, password string) error {
	auth, err := buildAuth(url, privateKey, password)
	if err != nil {
		return err
	}

	logMsg("Clone: attempting normal clone of " + url + " to " + directory)
	_, err = git.PlainClone(directory, false, &git.CloneOptions{
		Auth: auth,
		URL:  url,
	})
	if err == nil {
		logMsg("Clone: normal clone succeeded")
		_ = setCoreFileModeFalse(directory)
		return nil
	}

	errStr := err.Error()
	logMsg("Clone: normal clone failed: " + errStr)

	// Handle empty repository
	if strings.Contains(errStr, "empty") || strings.Contains(errStr, "repository is empty") {
		logMsg("Clone: remote repository is empty, creating empty local repo")
		os.RemoveAll(directory)

		repo, err := git.PlainInit(directory, false)
		if err != nil {
			return fmt.Errorf("PlainInit failed for empty repo: %w", err)
		}

		_, err = repo.CreateRemote(&config.RemoteConfig{
			Name: "origin",
			URLs: []string{url},
		})
		if err != nil {
			return fmt.Errorf("CreateRemote failed for empty repo: %w", err)
		}

		_ = setCoreFileModeFalse(directory)
		logMsg("Clone: empty repository cloned successfully")
		return nil
	}

	isModeError := strings.Contains(errStr, "malformed") ||
		strings.Contains(errStr, "mode") ||
		strings.Contains(errStr, "filemode") ||
		strings.Contains(errStr, "permission")

	if !isModeError {
		logMsg("Clone: error is not mode-related, returning original error")
		return err
	}

	logMsg("Clone: detected mode error, trying init + fetch fallback...")
	os.RemoveAll(directory)

	logMsg("Clone: PlainInit " + directory)
	repo, err := git.PlainInit(directory, false)
	if err != nil {
		return fmt.Errorf("PlainInit failed: %w", err)
	}

	logMsg("Clone: setting core.fileMode=false")
	if err := setCoreFileModeFalse(directory); err != nil {
		logMsg("Clone: warning: setCoreFileModeFalse failed: " + err.Error())
	}

	logMsg("Clone: creating remote origin " + url)
	_, err = repo.CreateRemote(&config.RemoteConfig{
		Name: "origin",
		URLs: []string{url},
	})
	if err != nil {
		return fmt.Errorf("CreateRemote failed: %w", err)
	}

	logMsg("Clone: fetching from origin...")
	err = repo.Fetch(&git.FetchOptions{
		RemoteName: "origin",
		Auth:       auth,
		Tags:       git.AllTags,
	})
	if err != nil && err != git.NoErrAlreadyUpToDate {
		return fmt.Errorf("Fetch failed: %w", err)
	}

	defaultBranch := "main"
	refs, err := repo.References()
	if err == nil {
		refs.ForEach(func(ref *plumbing.Reference) error {
			shortName := ref.Name().Short()
			if shortName == "origin/main" || shortName == "origin/master" {
				defaultBranch = strings.TrimPrefix(shortName, "origin/")
			}
			if ref.Name() == "HEAD" && ref.Type() == plumbing.SymbolicReference {
				target := ref.Target().Short()
				if strings.HasPrefix(target, "refs/heads/") {
					defaultBranch = strings.TrimPrefix(target, "refs/heads/")
				}
			}
			return nil
		})
	}

	rem, err := repo.Remote("origin")
	if err == nil {
		listRefs, err := rem.List(&git.ListOptions{Auth: auth})
		if err == nil {
			for _, ref := range listRefs {
				if ref.Name() == "HEAD" {
					target := ref.Target().Short()
					if strings.HasPrefix(target, "refs/heads/") {
						defaultBranch = strings.TrimPrefix(target, "refs/heads/")
					}
					break
				}
			}
		}
	}
	logMsg("Clone: default branch is " + defaultBranch)

	logMsg("Clone: manually checking out files to bypass ToOSFileMode")

	remoteRef := plumbing.NewRemoteReferenceName("origin", defaultBranch)
	ref, err := repo.Reference(remoteRef, false)
	if err != nil {
		return fmt.Errorf("Reference(%s) failed: %w", remoteRef, err)
	}

	commitObj, err := repo.CommitObject(ref.Hash())
	if err != nil {
		return fmt.Errorf("CommitObject failed: %w", err)
	}

	tree, err := commitObj.Tree()
	if err != nil {
		return fmt.Errorf("Tree failed: %w", err)
	}

	idx, err := repo.Storer.Index()
	if err != nil {
		return fmt.Errorf("Index failed: %w", err)
	}
	idx.Entries = make([]*index.Entry, 0)

	err = tree.Files().ForEach(func(f *object.File) error {
		logMsg("Clone: writing file " + f.Name + " (mode=" + f.Mode.String() + ")")

		dir := filepath.Dir(filepath.Join(directory, f.Name))
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("MkdirAll(%s) failed: %w", dir, err)
		}

		if f.Mode == 0120000 {
			content, err := f.Contents()
			if err != nil {
				return fmt.Errorf("read symlink %s failed: %w", f.Name, err)
			}
			if err := os.Symlink(content, filepath.Join(directory, f.Name)); err != nil {
				logMsg("Clone: symlink " + f.Name + " failed: " + err.Error())
			}
		} else {
			destPath := filepath.Join(directory, f.Name)
			reader, err := f.Reader()
			if err != nil {
				return fmt.Errorf("Reader(%s) failed: %w", f.Name, err)
			}

			destFile, err := os.OpenFile(destPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
			if err != nil {
				reader.Close()
				return fmt.Errorf("OpenFile(%s) failed: %w", f.Name, err)
			}

			_, err = io.Copy(destFile, reader)
			reader.Close()
			destFile.Close()
			if err != nil {
				return fmt.Errorf("Write(%s) failed: %w", f.Name, err)
			}
		}

		idx.Entries = append(idx.Entries, &index.Entry{
			Name: f.Name,
			Hash: f.Hash,
			Mode: normalizeFileMode(f.Mode),
		})

		return nil
	})
	if err != nil {
		return fmt.Errorf("file checkout failed: %w", err)
	}

	if err := repo.Storer.SetIndex(idx); err != nil {
		logMsg("Clone: warning: SetIndex failed: " + err.Error())
	}

	if err := repo.Storer.SetReference(plumbing.NewHashReference("HEAD", ref.Hash())); err != nil {
		logMsg("Clone: warning: SetReference HEAD failed: " + err.Error())
	}

	headRef, err := repo.Head()
	if err != nil {
		logMsg("Clone: warning: failed to get HEAD: " + err.Error())
	} else {
		newBranch := plumbing.NewHashReference(
			plumbing.NewBranchReferenceName(defaultBranch),
			headRef.Hash(),
		)
		err = repo.Storer.SetReference(newBranch)
		if err != nil {
			logMsg("Clone: warning: SetReference failed: " + err.Error())
		}
	}

	logMsg("Clone: init + fetch fallback succeeded")
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
		// Check if this is a malformed mode error on the index
		errStr := err.Error()
		if strings.Contains(errStr, "malformed") || strings.Contains(errStr, "mode") {
			logMsg("Fetch: detected malformed mode in index, attempting to fix...")
			if fixErr := FixIndex(directory); fixErr != nil {
				return fmt.Errorf("index corrupted and fix failed: %w (original: %v)", fixErr, err)
			}
			r, err = git.PlainOpen(directory)
			if err != nil {
				return err
			}
		} else {
			return err
		}
	}
	auth, err := buildAuthForRemote(r, remote, privateKey, password)
	if err != nil {
		return err
	}
	err = r.Fetch(&git.FetchOptions{RemoteName: remote, Auth: auth})
	if err == git.NoErrAlreadyUpToDate {
		return nil
	}
	return err
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
	logMsg("Push: opening repo at " + directory)
	r, err := git.PlainOpen(directory)
	if err != nil {
		logMsg("Push: PlainOpen failed: " + err.Error())

		// Check if this is a malformed mode error on the index
		errStr := err.Error()
		if strings.Contains(errStr, "malformed") || strings.Contains(errStr, "mode") {
			logMsg("Push: detected malformed mode in index, attempting to fix...")
			if fixErr := FixIndex(directory); fixErr != nil {
				logMsg("Push: FixIndex failed: " + fixErr.Error())
				return fmt.Errorf("index corrupted and fix failed: %w (original: %v)", fixErr, err)
			}
			logMsg("Push: index fixed, retrying PlainOpen...")
			r, err = git.PlainOpen(directory)
			if err != nil {
				logMsg("Push: PlainOpen still failed after fix: " + err.Error())
				return err
			}
			logMsg("Push: PlainOpen succeeded after index fix")
		} else {
			return err
		}
	}
	logMsg("Push: building auth for remote " + remote)
	auth, err := buildAuthForRemote(r, remote, privateKey, password)
	if err != nil {
		logMsg("Push: buildAuthForRemote failed: " + err.Error())
		return err
	}

	// Log diagnostic info
	headRef, err := r.Head()
	if err != nil {
		logMsg("Push: failed to get HEAD: " + err.Error())
	} else {
		logMsg("Push: HEAD=" + headRef.String())
	}
	rem, err := r.Remote(remote)
	if err == nil {
		logMsg("Push: remote URLs=" + fmt.Sprintf("%v", rem.Config().URLs))
	}

	// Set pack.window=0 to disable delta compression.
	// Some git servers (especially gitee) have issues unpacking delta-compressed
	// packs from go-git. Disabling deltas makes the pack larger but more compatible.
	cfg, err := r.Config()
	if err != nil {
		logMsg("Push: Config failed: " + err.Error())
		return err
	}
	cfg.Pack.Window = 0
	if err := r.SetConfig(cfg); err != nil {
		logMsg("Push: SetConfig failed: " + err.Error())
		return err
	}
	logMsg("Push: set pack.window=0 (no delta compression)")

	// Check repo health before first push attempt
	if healthy, issues := CheckRepoHealth(directory); !healthy {
		logMsg("Push: repo has issues: " + issues)
		logMsg("Push: attempting auto-fix by resetting to remote and re-committing...")

		// Step 1: Soft reset to remote (keeps working directory changes)
		if resetErr := ResetSoftToRemote(directory, remote); resetErr != nil {
			logMsg("Push: ResetSoftToRemote failed: " + resetErr.Error())
			logMsg("Push: falling back to RebuildHistory...")
			if rebuildErr := RebuildHistory(directory); rebuildErr != nil {
				logMsg("Push: RebuildHistory failed: " + rebuildErr.Error())
			}
		} else {
			logMsg("Push: reset to remote successfully, working directory preserved")
		}

		// Re-open repo after reset
		r, err = git.PlainOpen(directory)
		if err != nil {
			logMsg("Push: failed to reopen repo after reset: " + err.Error())
			return err
		}

		// Re-build auth with new repo handle
		auth, err = buildAuthForRemote(r, remote, privateKey, password)
		if err != nil {
			logMsg("Push: buildAuthForRemote failed after reset: " + err.Error())
			return err
		}

		// Step 2: Try to commit any staged/working directory changes
		w, err := r.Worktree()
		if err != nil {
			logMsg("Push: Worktree failed: " + err.Error())
		} else {
			status, err := w.Status()
			if err == nil && !status.IsClean() {
				logMsg("Push: committing working directory changes...")
				if err := w.AddWithOptions(&git.AddOptions{All: true}); err != nil {
					logMsg("Push: AddWithOptions failed: " + err.Error())
				} else {
					_, commitErr := w.Commit("Auto-commit after repo health fix", &git.CommitOptions{
						Author: &object.Signature{
							Name:  "GitJournal",
							Email: "app@gitjournal.io",
							When:  time.Now(),
						},
					})
					if commitErr != nil {
						logMsg("Push: Commit failed: " + commitErr.Error())
					} else {
						logMsg("Push: committed working directory changes")
					}
				}
			} else {
				logMsg("Push: working directory is clean, no changes to commit")
			}
		}

		// Re-open repo after commit
		r, err = git.PlainOpen(directory)
		if err != nil {
			logMsg("Push: failed to reopen repo after commit: " + err.Error())
			return err
		}
		auth, err = buildAuthForRemote(r, remote, privateKey, password)
		if err != nil {
			logMsg("Push: buildAuthForRemote failed after commit: " + err.Error())
			return err
		}
	} else {
		logMsg("Push: repo health check passed")
	}

	// Try push with retry for unpack errors
	var lastErr error
	for i := 0; i < 3; i++ {
		if i > 0 {
			logMsg("Push: retrying push (attempt " + string(rune('0'+i)) + "/3) after unpack error...")
		}
		logMsg("Push: pushing to remote " + remote)
		err = r.Push(&git.PushOptions{RemoteName: remote, Auth: auth})
		if err == nil {
			logMsg("Push: push succeeded")
			return nil
		}
		if err == git.NoErrAlreadyUpToDate {
			logMsg("Push: already up to date")
			return nil
		}
		lastErr = err
		errStr := err.Error()
		logMsg("Push: push failed: " + errStr)
		// Only retry on unpack errors
		if !strings.Contains(errStr, "unpack") && !strings.Contains(errStr, "abnormal") {
			logMsg("Push: error is not unpack-related, not retrying")
			break
		}
		if i < 2 {
			logMsg("Push: waiting 2 seconds before retry...")
			time.Sleep(2 * time.Second)
		}
	}
	return lastErr
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
	if err != nil {
		return err
	}
	// Normalize file modes in index to prevent unpack errors on push
	return normalizeIndexModes(directory)
}

func Remove(directory string, path string) error {
	w, err := openWorktree(directory)
	if err != nil {
		return err
	}
	_, err = w.Remove(path)
	return err
}

// normalizeIndexModes walks through all index entries and normalizes non-standard
// file modes to prevent unpack errors when pushing to remote servers.
func normalizeIndexModes(directory string) error {
	r, err := git.PlainOpen(directory)
	if err != nil {
		return err
	}
	idx, err := r.Storer.Index()
	if err != nil {
		return err
	}
	changed := false
	for _, entry := range idx.Entries {
		newMode := normalizeFileMode(entry.Mode)
		if newMode != entry.Mode {
			entry.Mode = newMode
			changed = true
		}
	}
	if changed {
		return r.Storer.SetIndex(idx)
	}
	return nil
}

// amendLastCommit amends the last commit with the current index,
// rebuilding tree objects with normalized file modes.
func amendLastCommit(directory string) error {
	r, err := git.PlainOpen(directory)
	if err != nil {
		return fmt.Errorf("PlainOpen: %w", err)
	}

	headRef, err := r.Head()
	if err != nil {
		return fmt.Errorf("Head: %w", err)
	}

	lastCommit, err := r.CommitObject(headRef.Hash())
	if err != nil {
		return fmt.Errorf("CommitObject: %w", err)
	}

	w, err := r.Worktree()
	if err != nil {
		return fmt.Errorf("Worktree: %w", err)
	}

	_, err = w.Commit(lastCommit.Message, &git.CommitOptions{
		Author:    &lastCommit.Author,
		Committer: &lastCommit.Committer,
		Parents:   lastCommit.ParentHashes,
	})
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

// CheckRepoHealth runs git fsck to check for corrupted objects.
// Returns true if the repo is healthy, false if there are errors.
func CheckRepoHealth(directory string) (bool, string) {
	logMsg("CheckRepoHealth: checking " + directory)

	r, err := git.PlainOpen(directory)
	if err != nil {
		return false, fmt.Sprintf("PlainOpen failed: %v", err)
	}

	// Get HEAD commit
	headRef, err := r.Head()
	if err != nil {
		return false, fmt.Sprintf("Head failed: %v", err)
	}

	// Walk all objects reachable from HEAD
	commitIter, err := r.Log(&git.LogOptions{From: headRef.Hash()})
	if err != nil {
		return false, fmt.Sprintf("Log failed: %v", err)
	}
	defer commitIter.Close()

	var issues []string
	for {
		commit, err := commitIter.Next()
		if err != nil {
			break
		}

		// Check tree for each commit
		tree, err := commit.Tree()
		if err != nil {
			issues = append(issues, fmt.Sprintf("commit %s: tree error: %v", commit.Hash.String()[:7], err))
			continue
		}

		// Validate tree entries
		for _, entry := range tree.Entries {
			mode := entry.Mode
			if mode != filemode.Regular && mode != filemode.Executable &&
				mode != filemode.Symlink && mode != filemode.Dir && mode != filemode.Submodule {
				issues = append(issues, fmt.Sprintf("commit %s: bad filemode %o in %s", commit.Hash.String()[:7], mode, entry.Name))
			}
		}

		// Check tree sorting
		for i := 1; i < len(tree.Entries); i++ {
			prev := tree.Entries[i-1].Name
			curr := tree.Entries[i].Name
			if prev > curr {
				issues = append(issues, fmt.Sprintf("commit %s: tree not sorted (%s > %s)", commit.Hash.String()[:7], prev, curr))
			}
		}
	}

	if len(issues) > 0 {
		return false, strings.Join(issues, "; ")
	}
	return true, ""
}

// ResetSoftToRemote resets local branch to match remote branch,
// keeping working directory changes intact.
func ResetSoftToRemote(directory string, remote string) error {
	logMsg("ResetSoftToRemote: resetting to remote " + remote)

	r, w, err := openRepositoryAndWorktree(directory)
	if err != nil {
		return err
	}

	// Get current branch
	headRef, err := r.Head()
	if err != nil {
		return fmt.Errorf("Head: %w", err)
	}
	branchName := headRef.Name().Short()
	logMsg("ResetSoftToRemote: current branch: " + branchName)

	// Get remote branch ref
	remoteRefName := plumbing.NewRemoteReferenceName(remote, branchName)
	remoteRef, err := r.Reference(remoteRefName, true)
	if err != nil {
		return fmt.Errorf("remote ref not found: %w", err)
	}
	logMsg("ResetSoftToRemote: remote ref: " + remoteRef.Hash().String())

	// Soft reset to remote commit (keeps working directory)
	if err := w.Reset(&git.ResetOptions{
		Commit: remoteRef.Hash(),
		Mode:   git.SoftReset,
	}); err != nil {
		return fmt.Errorf("soft reset: %w", err)
	}

	// Update local branch ref to point to remote commit
	localRef := plumbing.NewHashReference(headRef.Name(), remoteRef.Hash())
	if err := r.Storer.SetReference(localRef); err != nil {
		return fmt.Errorf("update local ref: %w", err)
	}

	logMsg("ResetSoftToRemote: successfully reset to remote")
	return nil
}

// FixIndex rebuilds the git index from HEAD to fix malformed mode issues
func FixIndex(directory string) error {
	logMsg("FixIndex: attempting to fix index in " + directory)

	r, err := git.PlainOpen(directory)
	if err != nil {
		return fmt.Errorf("PlainOpen failed: %w", err)
	}

	headRef, err := r.Head()
	if err != nil {
		return fmt.Errorf("Head failed: %w", err)
	}

	commitObj, err := r.CommitObject(headRef.Hash())
	if err != nil {
		return fmt.Errorf("CommitObject failed: %w", err)
	}

	tree, err := commitObj.Tree()
	if err != nil {
		return fmt.Errorf("Tree failed: %w", err)
	}

	idx, err := r.Storer.Index()
	if err != nil {
		return fmt.Errorf("Index failed: %w", err)
	}
	idx.Entries = make([]*index.Entry, 0)

	err = tree.Files().ForEach(func(f *object.File) error {
		idx.Entries = append(idx.Entries, &index.Entry{
			Name: f.Name,
			Hash: f.Hash,
			Mode: normalizeFileMode(f.Mode),
		})
		return nil
	})
	if err != nil {
		return fmt.Errorf("file iteration failed: %w", err)
	}

	if err := r.Storer.SetIndex(idx); err != nil {
		return fmt.Errorf("SetIndex failed: %w", err)
	}

	// Also set core.fileMode=false to prevent future issues
	_ = setCoreFileModeFalse(directory)

	logMsg("FixIndex: index rebuilt successfully")
	return nil
}

// plainOpenWithFix attempts to open a repo, and if it fails due to malformed mode,
// tries to rebuild the index from HEAD and retry.
func plainOpenWithFix(directory string) (*git.Repository, error) {
	r, err := git.PlainOpen(directory)
	if err != nil {
		errStr := err.Error()
		if strings.Contains(errStr, "malformed") || strings.Contains(errStr, "mode") {
			logMsg("plainOpenWithFix: detected malformed mode, attempting FixIndex...")
			if fixErr := FixIndex(directory); fixErr != nil {
				return nil, fmt.Errorf("index corrupted and fix failed: %w (original: %v)", fixErr, err)
			}
			r, err = git.PlainOpen(directory)
			if err != nil {
				return nil, err
			}
			logMsg("plainOpenWithFix: succeeded after FixIndex")
		} else {
			return nil, err
		}
	}
	return r, nil
}

func openWorktree(directory string) (*git.Worktree, error) {
	_, w, err := openRepositoryAndWorktree(directory)
	return w, err
}

// RebuildHistory rebuilds all tree objects in the repository history,
// normalizing file modes and ensuring entries are properly sorted.
// This fixes badFilemode and treeNotSorted errors that cause
// "unpack error: unpack-objects abnormal exit" on push.
func RebuildHistory(directory string) error {
	logMsg("RebuildHistory: starting history rebuild in " + directory)

	r, err := git.PlainOpen(directory)
	if err != nil {
		return fmt.Errorf("PlainOpen: %w", err)
	}

	// Get all refs
	refs, err := r.References()
	if err != nil {
		return fmt.Errorf("References: %w", err)
	}

	// Collect all commits that need rebuilding
	commitMap := make(map[plumbing.Hash]*object.Commit)
	commitOrder := []plumbing.Hash{}

	err = refs.ForEach(func(ref *plumbing.Reference) error {
		if ref.Type() != plumbing.HashReference {
			return nil
		}
		// Walk the commit history from this ref
		commitIter, err := r.Log(&git.LogOptions{From: ref.Hash()})
		if err != nil {
			return nil // skip refs that aren't commits
		}
		defer commitIter.Close()

		for {
			commit, err := commitIter.Next()
			if err != nil {
				break
			}
			if _, exists := commitMap[commit.Hash]; !exists {
				commitMap[commit.Hash] = commit
				commitOrder = append(commitOrder, commit.Hash)
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("ref iteration: %w", err)
	}

	if len(commitOrder) == 0 {
		logMsg("RebuildHistory: no commits found")
		return nil
	}

	logMsg(fmt.Sprintf("RebuildHistory: found %d commits to rebuild", len(commitOrder)))

	// Rebuild trees in reverse order (oldest first) to preserve parent references
	// Actually we need to rebuild from oldest to newest so parent trees are already fixed
	// But commitOrder is in discovery order, not chronological. Let's sort by commit time.
	sort.Slice(commitOrder, func(i, j int) bool {
		return commitMap[commitOrder[i]].Committer.When.Before(
			commitMap[commitOrder[j]].Committer.When,
		)
	})

	// Map old tree hashes to new tree hashes
	treeMap := make(map[plumbing.Hash]plumbing.Hash)

	for _, hash := range commitOrder {
		commit := commitMap[hash]
		newTreeHash, err := rebuildTree(r, commit.TreeHash, treeMap)
		if err != nil {
			return fmt.Errorf("rebuild tree for commit %s: %w", hash.String(), err)
		}

		// Create new commit with fixed tree
		newCommit := &object.Commit{
			Author:       commit.Author,
			Committer:    commit.Committer,
			Message:      commit.Message,
			TreeHash:     newTreeHash,
			ParentHashes: commit.ParentHashes,
			PGPSignature: commit.PGPSignature,
			Encoding:     commit.Encoding,
		}

		// Encode the commit into an EncodedObject for storage
		encodedObj := r.Storer.NewEncodedObject()
		if err := newCommit.Encode(encodedObj); err != nil {
			return fmt.Errorf("encode commit: %w", err)
		}
		newHash, err := r.Storer.SetEncodedObject(encodedObj)
		if err != nil {
			return fmt.Errorf("store commit: %w", err)
		}

		// Update tree map for this commit (in case it's referenced as a parent)
		treeMap[hash] = newHash
		logMsg(fmt.Sprintf("RebuildHistory: rebuilt commit %s -> %s", hash.String()[:7], newHash.String()[:7]))
	}

	// Update all refs to point to new commits
	refs, err = r.References()
	if err != nil {
		return fmt.Errorf("References second pass: %w", err)
	}

	err = refs.ForEach(func(ref *plumbing.Reference) error {
		if ref.Type() != plumbing.HashReference {
			return nil
		}
		if newHash, ok := treeMap[ref.Hash()]; ok {
			newRef := plumbing.NewHashReference(ref.Name(), newHash)
			return r.Storer.SetReference(newRef)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("update refs: %w", err)
	}

	logMsg("RebuildHistory: history rebuild complete")
	return nil
}

// rebuildTree recursively rebuilds a tree object, normalizing file modes
// and ensuring entries are sorted by name.
func rebuildTree(r *git.Repository, treeHash plumbing.Hash, treeMap map[plumbing.Hash]plumbing.Hash) (plumbing.Hash, error) {
	if newHash, ok := treeMap[treeHash]; ok {
		return newHash, nil
	}

	tree, err := r.TreeObject(treeHash)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("TreeObject: %w", err)
	}

	// Rebuild entries: normalize modes and recursively fix subtrees
	entries := make([]object.TreeEntry, len(tree.Entries))
	for i, entry := range tree.Entries {
		entries[i] = object.TreeEntry{
			Name: entry.Name,
			Mode: normalizeFileMode(entry.Mode),
			Hash: entry.Hash,
		}

		// If this is a directory, recursively rebuild it
		if entry.Mode == filemode.Dir {
			newSubHash, err := rebuildTree(r, entry.Hash, treeMap)
			if err != nil {
				return plumbing.ZeroHash, fmt.Errorf("rebuild subtree %s: %w", entry.Name, err)
			}
			entries[i].Hash = newSubHash
		}
	}

	// Sort entries by name (required for valid tree objects)
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name < entries[j].Name
	})

	newTree := &object.Tree{Entries: entries}

	// Encode the tree into an EncodedObject for storage
	encodedObj := r.Storer.NewEncodedObject()
	if err := newTree.Encode(encodedObj); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("encode tree: %w", err)
	}
	newHash, err := r.Storer.SetEncodedObject(encodedObj)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("store tree: %w", err)
	}

	treeMap[treeHash] = newHash
	return newHash, nil
}

func openRepositoryAndWorktree(directory string) (*git.Repository, *git.Worktree, error) {
	r, err := plainOpenWithFix(directory)
	if err != nil {
		return nil, nil, err
	}
	w, err := r.Worktree()
	if err != nil {
		return nil, nil, err
	}
	return r, w, nil
}
