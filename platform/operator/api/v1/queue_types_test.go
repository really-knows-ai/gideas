package v1

import (
	"encoding/json"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// TestQueueJSONShape pins the Queue CR wire shape: spec.queueName plus
// status.shards[].{shardID,addr,lastHeartbeatAt,phase}, incl. metav1.Time
// serialization.
func TestQueueJSONShape(t *testing.T) {
	ts := metav1.NewTime(time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC))
	q := &Queue{
		TypeMeta: metav1.TypeMeta{APIVersion: "flow.foundry.io/v1", Kind: "Queue"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "hitl-approval",
			Namespace: "foundry-system",
		},
		Spec: QueueSpec{QueueName: "hitl-approval"},
		Status: QueueStatus{Shards: []QueueShardStatus{
			{ShardID: "hitl-approval-7c9f", Addr: "10.1.2.3:50053", LastHeartbeatAt: ts, Phase: "active"},
		}},
	}

	data, err := json.Marshal(q)
	if err != nil {
		t.Fatalf("marshal Queue: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal Queue: %v", err)
	}

	spec, ok := decoded["spec"].(map[string]any)
	if !ok {
		t.Fatal("spec missing from JSON")
	}
	if spec["queueName"] != "hitl-approval" {
		t.Fatalf("expected spec.queueName hitl-approval, got %#v", spec["queueName"])
	}

	status, ok := decoded["status"].(map[string]any)
	if !ok {
		t.Fatal("status missing from JSON")
	}
	shards, ok := status["shards"].([]any)
	if !ok || len(shards) != 1 {
		t.Fatalf("expected 1 shard in status, got %#v", status["shards"])
	}
	shard, ok := shards[0].(map[string]any)
	if !ok {
		t.Fatal("first shard not a map")
	}
	if shard["shardID"] != "hitl-approval-7c9f" || shard["addr"] != "10.1.2.3:50053" || shard["phase"] != "active" {
		t.Fatalf("unexpected shard: %#v", shard)
	}
	if shard["lastHeartbeatAt"] != "2026-08-19T00:00:00Z" {
		t.Fatalf("expected RFC3339 lastHeartbeatAt, got %#v", shard["lastHeartbeatAt"])
	}
}

// TestQueueSchemeRoundTrip pins the Queue CRD registration into the scheme:
// ObjectKinds resolves to flow.foundry.io/v1 Queue, and a full object
// round-trips through the unstructured converter preserving status.shards.
func TestQueueSchemeRoundTrip(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}

	kinds, _, err := scheme.ObjectKinds(&Queue{})
	if err != nil {
		t.Fatalf("ObjectKinds: %v", err)
	}
	if len(kinds) != 1 || kinds[0].Group != "flow.foundry.io" || kinds[0].Version != "v1" || kinds[0].Kind != "Queue" {
		t.Fatalf("unexpected GVK: %+v", kinds)
	}

	q := &Queue{
		ObjectMeta: metav1.ObjectMeta{Name: "hitl-approval"},
		Spec:       QueueSpec{QueueName: "hitl-approval"},
		Status: QueueStatus{Shards: []QueueShardStatus{
			{ShardID: "s-0", Addr: "10.0.0.1:50053", Phase: "active"},
		}},
	}

	conv := runtime.DefaultUnstructuredConverter
	u, err := conv.ToUnstructured(q)
	if err != nil {
		t.Fatalf("ToUnstructured: %v", err)
	}
	var back Queue
	if err := conv.FromUnstructured(u, &back); err != nil {
		t.Fatalf("FromUnstructured: %v", err)
	}
	if len(back.Status.Shards) != 1 || back.Status.Shards[0].ShardID != "s-0" || back.Status.Shards[0].Phase != "active" {
		t.Fatalf("status.shards lost in unstructured round-trip: %+v", back.Status.Shards)
	}
}

// TestQueueDeepcopyIndependence pins that DeepCopy produces an independent,
// non-aliased Shards slice.
func TestQueueDeepcopyIndependence(t *testing.T) {
	q := &Queue{
		Status: QueueStatus{Shards: []QueueShardStatus{
			{ShardID: "s-0", Addr: "10.0.0.1:50053", Phase: "active"},
		}},
	}
	copied := q.DeepCopy()
	if &copied.Status.Shards[0] == &q.Status.Shards[0] {
		t.Fatal("DeepCopy shares the queue item backing array element")
	}
	copied.Status.Shards[0].Addr = "mutated"
	if q.Status.Shards[0].Addr != "10.0.0.1:50053" {
		t.Fatal("mutating the copy affected the original")
	}

	statusCopy := q.Status.DeepCopy()
	statusCopy.Shards[0].ShardID = "mutated"
	if q.Status.Shards[0].ShardID != "s-0" {
		t.Fatal("QueueStatus.DeepCopy aliased the Shards slice")
	}
}

// TestQueueDeepCopyObjectRoot pins that Queue/QueueList satisfy the
// runtime.Object contract used by client-go / controller-runtime.
func TestQueueDeepCopyObjectRoot(t *testing.T) {
	var _ runtime.Object = &Queue{}
	var _ runtime.Object = &QueueList{}

	obj := (&Queue{}).DeepCopyObject()
	if _, ok := obj.(*Queue); !ok {
		t.Fatalf("DeepCopyObject returned wrong type: %T", obj)
	}
	list := &QueueList{Items: []Queue{{Spec: QueueSpec{QueueName: "hitl-approval"}}}}
	if listObj := list.DeepCopyObject(); len(listObj.(*QueueList).Items) != 1 {
		t.Fatalf("QueueList DeepCopyObject lost items: %+v", listObj)
	}
}
