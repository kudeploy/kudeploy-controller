//go:build e2e
// +build e2e

/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package e2e

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kudeploy/kudeploy-controller/test/utils"
)

// namespace where the project is deployed in
const namespace = "kudeploy-controller-system"

// serviceAccountName created for the project
const serviceAccountName = "kudeploy-controller-controller-manager"

// metricsServiceName is the name of the metrics service of the project
const metricsServiceName = "kudeploy-controller-controller-manager-metrics-service"

// metricsRoleBindingName is the name of the RBAC that will be created to allow get the metrics data
const metricsRoleBindingName = "kudeploy-controller-metrics-binding"

// serviceE2EProjectName is the Project used by the Kudeploy service E2E.
const serviceE2EProjectName = "kudeploy-e2e"

// serviceE2EName is the Service used by the Kudeploy service E2E.
const serviceE2EName = "whoami"

// serviceE2EFirstImage is the first image deployed by the Kudeploy service E2E.
const serviceE2EFirstImage = "ghcr.io/kudeploy/whoami:latest"

// serviceE2ESecondImage is the image used to trigger a second Service version.
const serviceE2ESecondImage = "docker.io/traefik/whoami:v1.10.3"

// buildRunE2EProjectName is the Project used by the BuildRun E2E.
const buildRunE2EProjectName = "buildrun-e2e"

// buildRunE2EName is the BuildRun used by the BuildRun E2E.
const buildRunE2EName = "whoami-build"

// buildRunE2EImage is the destination image used by the BuildRun E2E.
const buildRunE2EImage = "example.com/kudeploy/whoami:e2e"

