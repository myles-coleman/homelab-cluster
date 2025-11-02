resource "kubernetes_manifest" "bootstrap" {
  manifest = {
    apiVersion = "argoproj.io/v1alpha1"
    kind       = "Application"
    metadata = {
      name      = "bootstrap"
      namespace = "argocd"
    }
    spec = {
      destination = {
        server      = "https://kubernetes.default.svc"
        namespace   = "argocd"
      }
      project = "default"
      source = {
        path           = "manifests/bootstrap"
        repoURL        = "https://github.com/myles-coleman/homelab-cluster"
        targetRevision = "main"
      }
      syncPolicy = {
        automated = {
          prune     = true
          selfHeal  = false
        }
        retry = {
          backoff = {
            duration     = "30s"
            factor       = 2
            maxDuration  = "2m"
          }
          limit = 5
        }
      }
    }
  }
}
