package gitstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/foundry/flow/cartographer/internal/uuidutil"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
)

// CreateBranch creates a branch from main with the given txID.
// Validates txID as a canonical RFC4122 §3 UUID v4 (SPEC:978 error-table row
// "Invalid transaction ID format" — non-canonical spellings are rejected).
// Returns ErrBranchAlreadyExists if the branch already exists, ErrInvalidUUID
// if txID is not a valid canonical UUID v4.
func (g *branchOps) CreateBranch(ctx context.Context, txID string) error {
	// The same canonical-form gate the entity/edge write paths use
	// (uuidutil.Validate): the txID is persisted as the branch name, so a
	// non-canonical spelling of a valid UUID (uppercase hex, no-hyphen,
	// braced, urn:uuid:) must be rejected rather than accepted by uuid.Parse.
	if err := uuidutil.Validate(txID); err != nil {
		return ErrInvalidUUID
	}

	// A branch can exist as a ref without a config entry (SetBranchRef writes
	// only the ref — the state DeleteBranch explicitly tolerates), so the config
	// check below alone would miss a ref-only branch and silently overwrite its
	// ref to point at main, discarding the branch's commits. Check both
	// existence markers: the ref here, the config entry via git.ErrBranchExists.
	exists, err := g.BranchExists(ctx, txID)
	if err != nil {
		return err
	}
	if exists {
		return ErrBranchAlreadyExists
	}

	err = g.repo.CreateBranch(&config.Branch{Name: txID})
	if err != nil {
		if err == git.ErrBranchExists {
			return ErrBranchAlreadyExists
		}
		return fmt.Errorf("create branch %s: %w", txID, err)
	}

	// CreateBranch only creates the config entry; the ref must point at main
	// explicitly. SPEC Hydration step 1 (SPEC:803) mandates that the
	// transaction branch is created from main. Branching from HEAD would let an
	// abandoned failed Commit (whose working tree is still checked out on the
	// transaction branch) leak its committed changes into the next transaction
	// via HardResetToBranch, breaking transaction isolation.
	mainRef, err := g.repo.Reference(plumbing.ReferenceName("refs/heads/main"), true)
	if err != nil {
		return fmt.Errorf("resolve main ref: %w", err)
	}
	branchRef := plumbing.NewHashReference(
		plumbing.ReferenceName("refs/heads/"+txID),
		mainRef.Hash(),
	)
	if err := g.backend.SetReference(branchRef); err != nil {
		return fmt.Errorf("set branch ref %s: %w", txID, err)
	}

	return nil
}

// Checkout checks out the named branch. If the branch does not exist, it
// creates it from HEAD and checks it out. Uses Force: true to handle dirty
// working trees.
// ponytail: The create-on-missing fallback (lines 81-82) is test-only infrastructure.
// The SPEC flow (R9 hydration step 1) creates the branch up front: CreateBranch
// writes refs/heads/<txID> via backend.SetReference before the working tree is
// ever checked out, so this path is never reached in production. RestoreMain
// always checks out "main" which exists. Only tests exercise the create-on-missing
// path.
func (g *branchOps) Checkout(ctx context.Context, branch string) error {
	ref := plumbing.ReferenceName("refs/heads/" + branch)
	err := g.wt.Checkout(&git.CheckoutOptions{Branch: ref, Force: true})
	if err != nil && err == plumbing.ErrReferenceNotFound {
		err = g.wt.Checkout(&git.CheckoutOptions{Branch: ref, Create: true, Force: true})
	}
	if err != nil {
		return fmt.Errorf("checkout %s: %w", branch, err)
	}
	return nil
}

// HardResetToBranch checks out the target branch, performs a hard reset,
// and cleans untracked files. The checkout-before-reset ordering is
// critical for correctness when SetBranchRef has moved the branch ref.
func (g *branchOps) HardResetToBranch(ctx context.Context, branch string) error {
	ref := plumbing.ReferenceName("refs/heads/" + branch)
	if err := g.wt.Checkout(&git.CheckoutOptions{Branch: ref, Force: true}); err != nil {
		return fmt.Errorf("hard reset checkout %s: %w", branch, err)
	}
	if err := g.wt.Reset(&git.ResetOptions{Mode: git.HardReset}); err != nil {
		return fmt.Errorf("hard reset %s: %w", branch, err)
	}
	if err := g.wt.Clean(&git.CleanOptions{Dir: true}); err != nil {
		return fmt.Errorf("clean %s: %w", branch, err)
	}
	return nil
}

// RestoreMain is a convenience method that checks out the main branch.
func (g *branchOps) RestoreMain(ctx context.Context) error {
	return g.Checkout(ctx, mainBranchName)
}

// CheckoutCommit checks out the given commit hash (detached HEAD) and updates
// the working tree to that commit's tree. It is used by startup recovery
// (buildMainFileLookups) to read main's file set as of a transaction's begin
// head (MainHeadAtLastSync), so the reconstructed change-log diff is computed
// against the transaction's true baseline rather than current main. The
// caller must restore main (RestoreMain) when done with the read; recovery
// does so under the git lock before returning. Validates that hash is a
// 40-character lowercase hex string.
func (g *branchOps) CheckoutCommit(ctx context.Context, hash string) error {
	if len(hash) != 40 {
		return fmt.Errorf("%w: %q", ErrInvalidHash, hash)
	}
	for _, c := range hash {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return fmt.Errorf("%w: %q", ErrInvalidHash, hash)
		}
	}
	if err := g.wt.Checkout(&git.CheckoutOptions{Hash: plumbing.NewHash(hash), Force: true}); err != nil {
		return fmt.Errorf("checkout commit %s: %w", hash, err)
	}
	return nil
}

