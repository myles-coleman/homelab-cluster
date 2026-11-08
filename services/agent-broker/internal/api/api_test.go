package api

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/bundle"
	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/identity"
)

type stubFetcher struct {
	b   bundle.Bundle
	err error
}

func (s stubFetcher) Fetch(context.Context, string) (bundle.Bundle, error) {
	return s.b, s.err
}

func sandboxedServer(t *testing.T, f bundle.Fetcher) *Server {
	t.Helper()

	const ns = "agent-sandbox"
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "opencode",
			Namespace: ns,
			UID:       "pod-uid",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: identity.SandboxAPIVersion,
				Kind:       identity.SandboxKind,
				Name:       "opencode",
				UID:        "sandbox-uid",
			}},
		},
	}
	sandbox := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": identity.SandboxAPIVersion,
		"kind":       identity.SandboxKind,
		"metadata": map[string]any{
			"name":      "opencode",
			"namespace": ns,
			"uid":       "sandbox-uid",
		},
	}}

	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	gvr := schema.GroupVersionResource{Group: "agents.x-k8s.io", Version: "v1beta1", Resource: "sandboxes"}
	if _, err := dyn.Resource(gvr).Namespace(ns).Create(context.Background(), sandbox, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed sandbox: %v", err)
	}

	return &Server{
		Cache:    bundle.NewCache(f, time.Minute),
		Resolver: identity.NewResolver(fake.NewSimpleClientset(pod), dyn, ns),
	}
}

func requestWithIdentity(t *testing.T, target, uri string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, target, nil)
	if uri != "" {
		u, err := url.Parse(uri)
		if err != nil {
			t.Fatalf("parse %q: %v", uri, err)
		}
		r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{URIs: []*url.URL{u}}}}
	}
	return r
}

const validSAN = "spiffe://cowlab.org/ns/agent-sandbox/pod/opencode"

func TestBundleRequiresClientIdentity(t *testing.T) {
	s := sandboxedServer(t, stubFetcher{})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, requestWithIdentity(t, "/v1/repos/nixos/bundle", ""))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	// The body must be a fixed classifier, not resolver internals (pod names,
	// namespaces, API errors).
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode 401 body: %v", err)
	}
	if body["reason"] != "client certificate is not a valid sandbox identity" {
		t.Errorf("reason = %q, want the fixed classifier", body["reason"])
	}
	if strings.Contains(rec.Body.String(), "opencode") || strings.Contains(rec.Body.String(), "namespace") {
		t.Errorf("401 body leaks resolver detail: %s", rec.Body.String())
	}
}

func TestBundleServesAllowedRepo(t *testing.T) {
	s := sandboxedServer(t, stubFetcher{b: bundle.Bundle{Repo: "nixos", Commit: "abc123", Content: []byte("bundle")}})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, requestWithIdentity(t, "/v1/repos/nixos/bundle", validSAN))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Bundle-Commit"); got != "abc123" {
		t.Errorf("X-Bundle-Commit = %q, want abc123", got)
	}
	if rec.Body.String() != "bundle" {
		t.Errorf("body = %q, want bundle", rec.Body.String())
	}
}

func TestBundleRejectsUnknownRepo(t *testing.T) {
	s := sandboxedServer(t, stubFetcher{})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, requestWithIdentity(t, "/v1/repos/other/bundle", validSAN))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "unknown-repo") {
		t.Errorf("body = %s, want unknown-repo", rec.Body.String())
	}
}

func TestBundleReportsFetchFailure(t *testing.T) {
	s := sandboxedServer(t, stubFetcher{err: errors.New("boom")})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, requestWithIdentity(t, "/v1/repos/nixos/bundle", validSAN))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "boom") {
		t.Errorf("error body leaked the fetch error: %s", rec.Body.String())
	}
}
