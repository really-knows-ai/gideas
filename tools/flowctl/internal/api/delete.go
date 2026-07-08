package api

import (
	"context"
	"fmt"
	"time"
)

// DeletePollInterval and DeletePollMaxAttempts control child polling behavior.
// These are exported for testability — tests can set them to smaller values
// to avoid waiting the full duration.
// ponytail: Package-level vars for test seam; if more fine-grained control
// is needed, pass a config struct through DeleteWorkitemCascade.
var DeletePollInterval = 2 * time.Second
var DeletePollMaxAttempts = 15

// CascadeResult holds the outcome of a cascading delete operation.
type CascadeResult struct {
	Success       bool
	Deleted       []string           // IDs of successfully deleted items (all levels)
	Failed        []ChildDeleteError // IDs and errors of failed child deletes
	ParentDeleted bool
	Error         string // overall blocker error if any
}

// ChildDeleteError records a child workitem that could not be deleted.
type ChildDeleteError struct {
	WorkitemID string
	Error      string
}

// K8sDeleter is the subset of K8sClient methods needed by DeleteWorkitemCascade.
// K8sClient satisfies this interface.
type K8sDeleter interface {
	ListChildren(ctx context.Context, namespace string, parentID string) ([]WorkitemSummary, error)
	DeleteWorkitem(ctx context.Context, namespace string, name string) error
	GetWorkitem(ctx context.Context, namespace string, name string) (*WorkitemDetail, error)
}

// Compile-time check that *K8sClient satisfies K8sDeleter.
var _ K8sDeleter = (*K8sClient)(nil)

// DeleteWorkitemCascade performs a bottom-up depth-first cascading delete.
// Algorithm:
//  1. Recursively delete children via DeleteWorkitemCascade (handles subtree including the child itself).
//  2. Before recursing on any child, verify its phase is Completed or Failed.
//  3. The recursive call handles child deletion and polling. The parent loop only
//     collects results and checks for failures.
//  4. If all children succeeded, delete the parent.
func DeleteWorkitemCascade(ctx context.Context, k8s K8sDeleter, namespace string, workitemID string) *CascadeResult {
	return deleteWorkitemCascade(ctx, k8s, namespace, workitemID, 0)
}

// deleteWorkitemCascade is the recursive helper with depth tracking.
func deleteWorkitemCascade(ctx context.Context, k8s K8sDeleter, namespace string, workitemID string, depth int) *CascadeResult {
	if depth > 100 {
		return &CascadeResult{
			Success: false,
			Error:   fmt.Sprintf("maximum recursion depth exceeded for %s", workitemID),
		}
	}

	result := &CascadeResult{
		Deleted: make([]string, 0),
		Failed:  make([]ChildDeleteError, 0),
	}

	// 1. List children
	children, err := k8s.ListChildren(ctx, namespace, workitemID)
	if err != nil {
		result.Error = fmt.Sprintf("list children for %s: %v", workitemID, err)
		return result
	}

	// 2. Recursively delete each child (depth-first, bottom-up).
	// If a child delete fails, the error is recorded and remaining children
	// are still processed.  The parent is preserved if any child failed.
	for _, child := range children {
		// 3. Verify child is terminal
		if child.State != "Completed" && child.State != "Failed" {
			result.Failed = append(result.Failed, ChildDeleteError{
				WorkitemID: child.Name,
				Error:      fmt.Sprintf("child %s is in %s state; cannot delete", child.Name, child.State),
			})
			result.Success = false
			continue
		}

		// Recursively delete child and all its descendants
		childResult := deleteWorkitemCascade(ctx, k8s, namespace, child.Name, depth+1)
		result.Deleted = append(result.Deleted, childResult.Deleted...)
		result.Failed = append(result.Failed, childResult.Failed...)

		if !childResult.Success {
			// Child subtree deletion failed — record and continue
			result.Success = false
		}
	}

	// 4. If any child failed, preserve parent
	if len(result.Failed) > 0 {
		result.Success = false
		result.Error = fmt.Sprintf("%d child deletion(s) failed", len(result.Failed))
		return result
	}

	// 5. Delete this workitem itself (with polling)
	err = k8s.DeleteWorkitem(ctx, namespace, workitemID)
	if err != nil {
		result.Error = fmt.Sprintf("failed to delete %s: %v", workitemID, err)
		result.Failed = append(result.Failed, ChildDeleteError{
			WorkitemID: workitemID,
			Error:      err.Error(),
		})
		result.Success = false
		return result
	}
	result.Deleted = append(result.Deleted, workitemID)

	// 6. Poll for removal (every DeletePollInterval, max DeletePollMaxAttempts)
	for i := 0; i < DeletePollMaxAttempts; i++ {
		select {
		case <-ctx.Done():
			result.Error = fmt.Sprintf("context cancelled while polling for %s", workitemID)
			result.Success = false
			return result
		case <-time.After(DeletePollInterval):
		}
		_, getErr := k8s.GetWorkitem(ctx, namespace, workitemID)
		if getErr != nil {
			// Not found (resource deleted)
			result.Success = true
			result.ParentDeleted = (depth == 0) // only top-level call marks parent
			return result
		}
	}

	// Timeout — workitem still present
	result.Error = fmt.Sprintf("%s still present after 30s timeout", workitemID)
	result.Success = false
	return result
}
