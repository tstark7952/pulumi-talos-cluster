package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/pulumi/pulumi-command/sdk/go/command/local"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

func main() {
	pulumi.Run(func(ctx *pulumi.Context) error {
		// Configuration
		clusterName := "talos-local"
		vmName := "talos-docker"
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return err
		}

		// Paths
		talosDir := filepath.Join(homeDir, ".talos")
		talosConfigPath := filepath.Join(talosDir, "config")
		kubeconfigPath := filepath.Join(homeDir, ".kube", "talos-config")
		dockerSocket := filepath.Join(homeDir, ".lima", vmName, "sock", "docker.sock")

		// Ensure directories exist
		createDirs, err := local.NewCommand(ctx, "create-dirs", &local.CommandArgs{
			Create: pulumi.String(fmt.Sprintf("mkdir -p %s ~/.kube", talosDir)),
		})
		if err != nil {
			return err
		}

		// Create Lima VM configuration for Docker
		limaConfigPath := "./talos-docker.yaml"
		limaConfig := `# Lima VM for Talos Docker provisioner
images:
- location: "https://cloud-images.ubuntu.com/releases/24.04/release/ubuntu-24.04-server-cloudimg-arm64.img"
  arch: "aarch64"
- location: "https://cloud-images.ubuntu.com/releases/24.04/release/ubuntu-24.04-server-cloudimg-amd64.img"
  arch: "x86_64"

cpus: 4
memory: "8GiB"
disk: "100GiB"

containerd:
  system: false
  user: false

provision:
- mode: system
  script: |
    #!/bin/bash
    set -eux -o pipefail

    # Install Docker
    if ! command -v docker &> /dev/null; then
      curl -fsSL https://get.docker.com | sh
      systemctl enable docker
      systemctl start docker
      usermod -aG docker $LIMA_CIDATA_USER
    fi

portForwards:
- guestSocket: "/var/run/docker.sock"
  hostSocket: "{{.Dir}}/sock/docker.sock"
`

		createLimaConfig, err := local.NewCommand(ctx, "create-lima-config", &local.CommandArgs{
			Create: pulumi.String(fmt.Sprintf("cat <<'EOF' > %s\n%s\nEOF", limaConfigPath, limaConfig)),
			Delete: pulumi.String(fmt.Sprintf("rm -f %s", limaConfigPath)),
		})
		if err != nil {
			return err
		}

		// Create Lima VM
		limaVm, err := local.NewCommand(ctx, "lima-vm", &local.CommandArgs{
			Create: pulumi.String(fmt.Sprintf(`
				# Check if VM already exists
				if limactl list --format json | grep -q '"name":"%s"'; then
					echo "VM %s already exists, checking status..."

					# Check if VM is running
					if limactl list --format json | grep -A 5 '"name":"%s"' | grep -q '"status":"Running"'; then
						echo "VM %s is already running"
					else
						echo "Starting existing VM %s..."
						limactl start %s
					fi
				else
					echo "Creating Lima VM %s..."
					limactl create --name=%s %s --tty=false
					limactl start %s
				fi

				# Wait for Docker to be ready
				echo "Waiting for Docker to be ready..."
				for i in $(seq 1 30); do
					if DOCKER_HOST="unix://%s" docker info >/dev/null 2>&1; then
						echo "Docker is ready"
						DOCKER_HOST="unix://%s" docker info | grep -E "Server Version|Operating System"
						break
					fi
					echo "Waiting for Docker... ($i/30)"
					sleep 5
				done
			`, vmName, vmName, vmName, vmName, vmName, vmName, vmName, vmName, limaConfigPath, vmName, dockerSocket, dockerSocket)),
			Delete: pulumi.String(fmt.Sprintf(`
				echo "Stopping and deleting Lima VM %s..."
				limactl stop %s 2>/dev/null || true
				limactl delete %s --force 2>/dev/null || true
				echo "Lima VM %s deleted"
			`, vmName, vmName, vmName, vmName)),
		}, pulumi.DependsOn([]pulumi.Resource{createDirs, createLimaConfig}))
		if err != nil {
			return err
		}

		// Create Talos cluster using talosctl
		createCluster, err := local.NewCommand(ctx, "create-talos-cluster", &local.CommandArgs{
			Create: pulumi.String(fmt.Sprintf(`
				# Check if cluster already exists
				if /opt/homebrew/bin/talosctl cluster show --name %s >/dev/null 2>&1; then
					echo "Talos cluster '%s' already exists"
					exit 0
				fi

				echo "Creating Talos cluster '%s'..."

				# Create cluster with Docker provisioner
				# 1 control-plane + 2 workers
				/opt/homebrew/bin/talosctl cluster create \
					--name %s \
					--controlplanes 1 \
					--workers 2 \
					--wait \
					--wait-timeout 10m

				echo "Talos cluster '%s' created successfully"
			`, clusterName, clusterName, clusterName, clusterName, clusterName)),
			Delete: pulumi.String(fmt.Sprintf(`
				echo "Destroying Talos cluster '%s'..."
				/opt/homebrew/bin/talosctl cluster destroy --name %s 2>/dev/null || true
				echo "Talos cluster '%s' destroyed"
			`, clusterName, clusterName, clusterName)),
			Environment: pulumi.StringMap{
				"TALOSCONFIG": pulumi.String(talosConfigPath),
				"DOCKER_HOST": pulumi.String(fmt.Sprintf("unix://%s", dockerSocket)),
			},
		}, pulumi.DependsOn([]pulumi.Resource{limaVm}))
		if err != nil {
			return err
		}

		// Export kubeconfig
		exportKubeconfig, err := local.NewCommand(ctx, "export-kubeconfig", &local.CommandArgs{
			Create: pulumi.String(fmt.Sprintf(`
				echo "Exporting kubeconfig to %s..."
				sleep 5

				# Get kubeconfig from Talos
				/opt/homebrew/bin/talosctl kubeconfig %s --force

				# Verify kubeconfig works
				if kubectl --kubeconfig %s get nodes >/dev/null 2>&1; then
					echo "Kubeconfig exported successfully"
					kubectl --kubeconfig %s get nodes
				else
					echo "Warning: kubectl not able to connect yet, cluster may still be initializing"
				fi
			`, kubeconfigPath, kubeconfigPath, kubeconfigPath, kubeconfigPath)),
			Delete: pulumi.String(fmt.Sprintf("rm -f %s", kubeconfigPath)),
			Environment: pulumi.StringMap{
				"TALOSCONFIG": pulumi.String(talosConfigPath),
				"DOCKER_HOST": pulumi.String(fmt.Sprintf("unix://%s", dockerSocket)),
			},
		}, pulumi.DependsOn([]pulumi.Resource{createCluster}))
		if err != nil {
			return err
		}

		// Wait for Kubernetes to be fully ready
		waitForK8s, err := local.NewCommand(ctx, "wait-for-kubernetes", &local.CommandArgs{
			Create: pulumi.String(fmt.Sprintf(`
				export KUBECONFIG=%s
				echo "Waiting for Kubernetes nodes to be ready..."

				for i in $(seq 1 60); do
					READY=$(kubectl get nodes --no-headers 2>/dev/null | grep -c " Ready " || echo "0")
					TOTAL=$(kubectl get nodes --no-headers 2>/dev/null | wc -l | tr -d ' ' || echo "0")

					if [ "$READY" = "$TOTAL" ] && [ "$TOTAL" -gt 0 ]; then
						echo "All $TOTAL nodes are ready"
						break
					fi

					echo "Waiting for nodes... ($READY/$TOTAL ready)"
					sleep 5
				done

				echo "Waiting for system pods..."
				kubectl wait --for=condition=Ready pods --all -n kube-system --timeout=300s 2>/dev/null || true

				echo "Kubernetes is ready"
				kubectl get nodes -o wide
			`, kubeconfigPath)),
			Environment: pulumi.StringMap{
				"KUBECONFIG": pulumi.String(kubeconfigPath),
			},
		}, pulumi.DependsOn([]pulumi.Resource{exportKubeconfig}))
		if err != nil {
			return err
		}

		// Apply security hardening
		applySecurityHardening, err := local.NewCommand(ctx, "apply-security-hardening", &local.CommandArgs{
			Create: pulumi.String(fmt.Sprintf(`
				export KUBECONFIG=%s
				echo "Applying security hardening..."

				# Label namespaces with Pod Security Standards
				kubectl label namespace kube-system pod-security.kubernetes.io/enforce=privileged --overwrite
				kubectl label namespace kube-system pod-security.kubernetes.io/warn=baseline --overwrite

				kubectl label namespace default pod-security.kubernetes.io/enforce=restricted --overwrite
				kubectl label namespace default pod-security.kubernetes.io/warn=restricted --overwrite

				# Create default deny NetworkPolicy
				cat <<'NETPOL' | kubectl apply -f -
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: default-deny-all
  namespace: default
spec:
  podSelector: {}
  policyTypes:
  - Ingress
  - Egress
---
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-dns
  namespace: default
spec:
  podSelector: {}
  policyTypes:
  - Egress
  egress:
  - to:
    - namespaceSelector:
        matchLabels:
          kubernetes.io/metadata.name: kube-system
    ports:
    - protocol: UDP
      port: 53
    - protocol: TCP
      port: 53
NETPOL

				echo "Security hardening applied"
			`, kubeconfigPath)),
			Environment: pulumi.StringMap{
				"KUBECONFIG": pulumi.String(kubeconfigPath),
			},
		}, pulumi.DependsOn([]pulumi.Resource{waitForK8s}))
		if err != nil {
			return err
		}

		// Final verification
		_, err = local.NewCommand(ctx, "verify-cluster", &local.CommandArgs{
			Create: pulumi.String(fmt.Sprintf(`
				export KUBECONFIG=%s

				echo "====================================================================="
				echo "Talos Cluster Ready"
				echo "====================================================================="
				echo ""
				echo "Cluster: %s"
				echo "Lima VM: %s"
				echo "Kubeconfig: %s"
				echo "Talosconfig: %s"
				echo ""
				echo "Security Features (built into Talos):"
				echo "  - Immutable OS (no shell, no SSH)"
				echo "  - API-only management"
				echo "  - Encryption at rest for etcd"
				echo "  - Secure boot capable"
				echo "  - Minimal attack surface"
				echo ""
				echo "Applied Hardening:"
				echo "  - Pod Security Standards enforced"
				echo "  - Default-deny NetworkPolicies"
				echo ""
				echo "Nodes:"
				kubectl get nodes -o wide
				echo ""
				echo "====================================================================="
				echo "Usage:"
				echo "  export KUBECONFIG=%s"
				echo "  kubectl get nodes"
				echo "  talosctl dashboard"
				echo "====================================================================="
			`, kubeconfigPath, clusterName, vmName, kubeconfigPath, talosConfigPath, kubeconfigPath)),
			Environment: pulumi.StringMap{
				"KUBECONFIG": pulumi.String(kubeconfigPath),
			},
		}, pulumi.DependsOn([]pulumi.Resource{applySecurityHardening}))
		if err != nil {
			return err
		}

		// Export outputs
		ctx.Export("clusterName", pulumi.String(clusterName))
		ctx.Export("vmName", pulumi.String(vmName))
		ctx.Export("kubeconfigPath", pulumi.String(kubeconfigPath))
		ctx.Export("talosconfigPath", pulumi.String(talosConfigPath))
		ctx.Export("dockerSocket", pulumi.String(dockerSocket))

		return nil
	})
}
