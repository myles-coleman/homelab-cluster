resource "kubernetes_namespace" "argocd" {
  metadata {
    name = "argocd"
  }
}

resource "random_password" "client_secret" {
  length  = 20
  special = false
}

resource "helm_release" "argocd" {
  name       = "argocd"
  repository = "https://argoproj.github.io/argo-helm"
  chart      = "argo-cd"
  version    = var.chart_version
  timeout      = 1200
  force_update = true
  namespace    = kubernetes_namespace.argocd.id
  values = [<<EOF
server:
  replicas: 1
  metrics:
    enabled: true

controller:
  replicas: 1
  metrics:
    enabled: true

repoServer:
  replicas: 1
  metrics:
    enabled: true

dex:
  metrics:
    enabled: true
  env:
    - name: ARGOCD_CLIENT_SECRET
      valueFrom:
        secretKeyRef:
          name: argocd-secret
          key: oidc.sso.clientSecret

configs:
  secret:
    extra:
      oidc.sso.clientID: argocd
      oidc.sso.clientSecret: ${random_password.client_secret.result}

  cm:
    exec.enabled: true
    admin.enabled: false

    url: ${var.argocd_url}
    oidc.config: |
      name: Dex
      issuer: ${var.dex_url}
      clientID: argocd
      clientSecret: $oidc.sso.clientSecret
      requestedScopes: ["openid", "profile", "email", "groups"]

      allowedAudiences:
      - argocd
      - kubernetes

  rbac:
    policy.csv: |
      g, ${var.github_username}, role:admin
      g, Cgg0NDUwNTg1NRIGZ2l0aHVi, role:admin
    policy.default: role:readonly

  params:
    "server.insecure": true
EOF
  ]
}
