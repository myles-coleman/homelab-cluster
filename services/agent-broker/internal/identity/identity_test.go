package identity

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
)

const (
	testNamespace = "agent-sandbox"
	testPodName   = "sandbox-pod"
	testSandbox   = "sandbox"
	testUID       = types.UID("11111111-1111-1111-1111-111111111111")
)

// TestResolveReturnsSandboxIdentity is the happy path FR-4 promises: a SAN that
// names a live Pod owned by a live Sandbox becomes an Identity carrying that
// Sandbox's UID, which downstream policy keys on.
func TestResolveReturnsSandboxIdentity(t *testing.T) {
	pod := podWithOwner(testPodName, sandboxOwnerRef(testSandbox, testUID))
	resolver := NewResolver(
		kubernetesfake.NewSimpleClientset(pod),
		newDynamicClient(t, sandboxObject(testSandbox, testUID)),
		testNamespace,
	)

	id, err := resolver.Resolve(context.Background(), podSAN(testPodName))
	if err != nil {
		t.Fatalf("Resolve(%q): %v", podSAN(testPodName), err)
	}
	if id.Namespace != testNamespace {
		t.Errorf("Namespace = %q, want %q", id.Namespace, testNamespace)
	}
	if id.PodName != testPodName {
		t.Errorf("PodName = %q, want %q", id.PodName, testPodName)
	}
	if id.SandboxName != testSandbox {
		t.Errorf("SandboxName = %q, want %q", id.SandboxName, testSandbox)
	}
	if id.SandboxUID != testUID {
		t.Errorf("SandboxUID = %q, want %q", id.SandboxUID, testUID)
	}
}

// TestResolveRejectsUntrustedSubjects walks every rejection branch in FR-4/FR-5.
// Each one must be a sentinel, never a partially populated Identity: a caller
// that mishandles one branch would otherwise admit an unowned workload.
func TestResolveRejectsUntrustedSubjects(t *testing.T) {
	otherUID := types.UID("22222222-2222-2222-2222-222222222222")

	tests := []struct {
		name      string
		san       string
		pods      []runtime.Object
		sandboxes []runtime.Object
		wantErr   error
	}{
		{
			name:    "pod does not exist",
			san:     podSAN("ghost-pod"),
			wantErr: ErrPodNotFound,
		},
		{
			name:    "pod has no owner references",
			san:     podSAN(testPodName),
			pods:    []runtime.Object{podWithOwner(testPodName)},
			wantErr: ErrOrphanPod,
		},
		{
			name:    "pod is owned by a non-Sandbox resource",
			san:     podSAN(testPodName),
			pods:    []runtime.Object{podWithOwner(testPodName, metav1.OwnerReference{APIVersion: "apps/v1", Kind: "Deployment", Name: "workload", UID: testUID})},
			wantErr: ErrOrphanPod,
		},
		{
			name:      "owner reference UID is stale",
			san:       podSAN(testPodName),
			pods:      []runtime.Object{podWithOwner(testPodName, sandboxOwnerRef(testSandbox, testUID))},
			sandboxes: []runtime.Object{sandboxObject(testSandbox, otherUID)},
			wantErr:   ErrSandboxUIDMismatch,
		},
		{
			name:    "sandbox has been deleted",
			san:     podSAN(testPodName),
			pods:    []runtime.Object{podWithOwner(testPodName, sandboxOwnerRef(testSandbox, testUID))},
			wantErr: ErrSandboxNotFound,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resolver := NewResolver(
				kubernetesfake.NewSimpleClientset(tc.pods...),
				newDynamicClient(t, tc.sandboxes...),
				testNamespace,
			)

			id, err := resolver.Resolve(context.Background(), tc.san)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Resolve(%q) error = %v, want %v", tc.san, err, tc.wantErr)
			}
			if id != (Identity{}) {
				t.Errorf("Resolve(%q) returned a partial identity %+v alongside an error", tc.san, id)
			}
		})
	}
}

// TestResolveRejectsMalformedSAN makes sure the URI parser is strict. A lax
// parser would let a different trust domain or namespace address this validator.
func TestResolveRejectsMalformedSAN(t *testing.T) {
	tests := []struct {
		name string
		san  string
	}{
		{name: "empty string", san: ""},
		{name: "wrong trust domain host", san: "spiffe://evil.example/ns/agent-sandbox/pod/" + testPodName},
		{name: "different namespace", san: "spiffe://cowlab.org/ns/other-namespace/pod/" + testPodName},
		{name: "missing pod segment", san: "spiffe://cowlab.org/ns/agent-sandbox/pod"},
		{name: "empty pod segment", san: "spiffe://cowlab.org/ns/agent-sandbox/pod/"},
		{name: "wrong scheme", san: "https://cowlab.org/ns/agent-sandbox/pod/" + testPodName},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resolver := NewResolver(
				kubernetesfake.NewSimpleClientset(),
				newDynamicClient(t),
				testNamespace,
			)

			id, err := resolver.Resolve(context.Background(), tc.san)
			if !errors.Is(err, ErrMalformedSAN) {
				t.Fatalf("Resolve(%q) error = %v, want %v", tc.san, err, ErrMalformedSAN)
			}
			if id != (Identity{}) {
				t.Errorf("Resolve(%q) returned a partial identity %+v alongside an error", tc.san, id)
			}
		})
	}
}

func podSAN(podName string) string {
	return "spiffe://" + SANHost + "/ns/" + testNamespace + "/pod/" + podName
}

func podWithOwner(name string, owners ...metav1.OwnerReference) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			Namespace:       testNamespace,
			OwnerReferences: owners,
		},
	}
}

func sandboxOwnerRef(name string, uid types.UID) metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion: SandboxAPIVersion,
		Kind:       SandboxKind,
		Name:       name,
		UID:        uid,
	}
}

// sandboxObject mirrors the unstructured shape the vendored agent-sandbox API
// serves; the resolver reads it through the dynamic client.
func sandboxObject(name string, uid types.UID) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{Object: map[string]interface{}{}}
	obj.SetAPIVersion(SandboxAPIVersion)
	obj.SetKind(SandboxKind)
	obj.SetNamespace(testNamespace)
	obj.SetName(name)
	obj.SetUID(uid)
	return obj
}

// newDynamicClient builds a fake dynamic client with the Sandboxes created
// through the API path rather than the constructor: the tracker guesses
// "sandboxs" from the kind, while the real resource is "sandboxes".
func newDynamicClient(t *testing.T, objects ...runtime.Object) dynamic.Interface {
	t.Helper()

	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	gvr := schema.GroupVersionResource{Group: "agents.x-k8s.io", Version: "v1beta1", Resource: "sandboxes"}
	for _, obj := range objects {
		sandbox, ok := obj.(*unstructured.Unstructured)
		if !ok {
			t.Fatalf("newDynamicClient only accepts unstructured Sandboxes, got %T", obj)
		}
		if _, err := client.Resource(gvr).Namespace(sandbox.GetNamespace()).Create(context.Background(), sandbox, metav1.CreateOptions{}); err != nil {
			t.Fatalf("seed Sandbox %s/%s: %v", sandbox.GetNamespace(), sandbox.GetName(), err)
		}
	}
	return client
}
