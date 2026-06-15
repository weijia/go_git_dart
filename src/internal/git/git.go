package git

/*
#cgo LDFLAGS: -landroid
#include <android/log.h>
#include <stdlib.h>
*/
import "C"
import (
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

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

// androidLog writes a debug message to Android logcat
func androidLog(tag, msg string) {
	ctag := C.CString(tag)
	cmsg := C.CString(msg)
	defer C.free(unsafe.Pointer(ctag))
	defer C.free(unsafe.Pointer(cmsg))
	C.__android_log_write(C.ANDROID_LOG_DEBUG, ctag, cmsg)
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

	androidLog("go_git_dart", "Clone: attempting normal clone of "+url+" to "+directory)
	_, err = git.PlainClone(directory, false, &git.CloneOptions{
		Auth: auth,
		URL:  url,
	})
	if err == nil {
		androidLog("go_git_dart", "Clone: normal clone succeeded")
		_ = setCoreFileModeFalse(directory)
		return nil
	}

	errStr := err.Error()
	androidLog("go_git_dart", "Clone: normal clone failed: "+errStr)

	isModeError := strings.Contains(errStr, "malformed") ||
		strings.Contains(errStr, "mode") ||
		strings.Contains(errStr, "filemode") ||
		strings.Contains(errStr, "permission")

	if !isModeError {
		androidLog("go_git_dart", "Clone: error is not mode-related, returning original error")
		return err
	}

	androidLog("go_git_dart", "Clone: detected mode error, trying init + fetch fallback...")
	os.RemoveAll(directory)

	androidLog("go_git_dart", "Clone: PlainInit "+directory)
	repo, err := git.PlainInit(directory, false)
	if err != nil {
		return fmt.Errorf("PlainInit failed: %w", err)
	}

	androidLog("go_git_dart", "Clone: setting core.fileMode=false")
	if err := setCoreFileModeFalse(directory); err != nil {
		androidLog("go_git_dart", "Clone: warning: setCoreFileModeFalse failed: "+err.Error())
	}

	androidLog("go_git_dart", "Clone: creating remote origin "+url)
	_, err = repo.CreateRemote(&config.RemoteConfig{
		Name: "origin",
		URLs: []string{url},
	})
	if err != nil {
		return fmt.Errorf("CreateRemote failed: %w", err)
	}

	androidLog("go_git_dart", "Clone: fetching from origin...")
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
	androidLog("go_git_dart", "Clone: default branch is "+defaultBranch)

	androidLog("go_git_dart", "Clone: manually checking out files to bypass ToOSFileMode")

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
		androidLog("go_git_dart", "Clone: writing file "+f.Name+" (mode="+f.Mode.String()+")")

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
				androidLog("go_git_dart", "Clone: symlink "+f.Name+" failed: "+err.Error())
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
			Mode: f.Mode,
		})

		return nil
	})
	if err != nil {
		return fmt.Errorf("file checkout failed: %w", err)
	}

	if err := repo.Storer.SetIndex(idx); err != nil {
		androidLog("go_git_dart", "Clone: warning: SetIndex failed: "+err.Error())
	}

	if err := repo.Storer.SetReference(plumbing.NewHashReference("HEAD", ref.Hash())); err != nil {
		androidLog("go_git_dart", "Clone: warning: SetReference HEAD failed: "+err.Error())
	}

	headRef, err := repo.Head()
	if err != nil {
		androidLog("go_git_dart", "Clone: warning: failed to get HEAD: "+err.Error())
	} else {
		newBranch := plumbing.NewHashReference(
			plumbing.NewBranchReferenceName(defaultBranch),
			headRef.Hash(),
		)
		err = repo.Storer.SetReference(newBranch)
		if err != nil {
			androidLog("go_git_dart", "Clone: warning: SetReference failed: "+err.Error())
		}
	}

	androidLog("go_git_dart", "Clone: init + fetch fallback succeeded")
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
	return err
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

func MergeCurrentBranch(directory string) error {
	r, err := git.PlainOpen(directory)
	if err != nil {
		return err
	}
	w, err := r.Worktree()
	if err != nil {
		return err
	}
	head, err := r.Head()
	if err != nil {
		return err
	}
	branchName := head.Name().Short()
	refName := plumbing.NewRemoteReferenceName("origin", branchName)
	_, err = r.Reference(refName, true)
	if err != nil {
		return err
	}
	return w.Merge(&git.MergeOptions{
		Strategy: git.FastForwardMerge,
	})
}
