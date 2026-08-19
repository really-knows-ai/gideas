/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// QueueSpec defines the desired state of Queue.
type QueueSpec struct {
	// queueName is the name of the queue this registry entry tracks.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	QueueName string `json:"queueName"`
}

// QueueShardStatus defines the observed state of a single shard of the queue.
type QueueShardStatus struct {
	// shardID is the owning pod identity of the shard.
	ShardID string `json:"shardID"`
	// addr is the host:port address the shard serves its QueuePeerService on.
	Addr string `json:"addr"`
	// lastHeartbeatAt is the RFC3339 timestamp of the shard's most recent heartbeat.
	// +optional
	LastHeartbeatAt metav1.Time `json:"lastHeartbeatAt,omitempty"`
	// phase is the shard's lease phase: "active" or "evicted".
	// +optional
	// +kubebuilder:validation:Enum=active;evicted
	Phase string `json:"phase,omitempty"`
}

// QueueStatus defines the observed state of Queue.
type QueueStatus struct {
	// shards are the living shards of this queue, one entry per registered shard.
	// +optional
	Shards []QueueShardStatus `json:"shards,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced

// Queue is the Schema for the queues API. One Queue CR exists per registered
// queue; the queue-service maintains its shards + status as the durable,
// etcd-backed registry (R-B7).
type Queue struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of Queue
	// +required
	Spec QueueSpec `json:"spec"`

	// status defines the observed state of Queue
	// +optional
	Status QueueStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// QueueList contains a list of Queue
type QueueList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []Queue `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(scheme *runtime.Scheme) error {
		scheme.AddKnownTypes(GroupVersion, &Queue{}, &QueueList{})
		return nil
	})
}