var _ = Describe("Manager", Ordered, func() {
	var controllerPodName string

	// Before running the tests, set up the environment by creating the namespace,
	// enforce the restricted security policy to the namespace, installing CRDs,
	// and deploying the controller.
	BeforeAll(func() {
		By("creating manager namespace")
		cmd := exec.Command("kubectl", "create", "ns", namespace)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to create namespace")

		By("labeling the namespace to enforce the restricted security policy")
		cmd = exec.Command("kubectl", "label", "--overwrite", "ns", namespace,
			"pod-security.kubernetes.io/enforce=restricted")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to label namespace with restricted policy")

		By("installing CRDs")
		cmd = exec.Command("make", "install")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to install CRDs")

		By("deploying the controller-manager")
		cmd = exec.Command("make", "deploy", fmt.Sprintf("IMG=%s", managerImage))
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to deploy the controller-manager")
	})

	// After all tests have been executed, clean up by undeploying the controller, uninstalling CRDs,
	// and deleting the namespace.
	AfterAll(func() {
		By("cleaning up the curl pod for metrics")
		cmd := exec.Command("kubectl", "delete", "pod", "curl-metrics", "-n", namespace)
		_, _ = utils.Run(cmd)

		By("undeploying the controller-manager")
		cmd = exec.Command("make", "undeploy")
		_, _ = utils.Run(cmd)

		By("uninstalling CRDs")
		cmd = exec.Command("make", "uninstall")
		_, _ = utils.Run(cmd)

		By("removing manager namespace")
		cmd = exec.Command("kubectl", "delete", "ns", namespace)
		_, _ = utils.Run(cmd)
	})

	// After each test, check for failures and collect logs, events,
	// and pod descriptions for debugging.
	AfterEach(func() {
		specReport := CurrentSpecReport()
		if specReport.Failed() {
			By("Fetching controller manager pod logs")
			cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", namespace)
			controllerLogs, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Controller logs:\n %s", controllerLogs)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Controller logs: %s", err)
			}

			By("Fetching Kubernetes events")
			cmd = exec.Command("kubectl", "get", "events", "-n", namespace, "--sort-by=.lastTimestamp")
			eventsOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Kubernetes events:\n%s", eventsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Kubernetes events: %s", err)
			}

			By("Fetching curl-metrics logs")
			cmd = exec.Command("kubectl", "logs", "curl-metrics", "-n", namespace)
			metricsOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Metrics logs:\n %s", metricsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get curl-metrics logs: %s", err)
			}

			By("Fetching controller manager pod description")
			cmd = exec.Command("kubectl", "describe", "pod", controllerPodName, "-n", namespace)
			podDescription, err := utils.Run(cmd)
			if err == nil {
				fmt.Println("Pod description:\n", podDescription)
			} else {
				fmt.Println("Failed to describe controller pod")
			}
		}
	})

	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(time.Second)

	Context("Manager", func() {
		It("should run successfully", func() {
			By("validating that the controller-manager pod is running as expected")
			verifyControllerUp := func(g Gomega) {
				By("getting the name of the controller-manager pod")
				cmd := exec.Command("kubectl", "get",
					"pods", "-l", "control-plane=controller-manager",
					"-o", "go-template={{ range .items }}"+
						"{{ if not .metadata.deletionTimestamp }}"+
						"{{ .metadata.name }}"+
						"{{ \"\\n\" }}{{ end }}{{ end }}",
					"-n", namespace,
				)

				podOutput, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "Failed to retrieve controller-manager pod information")
				podNames := utils.GetNonEmptyLines(podOutput)
				g.Expect(podNames).To(HaveLen(1), "expected 1 controller pod running")
				controllerPodName = podNames[0]
				g.Expect(controllerPodName).To(ContainSubstring("controller-manager"))

				By("validating the pod's status")
				cmd = exec.Command("kubectl", "get",
					"pods", controllerPodName, "-o", "jsonpath={.status.phase}",
					"-n", namespace,
				)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Running"), "Incorrect controller-manager pod status")
			}
			Eventually(verifyControllerUp).Should(Succeed())
		})

		It("should ensure the metrics endpoint is serving metrics", func() {
			By("creating a ClusterRoleBinding for the service account to allow access to metrics")
			cmd := exec.Command("kubectl", "create", "clusterrolebinding", metricsRoleBindingName,
				"--clusterrole=kudeploy-controller-metrics-reader",
				fmt.Sprintf("--serviceaccount=%s:%s", namespace, serviceAccountName),
			)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create ClusterRoleBinding")

			By("validating that the metrics service is available")
			cmd = exec.Command("kubectl", "get", "service", metricsServiceName, "-n", namespace)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Metrics service should exist")

			By("getting the service account token")
			token, err := serviceAccountToken()
			Expect(err).NotTo(HaveOccurred())
			Expect(token).NotTo(BeEmpty())

			By("ensuring the controller pod is ready")
			verifyControllerPodReady := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pod", controllerPodName, "-n", namespace,
					"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("True"), "Controller pod not ready")
			}
			Eventually(verifyControllerPodReady, 3*time.Minute, time.Second).Should(Succeed())

			By("verifying that the controller manager is serving the metrics server")
			verifyMetricsServerStarted := func(g Gomega) {
				cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring("Serving metrics server"),
					"Metrics server not yet started")
			}
			Eventually(verifyMetricsServerStarted, 3*time.Minute, time.Second).Should(Succeed())

			// +kubebuilder:scaffold:e2e-metrics-webhooks-readiness

			By("creating the curl-metrics pod to access the metrics endpoint")
			cmd = exec.Command("kubectl", "run", "curl-metrics", "--restart=Never",
				"--namespace", namespace,
				"--image=curlimages/curl:latest",
				"--overrides",
				fmt.Sprintf(`{
					"spec": {
						"containers": [{
							"name": "curl",
							"image": "curlimages/curl:latest",
							"command": ["/bin/sh", "-c"],
							"args": [
								"for i in $(seq 1 30); do curl -v -k -H 'Authorization: Bearer %s' https://%s.%s.svc.cluster.local:8443/metrics && exit 0 || sleep 2; done; exit 1"
							],
							"securityContext": {
								"readOnlyRootFilesystem": true,
								"allowPrivilegeEscalation": false,
								"capabilities": {
									"drop": ["ALL"]
								},
								"runAsNonRoot": true,
								"runAsUser": 1000,
								"seccompProfile": {
									"type": "RuntimeDefault"
								}
							}
						}],
						"serviceAccountName": "%s"
					}
				}`, token, metricsServiceName, namespace, serviceAccountName))
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create curl-metrics pod")

			By("waiting for the curl-metrics pod to complete.")
			verifyCurlUp := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pods", "curl-metrics",
					"-o", "jsonpath={.status.phase}",
					"-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Succeeded"), "curl pod in wrong status")
			}
			Eventually(verifyCurlUp, 5*time.Minute).Should(Succeed())

			By("getting the metrics by checking curl-metrics logs")
			verifyMetricsAvailable := func(g Gomega) {
				metricsOutput, err := getMetricsOutput()
				g.Expect(err).NotTo(HaveOccurred(), "Failed to retrieve logs from curl pod")
				g.Expect(metricsOutput).NotTo(BeEmpty())
				g.Expect(metricsOutput).To(ContainSubstring("< HTTP/1.1 200 OK"))
			}
			Eventually(verifyMetricsAvailable, 2*time.Minute).Should(Succeed())
		})

		It("should reconcile Project and Service rolling updates", func() {
			By("cleaning up any stale Project from previous interrupted runs")
			cmd := exec.Command("kubectl", "delete", "project", serviceE2EProjectName, "--ignore-not-found")
			_, _ = utils.Run(cmd)

			defer func() {
				By("cleaning up the Kudeploy E2E Project")
				cmd := exec.Command("kubectl", "delete", "project", serviceE2EProjectName, "--ignore-not-found")
				_, _ = utils.Run(cmd)
			}()

			By("creating a Kudeploy Project")
			applyManifest("kudeploy-project", fmt.Sprintf(`apiVersion: kudeploy.com/v1alpha1
kind: Project
metadata:
  name: %s
spec: {}
`, serviceE2EProjectName))

			By("waiting for the Project namespace to be ready")
			waitForJSONPath(
				"project", serviceE2EProjectName, "",
				"{.status.namespaceName}", serviceE2EProjectName,
				2*time.Minute,
			)
			cmd = exec.Command("kubectl", "get", "namespace", serviceE2EProjectName,
				"-o", "jsonpath={.metadata.labels.kudeploy\\.com/project}")
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal(serviceE2EProjectName))

			By("creating a ConfigMap used by Service envFrom")
			applyManifest("kudeploy-service-config", fmt.Sprintf(`apiVersion: v1
kind: ConfigMap
metadata:
  name: whoami-config
  namespace: %s
data:
  APP_MODE: e2e
`, serviceE2EProjectName))

			By("creating a Kudeploy Service")
			applyManifest("kudeploy-service", serviceManifest(serviceE2EFirstImage))

			By("waiting for the first Kudeploy Deployment to become active")
			waitForJSONPath(
				"deployments.kudeploy.com", "whoami-00001", serviceE2EProjectName,
				"{.status.conditions[?(@.type=='Ready')].status}", "True",
				5*time.Minute,
			)
			waitForJSONPath(
				"services.kudeploy.com", serviceE2EName, serviceE2EProjectName,
				"{.status.activeDeploymentName}", "whoami-00001",
				2*time.Minute,
			)
			expectKubernetesServiceSelector("whoami-00001")
			expectDeploymentImageAndPolicy("whoami-00001", serviceE2EFirstImage)
			expectDeploymentEnv("whoami-00001", "LOG_LEVEL", "e2e")
			expectDeploymentEnvFromConfigMap("whoami-00001", "whoami-config")
			expectDeploymentEnvSecret("whoami-00001", "whoami-00001-env")
			expectHTTPResponseFromService("whoami-first", "Hostname:")

			By("updating the Service env Secret to create a second version")
			patchSecretData(serviceE2EProjectName, "service-whoami-env", "API_TOKEN", "one")

			By("waiting for the second Kudeploy Deployment from env Secret change to become active")
			waitForJSONPath(
				"deployments.kudeploy.com", "whoami-00002", serviceE2EProjectName,
				"{.status.conditions[?(@.type=='Ready')].status}", "True",
				5*time.Minute,
			)
			waitForJSONPath(
				"services.kudeploy.com", serviceE2EName, serviceE2EProjectName,
				"{.status.activeDeploymentName}", "whoami-00002",
				2*time.Minute,
			)
			expectKubernetesServiceSelector("whoami-00002")
			expectDeploymentEnvFromConfigMap("whoami-00002", "whoami-config")
			expectDeploymentEnvSecret("whoami-00002", "whoami-00002-env")
			expectSecretData(serviceE2EProjectName, "whoami-00002-env", "API_TOKEN", "one")

			By("updating the Service image to create a second version")
			applyManifest("kudeploy-service-update", serviceManifest(serviceE2ESecondImage))

			By("verifying traffic stays on the previous version while the new version starts")
			expectKubernetesServiceSelector("whoami-00002")

			By("waiting for the third Kudeploy Deployment to become active")
			waitForJSONPath(
				"deployments.kudeploy.com", "whoami-00003", serviceE2EProjectName,
				"{.status.conditions[?(@.type=='Ready')].status}", "True",
				5*time.Minute,
			)
			waitForJSONPath(
				"services.kudeploy.com", serviceE2EName, serviceE2EProjectName,
				"{.status.activeDeploymentName}", "whoami-00003",
				2*time.Minute,
			)
			expectKubernetesServiceSelector("whoami-00003")
			expectDeploymentImageAndPolicy("whoami-00003", serviceE2ESecondImage)
			expectDeploymentEnv("whoami-00003", "LOG_LEVEL", "e2e")
			expectDeploymentEnvFromConfigMap("whoami-00003", "whoami-config")
			expectDeploymentEnvSecret("whoami-00003", "whoami-00003-env")
			expectSecretData(serviceE2EProjectName, "whoami-00003-env", "API_TOKEN", "one")
			expectHTTPResponseFromService("whoami-second", "Hostname:")
		})

		It("should create Tekton resources for BuildRun", func() {
			By("cleaning up any stale BuildRun Project from previous interrupted runs")
			cmd := exec.Command("kubectl", "delete", "project", buildRunE2EProjectName, "--ignore-not-found")
			_, _ = utils.Run(cmd)

			defer func() {
				By("cleaning up the BuildRun E2E Project")
				cmd := exec.Command("kubectl", "delete", "project", buildRunE2EProjectName, "--ignore-not-found")
				_, _ = utils.Run(cmd)
			}()

			By("creating a Kudeploy Project for BuildRun")
			applyManifest("buildrun-project", fmt.Sprintf(`apiVersion: kudeploy.com/v1alpha1
kind: Project
metadata:
  name: %s
spec: {}
`, buildRunE2EProjectName))

			By("waiting for the BuildRun namespace to be ready")
			waitForJSONPath(
				"project", buildRunE2EProjectName, "",
				"{.status.namespaceName}", buildRunE2EProjectName,
				2*time.Minute,
			)

			By("creating a Kudeploy BuildRun")
			applyManifest("kudeploy-buildrun", buildRunManifest())

			By("waiting for the BuildRun controller to create Tekton resources")
			waitForJSONPath(
				"buildruns.kudeploy.com", buildRunE2EName, buildRunE2EProjectName,
				"{.status.conditions[?(@.type=='Ready')].reason}", "PipelineRunCreated",
				2*time.Minute,
			)
			waitForJSONPath(
				"buildruns.kudeploy.com", buildRunE2EName, buildRunE2EProjectName,
				"{.status.pipelineRunName}", buildRunE2EName,
				2*time.Minute,
			)
			waitForJSONPath(
				"buildruns.kudeploy.com", buildRunE2EName, buildRunE2EProjectName,
				"{.status.serviceAccountName}", "buildrun-"+buildRunE2EName,
				2*time.Minute,
			)

			By("verifying the generated BuildRun ServiceAccount")
			expectServiceAccountLabel("buildrun-"+buildRunE2EName, "kudeploy.com/buildrun", buildRunE2EName)
			expectServiceAccountLabel("buildrun-"+buildRunE2EName, "app.kubernetes.io/managed-by", "kudeploy")

			By("verifying the generated Tekton PipelineRun")
			expectPipelineRunLabel("kudeploy.com/buildrun", buildRunE2EName)
			expectPipelineRunField("{.spec.pipelineRef.resolver}", "http")
			expectPipelineRunField("{.spec.pipelineRef.params[?(@.name=='url')].value}", buildPipelineURL())
			expectPipelineRunField("{.spec.taskRunTemplate.serviceAccountName}", "buildrun-"+buildRunE2EName)
			expectPipelineRunField("{.spec.params[?(@.name=='git-url')].value}", "https://github.com/kudeploy/whoami")
			expectPipelineRunField("{.spec.params[?(@.name=='image')].value}", buildRunE2EImage)
			expectPipelineRunField("{.spec.params[?(@.name=='context')].value}", ".")
			expectPipelineRunField("{.spec.params[?(@.name=='dockerfile')].value}", "./Dockerfile")
			expectPipelineRunField("{.spec.workspaces[0].name}", "source")
		})

		// +kubebuilder:scaffold:e2e-webhooks-checks

		// TODO: Customize the e2e test suite with scenarios specific to your project.
		// Consider applying sample/CR(s) and check their status and/or verifying
		// the reconciliation by using the metrics, i.e.:
		// metricsOutput, err := getMetricsOutput()
		// Expect(err).NotTo(HaveOccurred(), "Failed to retrieve logs from curl pod")
		// Expect(metricsOutput).To(ContainSubstring(
		//    fmt.Sprintf(`controller_runtime_reconcile_total{controller="%s",result="success"} 1`,
		//    strings.ToLower(<Kind>),
		// ))
	})
})