// BranchExists returns true if a branch with the given txID exists.
func (g *branchOps) BranchExists(ctx context.Context, txID string) (bool, error) {
	_, err := g.repo.Reference(plumbing.ReferenceName("refs/heads/"+txID), true)
	if err != nil {
		if err == plumbing.ErrReferenceNotFound {
			return false, nil
		}
		return false, fmt.Errorf("check branch %s: %w", txID, err)
	}
	return true, nil
}

// DeleteBranch deletes the branch with the given txID from both config
// and the reference store.
//
// PRECONDITION: the named branch must not be the currently checked-out
// branch. Deleting the branch that HEAD points at removes its ref while HEAD
// still symbolically references it, leaving a dangling HEAD. The production
// caller (transaction teardown) restores main first — RestoreMain — before
// deleting the transaction branch (see cartographer_server.go
// finishTransactionCleanup).
func (g *branchOps) DeleteBranch(ctx context.Context, txID string) error {
	// Remove the branch reference from storage
	refName := plumbing.ReferenceName("refs/heads/" + txID)
	if err := g.backend.RemoveReference(refName); err != nil {
		return fmt.Errorf("remove branch ref %s: %w", txID, err)
	}
	// Also remove the config entry. go-git returns ErrBranchNotFound when the
	// entry is already absent (e.g. branches created via SetBranchRef never
	// have a config entry) — that is a harmless no-op. Any other failure means
	// the config entry was not removed, leaving a stale branch behind while
	// the ref is gone, so it must be propagated.
	if err := g.repo.DeleteBranch(txID); err != nil && !errors.Is(err, git.ErrBranchNotFound) {
		return fmt.Errorf("delete branch config %s: %w", txID, err)
	}
	return nil
}

// CleanUntracked removes untracked files and directories from the working
// tree.
//
// ponytail: the ctx parameter is not passed to any underlying go-billy or
// go-git call (neither exposes ctx-aware IO), so it cannot cancel or
// time-box a hung filesystem. Its presence exists only to keep the GitStore
// interface uniform across all I/O methods. Consequence: a blocked wt.Clean
// on a stalled CSI/NFS mount blocks this call regardless of caller
// cancellation. Upgrade path: run the store's I/O on a ctx-cancellable
// goroutine or wrap the filesystem in a deadline adapter, as documented on
// GitStore.
func (g *branchOps) CleanUntracked(ctx context.Context) error {
	if err := g.wt.Clean(&git.CleanOptions{Dir: true}); err != nil {
		return fmt.Errorf("clean untracked: %w", err)
	}
	return nil
}

// ListBranches returns all branch names (with "refs/heads/" prefix
// stripped) except "main", which is always excluded.
func (g *branchOps) ListBranches(ctx context.Context) ([]string, error) {
	refIter, err := g.repo.Branches()
	if err != nil {
		return nil, fmt.Errorf("list branches: %w", err)
	}
	// Every iterator this package acquires must be closed (sibling pattern:
	// defer log.Close() in remote.go IsEmpty and commit.go); for the
	// filesystem-backed storer the reference iterator holds a packed-refs file
	// handle until Close.
	defer refIter.Close()

	var branches []string
	if err := refIter.ForEach(func(ref *plumbing.Reference) error {
		name := ref.Name().Short()
		if name != mainBranchName {
			branches = append(branches, name)
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("iterate branches: %w", err)
	}

	return branches, nil
}

// BranchHEAD returns the HEAD commit hash of the named branch as a string.
// Returns ErrBranchNotFound if the branch does not exist.
func (g *branchOps) BranchHEAD(ctx context.Context, branch string) (string, error) {
	ref, err := g.repo.Reference(plumbing.ReferenceName("refs/heads/"+branch), true)
	if err != nil {
		if err == plumbing.ErrReferenceNotFound {
			return "", ErrBranchNotFound
		}
		return "", fmt.Errorf("resolve branch %s: %w", branch, err)
	}
	return ref.Hash().String(), nil
}

// SetBranchRef updates the named branch ref to point at the given commit
// hash. Validates that the hash is a 40-character lowercase hex string.
func (g *branchOps) SetBranchRef(ctx context.Context, branch string, hash string) error {
	if len(hash) != 40 {
		return fmt.Errorf("%w: %q", ErrInvalidHash, hash)
	}
	for _, c := range hash {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return fmt.Errorf("%w: %q", ErrInvalidHash, hash)
		}
	}

	ref := plumbing.NewHashReference(
		plumbing.ReferenceName("refs/heads/"+branch),
		plumbing.NewHash(hash),
	)
	if err := g.backend.SetReference(ref); err != nil {
		return fmt.Errorf("set branch ref %s: %w", branch, err)
	}
	return nil
}
