# ArgoCD: the reconciler that makes Git the source of truth for what runs.
#
# It watches deploy/overlays/dev on main and continuously applies it. CI only
# ever commits to that path; nothing outside the cluster holds a kubeconfig.
# Delete a Deployment by hand and ArgoCD recreates it within seconds, which is
# the clearest demonstration of what "desired state" means.
#
# Trimmed for a single B2s node: no Dex (SSO), no notifications controller, no
# ApplicationSet controller. What remains is the API server, the repo server
# that renders Kustomize, the application controller that diffs and applies,
# and Redis as their cache.
resource "helm_release" "argocd" {
  name             = "argocd"
  repository       = "https://argoproj.github.io/argo-helm"
  chart            = "argo-cd"
  version          = "10.9.6"
  namespace        = "argocd"
  create_namespace = true

  wait    = true
  timeout = 600

  values = [yamlencode({
    global = {
      domain = "argocd.${var.dns_zone}"
    }
    configs = {
      params = {
        # nginx terminates TLS with the Let's Encrypt certificate. ArgoCD must
        # serve plain HTTP behind it, or it redirects to its own HTTPS and the
        # browser loops between the two.
        "server.insecure" = true
      }
    }
    dex            = { enabled = false }
    notifications  = { enabled = false }
    applicationSet = { enabled = false }

    controller = {
      resources = { requests = { cpu = "100m", memory = "256Mi" }, limits = { memory = "512Mi" } }
    }
    server = {
      resources = { requests = { cpu = "50m", memory = "96Mi" }, limits = { memory = "256Mi" } }
      ingress = {
        enabled          = true
        ingressClassName = "nginx"
        annotations = {
          "cert-manager.io/cluster-issuer" = "letsencrypt-prod"
        }
        extraTls = [{
          hosts      = ["argocd.${var.dns_zone}"]
          secretName = "argocd-server-tls"
        }]
      }
    }
    repoServer = {
      resources = { requests = { cpu = "50m", memory = "128Mi" }, limits = { memory = "384Mi" } }
    }
    redis = {
      resources = { requests = { cpu = "10m", memory = "32Mi" }, limits = { memory = "128Mi" } }
    }
  })]

  # The ingress needs nginx's IngressClass and cert-manager's issuer to exist.
  depends_on = [helm_release.ingress_nginx, helm_release.cert_manager]
}