// serviceAccountToken returns a token for the specified service account in the given namespace.
// It uses the Kubernetes TokenRequest API to generate a token by directly sending a request
// and parsing the resulting token from the API response.
func serviceAccountToken() (string, error) {
	const tokenRequestRawString = `{
		"apiVersion": "authentication.k8s.io/v1",
		"kind": "TokenRequest"
	}`

	By("creating temporary file to store the token request")
	secretName := fmt.Sprintf("%s-token-request", serviceAccountName)
	tokenRequestFile := filepath.Join("/tmp", secretName)
	err := os.WriteFile(tokenRequestFile, []byte(tokenRequestRawString), os.FileMode(0o644))
	if err != nil {
		return "", err
	}

	var out string
	verifyTokenCreation := func(g Gomega) {
		By("executing kubectl command to create the token")
		cmd := exec.Command("kubectl", "create", "--raw", fmt.Sprintf(
			"/api/v1/namespaces/%s/serviceaccounts/%s/token",
			namespace,
			serviceAccountName,
		), "-f", tokenRequestFile)

		output, err := cmd.CombinedOutput()
		g.Expect(err).NotTo(HaveOccurred())

		By("parsing the JSON output to extract the token")
		var token tokenRequest
		err = json.Unmarshal(output, &token)
		g.Expect(err).NotTo(HaveOccurred())

		out = token.Status.Token
	}
	Eventually(verifyTokenCreation).Should(Succeed())

	return out, err
}

