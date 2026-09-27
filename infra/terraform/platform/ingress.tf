# ingress-nginx: one pod that terminates TLS and routes by Host/path to
# internal Services. Its own Service is type LoadBalancer, which the Azure
# cloud controller turns into a real load balancer and public IP. Watch it
# happen with: kubectl get svc -n ingress-nginx -w
resource "helm_release" "ingress_nginx" {
  name             = "ingress-nginx"
  repository       = "https://kubernetes.github.io/ingress-nginx"
  chart            = "ingress-nginx"
  version          = "4.15.1"
  namespace        = "ingress-nginx"
  create_namespace = true

  # Block until the LoadBalancer has an external IP, so the DNS records above
  # can read it in the same apply.
  wait    = true
  timeout = 600

  values = [yamlencode({
    controller = {
      replicaCount = 1
      # A B2s has ~800m of schedulable CPU left after the system pods. These
      # are requests (what the scheduler reserves), not usage.
      resources = {
        requests = { cpu = "50m", memory = "128Mi" }
        limits   = { memory = "256Mi" }
      }
      service = {
        annotations = {
          # Azure's load balancer health-probes the backend on "/" by default,
          # which nginx answers with 404, and the LB then marks the node
          # unhealthy and drops traffic. Probe the controller's real health
          # endpoint instead. This is the single most common "ingress-nginx on
          # AKS has an IP but nothing connects" cause.
          "service.beta.kubernetes.io/azure-load-balancer-health-probe-request-path" = "/healthz"
        }
      }
    }
  })]
}

# cert-manager: watches Certificate/Ingress objects, performs the ACME
# challenge with Let's Encrypt, and stores the issued cert in a Secret that
# ingress-nginx serves. The ClusterIssuer that names Let's Encrypt lives with
# the application manifests, because it is a Kubernetes object that needs
# cert-manager's CRDs to exist first.
resource "helm_release" "cert_manager" {
  name             = "cert-manager"
  repository       = "https://charts.jetstack.io"
  chart            = "cert-manager"
  version          = "v1.21.2"
  namespace        = "cert-manager"
  create_namespace = true

  wait    = true
  timeout = 300

  values = [yamlencode({
    crds       = { enabled = true }
    resources  = { requests = { cpu = "10m", memory = "64Mi" } }
    webhook    = { resources = { requests = { cpu = "10m", memory = "32Mi" } } }
    cainjector = { resources = { requests = { cpu = "10m", memory = "64Mi" } } }
  })]
}

locals {
  tags = {
    project     = "optifuse"
    environment = "dev"
    managed_by  = "terraform"
  }
}
