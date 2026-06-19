package git

import (
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
		// On first unpack failure, normalize index modes and amend last commit
		if i == 0 {
			logMsg("Push: unpack error detected, normalizing index and amending last commit...")
			if fixErr := FixIndex(directory); fixErr != nil {
				logMsg("Push: FixIndex failed: " + fixErr.Error())
			} else {
				// Amend the last commit to rebuild tree objects with normalized modes
				if amendErr := amendLastCommit(directory); amendErr != nil {
					logMsg("Push: amendLastCommit failed: " + amendErr.Error())
				} else {
					logMsg("Push: commit amended with normalized modes")
				}
				// Re-open repo after fix
				r.Close()
				r, err = git.PlainOpen(directory)
				if err != nil {
					logMsg("Push: failed to reopen repo after FixIndex: " + err.Error())
					break
				}
				// Re-build auth since we have a new repo handle
				auth, err = buildAuthForRemote(r, remote, privateKey, password)
				if err != nil {
					logMsg("Push: buildAuthForRemote failed after FixIndex: " + err.Error())
					break
				}
			}
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
