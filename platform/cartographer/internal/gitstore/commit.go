package gitstore

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

var errCommitFound = errors.New("transaction commit found")

// AddAll stages all changes under the given path (equivalent to git add).
// When path is ".", stages everything in the worktree.
func (g *gitStore) AddAll(ctx context.Context, path string) error {
	if err := g.wt.AddWithOptions(&git.AddOptions{Path: path}); err != nil {
		return fmt.Errorf("add %s: %w", path, err)
	}
	return nil
}

// GitRm removes the given path from the working tree and stages the deletion
// in the git index (analogous to git rm -r). If the path does not exist on
// the working tree filesystem, it is a no-op (returns nil).
// For directory paths, all files under it are removed recursively.
func (g *gitStore) GitRm(ctx context.Context, path string) error {
	// Check if path exists
	_, err := g.fs.Stat(path)
	if err != nil {
		if isNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat %s: %w", path, err)
	}

	// Walk the path and remove each file
	if err := g.removePathRecursive(path); err != nil {
		return fmt.Errorf("git rm %s: %w", path, err)
	}
	return nil
}

// removePathRecursive walks dir/file entries and removes them from the
// worktree via wt.Remove, which stages the deletion.
func (g *gitStore) removePathRecursive(path string) error {
	fi, err := g.fs.Stat(path)
	if err != nil {
		return err
	}

	if !fi.IsDir() {
		_, err := g.wt.Remove(path)
		return err
	}

	entries, err := g.fs.ReadDir(path)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		childPath := filepath.Join(path, entry.Name())
		if entry.IsDir() {
			if err := g.removePathRecursive(childPath); err != nil {
				return err
			}
		} else {
			if _, err := g.wt.Remove(childPath); err != nil {
				return err
			}
		}
	}

	return nil
}

// Commit creates a commit on the current branch with the given message.
// It does NOT call AddAll — the caller must stage changes first.
// If there are no changes to commit (empty diff), it returns nil.
func (g *gitStore) Commit(ctx context.Context, message string) error {
	sig := &object.Signature{
		Name:  "cartographer",
		Email: "cartographer@foundry.flow",
	}
	_, err := g.wt.Commit(message, &git.CommitOptions{
		Author:    sig,
		Committer: sig,
	})
	if err != nil && err != git.ErrEmptyCommit {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// CommitExistsOnBranch scans the commit log of the currently checked-out
// branch for a commit whose first message line has the prefix
// "transaction:<txID>". Prefix matching (per SPEC Commit retry, which
// inspects git log for the transaction:<tx-id> prefix) is consistent with
// GitLogOneline.
//
// PRECONDITION: the caller MUST have checked the relevant branch out before
// calling this method. The scan walks HEAD's log (git log without a ref), so
// it only detects commits reachable from the currently checked-out branch. If
// the transaction branch has not been checked out first, a commit made on that
// branch will not be visible and this method silently returns false (the
// production caller, the Commit retry path, checks the transaction branch out
// first — see cartographer_server.go).
func (g *gitStore) CommitExistsOnBranch(ctx context.Context, txID string) (bool, error) {
	message := "transaction:" + txID
	log, err := g.repo.Log(&git.LogOptions{})
	if err != nil {
		return false, fmt.Errorf("log: %w", err)
	}
	defer log.Close()

	if err := log.ForEach(func(commit *object.Commit) error {
		firstLine, _, _ := strings.Cut(commit.Message, "\n")
		firstLine = strings.TrimRight(firstLine, "\r")
		if strings.HasPrefix(firstLine, message) {
			return errCommitFound
		}
		return nil
	}); err != nil {
		if errors.Is(err, errCommitFound) {
			return true, nil
		}
		return false, fmt.Errorf("iterate log: %w", err)
	}

	return false, nil
}

// FastForwardMerge performs a fast-forward merge of branch into into.
// It checks that into is an ancestor of branch (divergence check) before
// advancing the ref. After the merge, the working tree is on the into
// branch. The source branch is NOT deleted by this method.
func (g *gitStore) FastForwardMerge(ctx context.Context, branch, into string) error {
	intoRef, err := g.repo.Reference(plumbing.ReferenceName("refs/heads/"+into), true)
	if err != nil {
		if err == plumbing.ErrReferenceNotFound {
			return ErrBranchNotFound
		}
		return fmt.Errorf("resolve into ref %s: %w", into, err)
	}

	branchRef, err := g.repo.Reference(plumbing.ReferenceName("refs/heads/"+branch), true)
	if err != nil {
		if err == plumbing.ErrReferenceNotFound {
			return ErrBranchNotFound
		}
		return fmt.Errorf("resolve branch ref %s: %w", branch, err)
	}

	// Divergence check: into must be an ancestor of branch
	intoCommit, err := g.repo.CommitObject(intoRef.Hash())
	if err != nil {
		return fmt.Errorf("resolve into commit: %w", err)
	}
	branchCommit, err := g.repo.CommitObject(branchRef.Hash())
	if err != nil {
		return fmt.Errorf("resolve branch commit: %w", err)
	}

	isAncestor, err := intoCommit.IsAncestor(branchCommit)
	if err != nil {
		return fmt.Errorf("ancestry check: %w", err)
	}
	if !isAncestor {
		return ErrMergeDiverged
	}

	// Checkout branch (force to handle dirty index)
	if err := g.wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.ReferenceName("refs/heads/" + branch),
		Force:  true,
	}); err != nil {
		return fmt.Errorf("checkout branch %s: %w", branch, err)
	}

	// Advance into to branch tip
	newRef := plumbing.NewHashReference(intoRef.Name(), branchRef.Hash())
	if err := g.backend.SetReference(newRef); err != nil {
		return fmt.Errorf("advance ref %s: %w", into, err)
	}

	// Back to into
	if err := g.wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.ReferenceName("refs/heads/" + into),
		Force:  true,
	}); err != nil {
		return fmt.Errorf("checkout into %s: %w", into, err)
	}

	return nil
}

// GitLogOneline iterates commits on the current branch and returns the
// first message lines (formatted as "<hash> <subject>") for commits whose
// first message line starts with the given prefix — matching git log
// --oneline semantics (per SPEC Commit retry, which inspects git log
// --oneline for the transaction:<tx-id> prefix). Results are most recent
// first.
func (g *gitStore) GitLogOneline(ctx context.Context, prefix string) ([]string, error) {
	log, err := g.repo.Log(&git.LogOptions{})
	if err != nil {
		return nil, fmt.Errorf("log: %w", err)
	}
	defer log.Close()

	var results []string
	if err := log.ForEach(func(commit *object.Commit) error {
		firstLine, _, _ := strings.Cut(commit.Message, "\n")
		firstLine = strings.TrimRight(firstLine, "\r")
		if strings.HasPrefix(firstLine, prefix) {
			results = append(results, commit.Hash.String()+" "+firstLine)
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("iterate log: %w", err)
	}

	return results, nil
}