// getMetricsOutput retrieves and returns the logs from the curl pod used to access the metrics endpoint.
func getMetricsOutput() (string, error) {
	By("getting the curl-metrics logs")
	cmd := exec.Command("kubectl", "logs", "curl-metrics", "-n", namespace)
	return utils.Run(cmd)
}

func applyManifest(name, manifest string) {
	manifestFile := filepath.Join("/tmp", fmt.Sprintf("%s.yaml", name))
	Expect(os.WriteFile(manifestFile, []byte(manifest), os.FileMode(0o644))).To(Succeed())

	cmd := exec.Command("kubectl", "apply", "-f", manifestFile)
	_, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())
}

func serviceManifest(image string) string {
	return fmt.Sprintf(`apiVersion: kudeploy.com/v1alpha1
kind: Service
metadata:
  name: %s
  namespace: %s
spec:
  image: %s
  env:
    - name: LOG_LEVEL
      value: e2e
  envFrom:
    - configMapRef:
        name: whoami-config
  ports:
    - port: 80
      targetPort: 80
`, serviceE2EName, serviceE2EProjectName, image)
}

func buildRunManifest() string {
	return fmt.Sprintf(`apiVersion: kudeploy.com/v1alpha1
kind: BuildRun
metadata:
  name: %s
  namespace: %s
spec:
  git:
    url: https://github.com/kudeploy/whoami
  image:
    repository: example.com/kudeploy/whoami
    tag: e2e
`, buildRunE2EName, buildRunE2EProjectName)
}

