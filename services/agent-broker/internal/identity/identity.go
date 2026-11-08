// Package identity turns a presented client certificate into a cluster identity
// (spec 30, FR-4, FR-5).
//
// The sandbox holds a short-lived bearer certificate whose only SAN is
// spiffe://cowlab.org/ns/agent-sandbox/pod/<pod-name>. Resolving it means:
//
//  1. parse the URI SAN and reject anything that is not exactly that shape in the
//     namespace this validator serves;
//  2. get the Pod and require an owner reference of kind Sandbox in
//     agents.x-k8s.io/v1beta1;
//  3. get that Sandbox and require its UID to equal the owner reference's UID, so
//     a recreated Pod cannot inherit a stale identity.
//
// A missing Pod, an unowned Pod, a missing Sandbox and a UID mismatch are all
// rejections (401 at the HTTP layer), never partial identities.
package identity

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

const (
	// SandboxAPIVersion is the vendored agent-sandbox API version.
	SandboxAPIVersion = "agents.x-k8s.io/v1beta1"
	// SandboxKind is the owner-reference kind that grants identity.
	SandboxKind = "Sandbox"
	// SANHost is the trust domain host component of the URI SAN.
	SANHost = "cowlab.org"
)

var (
	ErrMalformedSAN       = errors.New("identity: malformed or foreign SAN")
	ErrPodNotFound        = errors.New("identity: pod not found")
	ErrOrphanPod          = errors.New("identity: pod is not owned by a Sandbox")
	ErrSandboxNotFound    = errors.New("identity: sandbox not found")
	ErrSandboxUIDMismatch = errors.New("identity: sandbox UID mismatch")
	ErrNotImplemented     = errors.New("identity: not implemented")
)

// sandboxGVR is the dynamic resource the agent-sandbox controller serves. The
// plural is "sandboxes", not the "sandboxs" the REST mapper would guess.
var sandboxGVR = schema.GroupVersionResource{
	Group:    "agents.x-k8s.io",
	Version:  "v1beta1",
	Resource: "sandboxes",
}

// Identity is the resolved subject of a client certificate.
type Identity struct {
	Namespace   string
	PodName     string
	SandboxName string
	SandboxUID  types.UID
}

// Resolver resolves URI SANs against one namespace.
type Resolver struct {
	kube      kubernetes.Interface
	dyn       dynamic.Interface
	namespace string
}

// NewResolver builds a Resolver. Both clients are expected to be namespace-scoped
// by RBAC, not by this constructor (FR-60).
func NewResolver(kube kubernetes.Interface, dyn dynamic.Interface, namespace string) *Resolver {
	return &Resolver{kube: kube, dyn: dyn, namespace: namespace}
}

// Resolve maps uriSAN to an Identity or returns one of the sentinel errors.
func (r *Resolver) Resolve(ctx context.Context, uriSAN string) (Identity, error) {
	podName, err := r.parseSAN(uriSAN)
	if err != nil {
		return Identity{}, err
	}

	pod, err := r.kube.CoreV1().Pods(r.namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return Identity{}, fmt.Errorf("%w: pod %q in namespace %q", ErrPodNotFound, podName, r.namespace)
		}
		return Identity{}, fmt.Errorf("identity: get pod %q in namespace %q: %w", podName, r.namespace, err)
	}

	owner, err := sandboxOwner(pod.OwnerReferences)
	if err != nil {
		return Identity{}, fmt.Errorf("%w: pod %q in namespace %q", err, podName, r.namespace)
	}

	sandbox, err := r.dyn.Resource(sandboxGVR).Namespace(r.namespace).Get(ctx, owner.Name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return Identity{}, fmt.Errorf("%w: sandbox %q in namespace %q", ErrSandboxNotFound, owner.Name, r.namespace)
		}
		return Identity{}, fmt.Errorf("identity: get sandbox %q in namespace %q: %w", owner.Name, r.namespace, err)
	}

	// A recreated Sandbox gets a new UID; an owner reference carrying the old UID
	// must not grant the new workload's identity (FR-5).
	if sandbox.GetUID() != owner.UID {
		return Identity{}, fmt.Errorf("%w: pod %q owner %q has UID %q, live Sandbox has UID %q", ErrSandboxUIDMismatch, podName, owner.Name, owner.UID, sandbox.GetUID())
	}

	return Identity{
		Namespace:   r.namespace,
		PodName:     podName,
		SandboxName: owner.Name,
		SandboxUID:  owner.UID,
	}, nil
}

// parseSAN enforces the exact SAN shape from FR-4. It is intentionally strict:
// any scheme/host mismatch, extra segment, encoded path or foreign namespace is a
// rejection rather than something to normalise.
func (r *Resolver) parseSAN(uriSAN string) (string, error) {
	u, err := url.Parse(uriSAN)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrMalformedSAN, err)
	}
	if u.Scheme != "spiffe" || u.Host != SANHost {
		return "", fmt.Errorf("%w: scheme %q host %q", ErrMalformedSAN, u.Scheme, u.Host)
	}
	// Credentials, queries, fragments and opaque forms are never part of a
	// SPIFFE ID this validator accepts.
	if u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("%w: unexpected URI component", ErrMalformedSAN)
	}
	// Any percent-encoding is non-canonical here and is how encoded traversal
	// (for example %2e%2e or %2f) would try to slip past the segment checks.
	if u.RawPath != "" {
		return "", fmt.Errorf("%w: encoded path", ErrMalformedSAN)
	}

	segments := strings.Split(u.Path, "/")
	// Exactly: "", "ns", <namespace>, "pod", <pod-name>.
	if len(segments) != 5 || segments[1] != "ns" || segments[3] != "pod" {
		return "", fmt.Errorf("%w: path %q", ErrMalformedSAN, u.Path)
	}
	namespace, podName := segments[2], segments[4]
	if namespace == "" || podName == "" || namespace == "." || namespace == ".." || podName == "." || podName == ".." {
		return "", fmt.Errorf("%w: path %q", ErrMalformedSAN, u.Path)
	}
	if namespace != r.namespace {
		return "", fmt.Errorf("%w: namespace %q is not served here", ErrMalformedSAN, namespace)
	}
	return podName, nil
}

// sandboxOwner finds the Sandbox owner reference. Multiple references are
// allowed, but only one with the exact API version and kind grants identity.
func sandboxOwner(refs []metav1.OwnerReference) (metav1.OwnerReference, error) {
	for _, ref := range refs {
		if ref.APIVersion == SandboxAPIVersion && ref.Kind == SandboxKind && ref.Name != "" {
			return ref, nil
		}
	}
	return metav1.OwnerReference{}, ErrOrphanPod
}
