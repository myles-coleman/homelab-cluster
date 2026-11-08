// Package publish: GitFetcher adapts gitbundle to BundleServer.
package publish

import (
	"context"

	"github.com/myles-coleman/homelab-cluster/services/agent-broker/internal/gitbundle"
)

// GitFetcher clones with the token the server minted for this request.
type GitFetcher struct {
	Owner string
	Opts  gitbundle.Options
}

// Fetch implements BundleFetcher.
func (g GitFetcher) Fetch(ctx context.Context, repo, token string) (string, []byte, error) {
	opts := g.Opts
	opts.Token = token
	return gitbundle.Fetch(ctx, opts, g.Owner, repo)
}