func waitForJSONPath(resource, name, resourceNamespace, jsonPath, expected string, timeout time.Duration) {
	verify := func(g Gomega) {
		args := []string{"get", resource, name}
		if resourceNamespace != "" {
			args = append(args, "-n", resourceNamespace)
		}
		args = append(args, "-o", fmt.Sprintf("jsonpath=%s", jsonPath))
		cmd := exec.Command("kubectl", args...)
		output, err := utils.Run(cmd)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(output).To(Equal(expected))
	}
	Eventually(verify, timeout, time.Second).Should(Succeed())
}

func expectKubernetesServiceSelector(deploymentName string) {
	cmd := exec.Command("kubectl", "get", "service", serviceE2EName,
		"-n", serviceE2EProjectName,
		"-o", "jsonpath={.spec.selector.kudeploy\\.com/deployment}",
	)
	output, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())
	Expect(output).To(Equal(deploymentName))
}

func expectDeploymentImageAndPolicy(deploymentName, image string) {
	cmd := exec.Command("kubectl", "get", "deployment", deploymentName,
		"-n", serviceE2EProjectName,
		"-o", "jsonpath={.spec.template.spec.containers[0].image}",
	)
	output, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())
	Expect(output).To(Equal(image))

	cmd = exec.Command("kubectl", "get", "deployment", deploymentName,
		"-n", serviceE2EProjectName,
		"-o", "jsonpath={.spec.template.spec.containers[0].imagePullPolicy}",
	)
	output, err = utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())
	Expect(output).To(Equal("Always"))
}

