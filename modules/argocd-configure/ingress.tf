resource "kubernetes_ingress_v1" "argocd_server" {
  metadata {
    name      = "argocd-server"
    namespace = "argocd"
    annotations = {
      "kubernetes.io/ingress.class"                         = "traefik-external"
      "traefik.ingress.kubernetes.io/router.entrypoints"    = "websecure"
      "traefik.ingress.kubernetes.io/router.tls"            = "true"
      "traefik.ingress.kubernetes.io/service.serversscheme" = "h2c"
    }
  }

  spec {
    tls {
      hosts       = [ var.argocd_host ]
      secret_name = "cowlab-production-tls"
    }

    rule {
      host = var.argocd_host
      http {
        path {
          path      = "/"
          path_type = "Prefix"
          backend {
            service {
              name = "argocd-server"
              port {
                number = 80
              }
            }
          }
        }
      }
    }
  }
}
