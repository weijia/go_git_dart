package git

import (
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/index"
	"github.com/go-git/go-git/v5/plumbing/object"
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

// Clone clones a repository. If the initial clone fails due to malformed mode
// errors (non-standard file permissions from Gitee etc.), it falls back to:
// 1. git.PlainInit (non-bare, no checkout)
// 2. Add remote origin
// 3. Fetch all refs
// 4. Set core.fileMode=false
// 5. Checkout with force to ignore permission issues
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

	log.Printf("[go_git_dart] Clone: detected mode error, trying init + fetch fallback...")

	// Clean up any partially created directory
	os.RemoveAll(directory)

	// Step 1: Init a non-bare repository using go-git API
	log.Printf("[go_git_dart] Clone: PlainInit %s", directory)
	repo, err := git.PlainInit(directory, false)
	if err != nil {
		return fmt.Errorf("PlainInit failed: %w", err)
	}

	// Step 2: Set core.fileMode=false BEFORE fetch/checkout
	log.Printf("[go_git_dart] Clone: setting core.fileMode=false")
	if err := setCoreFileModeFalse(directory); err != nil {
		log.Printf("[go_git_dart] Clone: warning: setCoreFileModeFalse failed: %s", err.Error())
	}

	// Step 3: Create remote and fetch
	log.Printf("[go_git_dart] Clone: creating remote origin %s", url)
	_, err = repo.CreateRemote(&config.RemoteConfig{
		Name: "origin",
		URLs: []string{url},
	})
	if err != nil {
		return fmt.Errorf("CreateRemote failed: %w", err)
	}

	log.Printf("[go_git_dart] Clone: fetching from origin...")
	err = repo.Fetch(&git.FetchOptions{
		RemoteName: "origin",
		Auth:       auth,
		Tags:       git.AllTags,
	})
	if err != nil && err != git.NoErrAlreadyUpToDate {
		return fmt.Errorf("Fetch failed: %w", err)
	}

	// Step 4: Determine default branch
	defaultBranch := "main"
	refs, err := repo.References()
	if err == nil {
		refs.ForEach(func(ref *plumbing.Reference) error {
			shortName := ref.Name().Short()
			// Check for remote tracking branches like "origin/main" or "origin/master"
			if shortName == "origin/main" || shortName == "origin/master" {
				// Extract branch name: "origin/master" -> "master"
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
	// Also check remotes for HEAD symref
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
	log.Printf("[go_git_dart] Clone: default branch is %s", defaultBranch)

	// Step 5: Checkout files manually to bypass go-git's ToOSFileMode errors
	// go-git's ToOSFileMode() doesn't support non-standard modes like 0100600
	// Instead of using wt.Checkout(), we iterate the tree and write files ourselves
	log.Printf("[go_git_dart] Clone: manually checking out files to bypass ToOSFileMode")

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

	// Build the index
	idx, err := repo.Storer.Index()
	if err != nil {
		return fmt.Errorf("Index failed: %w", err)
	}
	idx.Entries = make([]*index.Entry, 0)

	// Walk the tree and write files
	err = tree.Files().ForEach(func(f *object.File) error {
		log.Printf("[go_git_dart] Clone: writing file %s (mode=%s)", f.Name, f.Mode)

		// Create parent directories
		dir := filepath.Dir(filepath.Join(directory, f.Name))
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("MkdirAll(%s) failed: %w", dir, err)
		}

		// Handle symlinks
		if f.Mode == 0120000 {
			content, err := f.Contents()
			if err != nil {
				return fmt.Errorf("read symlink %s failed: %w", f.Name, err)
			}
			if err := os.Symlink(content, filepath.Join(directory, f.Name)); err != nil {
				log.Printf("[go_git_dart] Clone: symlink %s failed: %s", f.Name, err.Error())
			}
		} else {
			// Regular file - write with 0644 permissions, ignoring git mode
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

		// Add to index
		idx.Entries = append(idx.Entries, &index.Entry{
			Name: f.Name,
			Hash: f.Hash,
			Mode: f.Mode,
		})

		return nil
	})
	if err != nil {
		return fmt.Errorf("file checkout failed: %w", err)
	}

	// Write index
	if err := repo.Storer.SetIndex(idx); err != nil {
		log.Printf("[go_git_dart] Clone: warning: SetIndex failed: %s", err.Error())
	}

	// Set HEAD to the commit
	if err := repo.Storer.SetReference(plumbing.NewHashReference("HEAD", ref.Hash())); err != nil {
		log.Printf("[go_git_dart] Clone: warning: SetReference HEAD failed: %s", err.Error())
	}

	// Step 6: Create a local branch tracking the remote
	headRef, err := repo.Head()
	if err != nil {
		log.Printf("[go_git_dart] Clone: warning: failed to get HEAD: %s", err.Error())
	} else {
		// Create branch reference using Storer
		newBranch := plumbing.NewHashReference(
			plumbing.NewBranchReferenceName(defaultBranch),
			headRef.Hash(),
		)
		err = repo.Storer.SetReference(newBranch)
		if err != nil {
			// Branch may already exist, that's fine
			log.Printf("[go_git_dart] Clone: warning: SetReference failed: %s", err.Error())
		}
	}

	log.Printf("[go_git_dart] Clone: init + fetch fallback succeeded")
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
