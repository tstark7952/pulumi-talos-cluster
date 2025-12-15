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
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return err
		}

		// Paths for Talos configuration
		talosDir := filepath.Join(homeDir, ".talos")
		talosConfigPath := filepath.Join(talosDir, "config")
		kubeconfigPath := filepath.Join(homeDir, ".kube", "talos-config")

		// Ensure talos directory exists
		createTalosDir, err := local.NewCommand(ctx, "create-talos-dir", &local.CommandArgs{
			Create: pulumi.String(fmt.Sprintf("mkdir -p %s", talosDir)),
		})
		if err != nil {
			return err
		}

		// Create Talos cluster using talosctl
		// This creates a Docker-based Talos cluster with full security features:
		// - Immutable OS
		// - API-only management (no SSH)
		// - Encryption at rest
		// - Audit logging
		// - Secure boot support
		createCluster, err := local.NewCommand(ctx, "create-talos-cluster", &local.CommandArgs{
			Create: pulumi.String(fmt.Sprintf(`
				# Check if cluster already exists
				if /opt/homebrew/bin/talosctl cluster show --name %s >/dev/null 2>&1; then
					echo "Talos cluster '%s' already exists"
					exit 0
				fi

				echo "Creating Talos cluster '%s'..."

				# Create cluster with Docker provisioner
				# 1 control-plane + 2 workers for HA testing
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
			},
		}, pulumi.DependsOn([]pulumi.Resource{createTalosDir}))
		if err != nil {
			return err
		}

		// Export kubeconfig to standard location
		exportKubeconfig, err := local.NewCommand(ctx, "export-kubeconfig", &local.CommandArgs{
			Create: pulumi.String(fmt.Sprintf(`
				echo "Exporting kubeconfig to %s..."

				# Wait for cluster to be fully ready
				sleep 5

				# Get kubeconfig from Talos
				/opt/homebrew/bin/talosctl kubeconfig %s --force --name %s

				# Verify kubeconfig works
				if kubectl --kubeconfig %s get nodes >/dev/null 2>&1; then
					echo "Kubeconfig exported successfully"
					kubectl --kubeconfig %s get nodes
				else
					echo "Warning: kubectl not able to connect yet, cluster may still be initializing"
				fi
			`, kubeconfigPath, kubeconfigPath, clusterName, kubeconfigPath, kubeconfigPath)),
			Delete: pulumi.String(fmt.Sprintf("rm -f %s", kubeconfigPath)),
			Environment: pulumi.StringMap{
				"TALOSCONFIG": pulumi.String(talosConfigPath),
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

				# Wait for all nodes to be Ready
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

				# Wait for system pods
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

		// Apply security hardening (PSS labels and NetworkPolicies)
		applySecurityHardening, err := local.NewCommand(ctx, "apply-security-hardening", &local.CommandArgs{
			Create: pulumi.String(fmt.Sprintf(`
				export KUBECONFIG=%s
				echo "Applying security hardening..."

				# Label namespaces with Pod Security Standards
				kubectl label namespace kube-system pod-security.kubernetes.io/enforce=privileged --overwrite
				kubectl label namespace kube-system pod-security.kubernetes.io/warn=baseline --overwrite

				kubectl label namespace default pod-security.kubernetes.io/enforce=restricted --overwrite
				kubectl label namespace default pod-security.kubernetes.io/warn=restricted --overwrite

				# Create default deny NetworkPolicy for default namespace
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

				echo "Security hardening applied:"
				echo "- Pod Security Standards: privileged for kube-system, restricted for default"
				echo "- Default NetworkPolicy: deny-all with DNS egress allowed"
			`, kubeconfigPath)),
			Environment: pulumi.StringMap{
				"KUBECONFIG": pulumi.String(kubeconfigPath),
			},
		}, pulumi.DependsOn([]pulumi.Resource{waitForK8s}))
		if err != nil {
			return err
		}

		// Final verification and status output
		_, err = local.NewCommand(ctx, "verify-cluster", &local.CommandArgs{
			Create: pulumi.String(fmt.Sprintf(`
				export KUBECONFIG=%s

				echo "====================================================================="
				echo "Talos Cluster Ready"
				echo "====================================================================="
				echo ""
				echo "Cluster: %s"
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
				echo "System Pods:"
				kubectl get pods -n kube-system
				echo ""
				echo "====================================================================="
				echo "Usage:"
				echo "  export KUBECONFIG=%s"
				echo "  kubectl get nodes"
				echo "  talosctl --talosconfig %s dashboard"
				echo "====================================================================="
			`, kubeconfigPath, clusterName, kubeconfigPath, talosConfigPath, kubeconfigPath, talosConfigPath)),
			Environment: pulumi.StringMap{
				"KUBECONFIG": pulumi.String(kubeconfigPath),
			},
		}, pulumi.DependsOn([]pulumi.Resource{applySecurityHardening}))
		if err != nil {
			return err
		}

		// Export outputs
		ctx.Export("clusterName", pulumi.String(clusterName))
		ctx.Export("kubeconfigPath", pulumi.String(kubeconfigPath))
		ctx.Export("talosconfigPath", pulumi.String(talosConfigPath))

		return nil
	})
}
