package main

import (
	"fmt"
	"os"

	git "github.com/gitjournal/go-git-dart/internal/git"
	keygen "github.com/gitjournal/go-git-dart/internal/keygen"

	/*
	   #include <stdlib.h>
	*/
	"C"
	"unsafe"
)

//export GitClone
func GitClone(url *C.char, directory *C.char, privateKey *C.char, privateKeyLen C.int, password *C.char) *C.char {
	fmt.Fprintf(os.Stderr, "[go_git_dart] GitClone called: url=%s dir=%s\n", C.GoString(url), C.GoString(directory))
	err := git.Clone(C.GoString(url), C.GoString(directory), C.GoBytes(unsafe.Pointer(privateKey), privateKeyLen), C.GoString(password))
	if err != nil {
		fmt.Fprintf(os.Stderr, "[go_git_dart] GitClone error: %v\n", err)
		return C.CString(err.Error())
	}

	fmt.Fprintf(os.Stderr, "[go_git_dart] GitClone success\n")
	return nil
}

//export GitFetch
func GitFetch(remote *C.char, directory *C.char, privateKey *C.char, privateKeyLen C.int, password *C.char) *C.char {
	fmt.Fprintf(os.Stderr, "[go_git_dart] GitFetch called: remote=%s dir=%s\n", C.GoString(remote), C.GoString(directory))
	err := git.Fetch(C.GoString(remote), C.GoString(directory), C.GoBytes(unsafe.Pointer(privateKey), privateKeyLen), C.GoString(password))
	if err != nil {
		fmt.Fprintf(os.Stderr, "[go_git_dart] GitFetch error: %v\n", err)
		return C.CString(err.Error())
	}

	fmt.Fprintf(os.Stderr, "[go_git_dart] GitFetch success\n")
	return nil
}

//export GitPull
func GitPull(remote *C.char, directory *C.char, privateKey *C.char, privateKeyLen C.int, password *C.char) *C.char {
	fmt.Fprintf(os.Stderr, "[go_git_dart] GitPull called: remote=%s dir=%s\n", C.GoString(remote), C.GoString(directory))
	err := git.Pull(C.GoString(remote), C.GoString(directory), C.GoBytes(unsafe.Pointer(privateKey), privateKeyLen), C.GoString(password))
	if err != nil {
		fmt.Fprintf(os.Stderr, "[go_git_dart] GitPull error: %v\n", err)
		return C.CString(err.Error())
	}

	fmt.Fprintf(os.Stderr, "[go_git_dart] GitPull success\n")
	return nil
}

//export GitPush
func GitPush(remote *C.char, directory *C.char, privateKey *C.char, privateKeyLen C.int, password *C.char) *C.char {
	fmt.Fprintf(os.Stderr, "[go_git_dart] GitPush called: remote=%s dir=%s\n", C.GoString(remote), C.GoString(directory))
	err := git.Push(C.GoString(remote), C.GoString(directory), C.GoBytes(unsafe.Pointer(privateKey), privateKeyLen), C.GoString(password))
	if err != nil {
		fmt.Fprintf(os.Stderr, "[go_git_dart] GitPush error: %v\n", err)
		return C.CString(err.Error())
	}

	fmt.Fprintf(os.Stderr, "[go_git_dart] GitPush success\n")
	return nil
}

//export GitDefaultBranch
func GitDefaultBranch(remoteUrl *C.char, privateKey *C.char, privateKeyLen C.int, password *C.char, outputBranchName **C.char) *C.char {
	fmt.Fprintf(os.Stderr, "[go_git_dart] GitDefaultBranch called: url=%s\n", C.GoString(remoteUrl))
	val, err := git.DefaultBranch(C.GoString(remoteUrl), C.GoBytes(unsafe.Pointer(privateKey), privateKeyLen), C.GoString(password))
	if err != nil {
		fmt.Fprintf(os.Stderr, "[go_git_dart] GitDefaultBranch error: %v\n", err)
		return C.CString(err.Error())
	}

	fmt.Fprintf(os.Stderr, "[go_git_dart] GitDefaultBranch success: %s\n", val)
	*outputBranchName = C.CString(val)
	return nil
}

//export GitAdd
func GitAdd(directory *C.char, path *C.char) *C.char {
	fmt.Fprintf(os.Stderr, "[go_git_dart] GitAdd called: dir=%s path=%s\n", C.GoString(directory), C.GoString(path))
	err := git.Add(C.GoString(directory), C.GoString(path))
	if err != nil {
		fmt.Fprintf(os.Stderr, "[go_git_dart] GitAdd error: %v\n", err)
		return C.CString(err.Error())
	}

	fmt.Fprintf(os.Stderr, "[go_git_dart] GitAdd success\n")
	return nil
}

//export GitRemove
func GitRemove(directory *C.char, path *C.char) *C.char {
	fmt.Fprintf(os.Stderr, "[go_git_dart] GitRemove called: dir=%s path=%s\n", C.GoString(directory), C.GoString(path))
	err := git.Remove(C.GoString(directory), C.GoString(path))
	if err != nil {
		fmt.Fprintf(os.Stderr, "[go_git_dart] GitRemove error: %v\n", err)
		return C.CString(err.Error())
	}

	fmt.Fprintf(os.Stderr, "[go_git_dart] GitRemove success\n")
	return nil
}

