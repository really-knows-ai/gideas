// Package dispatcher implements the push-based work assignment logic.
//
// The Dispatcher is responsible for:
//  1. Discovering "Ready" Pods belonging to a FoundryNode's Deployment.
//  2. Selecting a target Pod (round-robin or random).
//  3. Dialing the selected Pod's Sidecar (port 50051) and calling AssignWork.
//
// The Operator invokes the Dispatcher when a Workitem transitions to
// Pending and has a currentAssignee set.
package dispatcher

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"

	flowv1gen "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// LabelNodeName is the Pod label used to identify which FoundryNode
	// a Pod belongs to. Set by the Deployment/ReplicaSet that manages the
	// Node's Pods.
	LabelNodeName = "flow.gideas.io/node-name"

	// DefaultSidecarPort is the gRPC port the Sidecar listens on inside
	// each Pod.
	DefaultSidecarPort = 50051
)

// Dispatcher assigns Workitems to specific Pods by pushing via gRPC.
type Dispatcher struct {
	// K8sClient is used to discover Pods.
	K8sClient client.Client

	// Namespace restricts Pod discovery to a single namespace.
	Namespace string

	// SidecarPort can be overridden for testing. Defaults to 50051.
	SidecarPort int

	// DialFunc can be overridden for testing to avoid real gRPC connections.
	// If nil, grpc.NewClient is used.
	DialFunc func(addr string) (flowv1gen.SidecarServiceClient, func() error, error)
}

// New creates a Dispatcher for the given namespace.
func New(k8sClient client.Client, namespace string) *Dispatcher {
	return &Dispatcher{
		K8sClient:   k8sClient,
		Namespace:   namespace,
		SidecarPort: DefaultSidecarPort,
	}
}

// AssignResult contains the outcome of a successful assignment.
type AssignResult struct {
	// PodName is the name of the Pod the work was assigned to.
	PodName string
	// PodIP is the IP address of the Pod.
	PodIP string
}

// Assign discovers a ready Pod for the given node and pushes the work
// assignment via gRPC.
//
// Steps:
//  1. List Pods with label flow.gideas.io/node-name=<nodeName>.
//  2. Filter for Running + Ready Pods.
//  3. Select one Pod (random).
//  4. Dial PodIP:50051 and call SidecarService.AssignWork.
func (d *Dispatcher) Assign(ctx context.Context, nodeName string, flowID string, workitemID string) (*AssignResult, error) {
	log := slog.With("node", nodeName, "workitem_id", workitemID)

	// 1. Discover Pods.
	pods, err := d.discoverReadyPods(ctx, nodeName)
	if err != nil {
		return nil, fmt.Errorf("discover pods for node %q: %w", nodeName, err)
	}
	if len(pods) == 0 {
		return nil, fmt.Errorf("no ready pods found for node %q", nodeName)
	}

	log.Info("Discovered ready pods", "count", len(pods))

	// 2. Select a Pod (random).
	selected := pods[rand.IntN(len(pods))]
	podIP := selected.Status.PodIP
	podName := selected.Name

	log.Info("Selected pod for assignment",
		"pod", podName,
		"ip", podIP,
	)

	// 3. Dial and assign.
	addr := fmt.Sprintf("%s:%d", podIP, d.sidecarPort())

	sidecarClient, closeConn, err := d.dial(addr)
	if err != nil {
		return nil, fmt.Errorf("dial sidecar at %s: %w", addr, err)
	}
	defer func() {
		if err := closeConn(); err != nil {
			log.Warn("Failed to close sidecar connection", "error", err)
		}
	}()

	ack, err := sidecarClient.AssignWork(ctx, &flowv1gen.AssignWorkRequest{
		Context: &flowv1gen.WorkitemContext{
			FlowId:     flowID,
			WorkitemId: workitemID,
			NodeId:     nodeName,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("assign work to pod %s (%s): %w", podName, addr, err)
	}

	if !ack.GetAccepted() {
		return nil, fmt.Errorf("pod %s rejected assignment: %s", podName, ack.GetMessage())
	}

	log.Info("Assigned work to pod",
		"pod", podName,
		"ip", podIP,
		"accepted", ack.GetAccepted(),
	)

	return &AssignResult{
		PodName: podName,
		PodIP:   podIP,
	}, nil
}

// discoverReadyPods lists Pods matching the node label and filters for
// Running + Ready status.
func (d *Dispatcher) discoverReadyPods(ctx context.Context, nodeName string) ([]corev1.Pod, error) {
	var podList corev1.PodList
	listOpts := []client.ListOption{
		client.InNamespace(d.Namespace),
		client.MatchingLabels{LabelNodeName: nodeName},
	}

	if err := d.K8sClient.List(ctx, &podList, listOpts...); err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}

	var ready []corev1.Pod
	for _, pod := range podList.Items {
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}
		if !isPodReady(pod) {
			continue
		}
		if pod.Status.PodIP == "" {
			continue
		}
		ready = append(ready, pod)
	}

	return ready, nil
}

// isPodReady checks if the Pod's Ready condition is True.
func isPodReady(pod corev1.Pod) bool {
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady {
			return cond.Status == corev1.ConditionTrue
		}
	}
	return false
}

// sidecarPort returns the configured or default sidecar port.
func (d *Dispatcher) sidecarPort() int {
	if d.SidecarPort > 0 {
		return d.SidecarPort
	}
	return DefaultSidecarPort
}

// dial creates a gRPC connection to the given address and returns a
// SidecarServiceClient and a close function.
func (d *Dispatcher) dial(addr string) (flowv1gen.SidecarServiceClient, func() error, error) {
	if d.DialFunc != nil {
		return d.DialFunc(addr)
	}

	conn, err := grpc.NewClient(
		addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, nil, err
	}

	return flowv1gen.NewSidecarServiceClient(conn), conn.Close, nil
}