func expectDeploymentEnv(deploymentName, envName, expectedValue string) {
	cmd := exec.Command("kubectl", "get", "deployment", deploymentName,
		"-n", serviceE2EProjectName,
		"-o", fmt.Sprintf("jsonpath={.spec.template.spec.containers[0].env[?(@.name=='%s')].value}", envName),
	)
	output, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())
	Expect(output).To(Equal(expectedValue))
}

func expectDeploymentEnvFromConfigMap(deploymentName, configMapName string) {
	cmd := exec.Command("kubectl", "get", "deployment", deploymentName,
		"-n", serviceE2EProjectName,
		"-o", fmt.Sprintf("jsonpath={.spec.template.spec.containers[0].envFrom[?(@.configMapRef.name=='%s')].configMapRef.name}", configMapName),
	)
	output, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())
	Expect(output).To(Equal(configMapName))
}

func expectDeploymentEnvSecret(deploymentName, secretName string) {
	cmd := exec.Command("kubectl", "get", "deployment", deploymentName,
		"-n", serviceE2EProjectName,
		"-o", fmt.Sprintf("jsonpath={.spec.template.spec.containers[0].envFrom[?(@.secretRef.name=='%s')].secretRef.name}", secretName),
	)
	output, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())
	Expect(output).To(Equal(secretName))
}

func patchSecretData(namespaceName, secretName, key, value string) {
	encoded := base64.StdEncoding.EncodeToString([]byte(value))
	patch := fmt.Sprintf(`{"data":{"%s":"%s"}}`, key, encoded)
	cmd := exec.Command("kubectl", "patch", "secret", secretName,
		"-n", namespaceName,
		"--type", "merge",
		"-p", patch,
	)
	_, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())
}

func expectSecretData(namespaceName, secretName, key, expectedValue string) {
	cmd := exec.Command("kubectl", "get", "secret", secretName,
		"-n", namespaceName,
		"-o", fmt.Sprintf("jsonpath={.data.%s}", key),
	)
	output, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())
	decoded, err := base64.StdEncoding.DecodeString(output)
	Expect(err).NotTo(HaveOccurred())
	Expect(string(decoded)).To(Equal(expectedValue))
}

func expectHTTPResponseFromService(podName, expectedSubstring string) {
	cmd := exec.Command("kubectl", "delete", "pod", podName,
		"-n", serviceE2EProjectName, "--ignore-not-found")
	_, _ = utils.Run(cmd)

	cmd = exec.Command("kubectl", "run", podName,
		"--restart=Never",
		"--namespace", serviceE2EProjectName,
		"--image=curlimages/curl:latest",
		"--",
		"curl", "-sS", fmt.Sprintf("http://%s.%s.svc.cluster.local", serviceE2EName, serviceE2EProjectName),
	)
	_, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())

	waitForJSONPath("pod", podName, serviceE2EProjectName, "{.status.phase}", "Succeeded", 3*time.Minute)

	cmd = exec.Command("kubectl", "logs", podName, "-n", serviceE2EProjectName)
	output, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())
	Expect(output).To(ContainSubstring(expectedSubstring))
}

func expectServiceAccountLabel(serviceAccountName, labelKey, expectedValue string) {
	cmd := exec.Command("kubectl", "get", "serviceaccount", serviceAccountName,
		"-n", buildRunE2EProjectName,
		"-o", fmt.Sprintf("jsonpath={.metadata.labels.%s}", escapedJSONPathKey(labelKey)),
	)
	output, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())
	Expect(output).To(Equal(expectedValue))
}

func expectPipelineRunLabel(labelKey, expectedValue string) {
	expectPipelineRunField(fmt.Sprintf("{.metadata.labels.%s}", escapedJSONPathKey(labelKey)), expectedValue)
}

func expectPipelineRunField(jsonPath, expectedValue string) {
	cmd := exec.Command("kubectl", "get", "pipelinerun", buildRunE2EName,
		"-n", buildRunE2EProjectName,
		"-o", fmt.Sprintf("jsonpath=%s", jsonPath),
	)
	output, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())
	Expect(output).To(Equal(expectedValue))
}

func escapedJSONPathKey(key string) string {
	return strings.ReplaceAll(key, ".", "\\.")
}

func buildPipelineURL() string {
	return "https://raw.githubusercontent.com/kudeploy/kudeploy-manifests/main/tekton/pipelines/build-and-push.yaml"
}

// tokenRequest is a simplified representation of the Kubernetes TokenRequest API response,
// containing only the token field that we need to extract.
type tokenRequest struct {
	Status struct {
		Token string `json:"token"`
	} `json:"status"`
}