//export GitResetHard
func GitResetHard(directory *C.char) *C.char {
	fmt.Fprintf(os.Stderr, "[go_git_dart] GitResetHard called: dir=%s\n", C.GoString(directory))
	err := git.ResetHard(C.GoString(directory))
	if err != nil {
		fmt.Fprintf(os.Stderr, "[go_git_dart] GitResetHard error: %v\n", err)
		return C.CString(err.Error())
	}

	fmt.Fprintf(os.Stderr, "[go_git_dart] GitResetHard success\n")
	return nil
}

//export GitResetHardTo
func GitResetHardTo(directory *C.char, commitHash *C.char) *C.char {
	fmt.Fprintf(os.Stderr, "[go_git_dart] GitResetHardTo called: dir=%s hash=%s\n", C.GoString(directory), C.GoString(commitHash))
	err := git.ResetHardTo(C.GoString(directory), C.GoString(commitHash))
	if err != nil {
		fmt.Fprintf(os.Stderr, "[go_git_dart] GitResetHardTo error: %v\n", err)
		return C.CString(err.Error())
	}

	fmt.Fprintf(os.Stderr, "[go_git_dart] GitResetHardTo success\n")
	return nil
}

//export GitCheckout
func GitCheckout(directory *C.char, branch *C.char) *C.char {
	fmt.Fprintf(os.Stderr, "[go_git_dart] GitCheckout called: dir=%s branch=%s\n", C.GoString(directory), C.GoString(branch))
	err := git.Checkout(C.GoString(directory), C.GoString(branch))
	if err != nil {
		fmt.Fprintf(os.Stderr, "[go_git_dart] GitCheckout error: %v\n", err)
		return C.CString(err.Error())
	}

	fmt.Fprintf(os.Stderr, "[go_git_dart] GitCheckout success\n")
	return nil
}

//export GitMergeCurrentBranch
func GitMergeCurrentBranch(directory *C.char) *C.char {
	fmt.Fprintf(os.Stderr, "[go_git_dart] GitMergeCurrentBranch called: dir=%s\n", C.GoString(directory))
	err := git.MergeCurrentBranch(C.GoString(directory))
	if err != nil {
		fmt.Fprintf(os.Stderr, "[go_git_dart] GitMergeCurrentBranch error: %v\n", err)
		return C.CString(err.Error())
	}

	fmt.Fprintf(os.Stderr, "[go_git_dart] GitMergeCurrentBranch success\n")
	return nil
}

//export GitFixIndex
func GitFixIndex(directory *C.char) *C.char {
	fmt.Fprintf(os.Stderr, "[go_git_dart] GitFixIndex called: dir=%s\n", C.GoString(directory))
	err := git.FixIndex(C.GoString(directory))
	if err != nil {
		fmt.Fprintf(os.Stderr, "[go_git_dart] GitFixIndex error: %v\n", err)
		return C.CString(err.Error())
	}

	fmt.Fprintf(os.Stderr, "[go_git_dart] GitFixIndex success\n")
	return nil
}

//export GitRebuildHistory
func GitRebuildHistory(directory *C.char) *C.char {
	fmt.Fprintf(os.Stderr, "[go_git_dart] GitRebuildHistory called: dir=%s\n", C.GoString(directory))
	err := git.RebuildHistory(C.GoString(directory))
	if err != nil {
		fmt.Fprintf(os.Stderr, "[go_git_dart] GitRebuildHistory error: %v\n", err)
		return C.CString(err.Error())
	}

	fmt.Fprintf(os.Stderr, "[go_git_dart] GitRebuildHistory success\n")
	return nil
}

//export GitCheckRepoHealth
func GitCheckRepoHealth(directory *C.char, issues **C.char) C.int {
	fmt.Fprintf(os.Stderr, "[go_git_dart] GitCheckRepoHealth called: dir=%s\n", C.GoString(directory))
	healthy, issuesStr := git.CheckRepoHealth(C.GoString(directory))
	if !healthy {
		*issues = C.CString(issuesStr)
		fmt.Fprintf(os.Stderr, "[go_git_dart] GitCheckRepoHealth: unhealthy - %s\n", issuesStr)
		return 0
	}
	fmt.Fprintf(os.Stderr, "[go_git_dart] GitCheckRepoHealth: healthy\n")
	return 1
}

//export GitResetSoftToRemote
func GitResetSoftToRemote(directory *C.char, remote *C.char) *C.char {
	fmt.Fprintf(os.Stderr, "[go_git_dart] GitResetSoftToRemote called: dir=%s remote=%s\n", C.GoString(directory), C.GoString(remote))
	err := git.ResetSoftToRemote(C.GoString(directory), C.GoString(remote))
	if err != nil {
		fmt.Fprintf(os.Stderr, "[go_git_dart] GitResetSoftToRemote error: %v\n", err)
		return C.CString(err.Error())
	}

	fmt.Fprintf(os.Stderr, "[go_git_dart] GitResetSoftToRemote success\n")
	return nil
}

//export GJGenerateRSAKeys
func GJGenerateRSAKeys(publicKey **C.char, privateKey **C.char) *C.char {
	fmt.Fprintf(os.Stderr, "[go_git_dart] GJGenerateRSAKeys called\n")
	publicKeyVal, privateKeyVal, err := keygen.GenerateRSAKeys()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[go_git_dart] GJGenerateRSAKeys error: %v\n", err)
		return C.CString(err.Error())
	}

	fmt.Fprintf(os.Stderr, "[go_git_dart] GJGenerateRSAKeys success\n")
	*publicKey = C.CString(publicKeyVal)
	*privateKey = C.CString(privateKeyVal)

	return nil
}

func main() {}
