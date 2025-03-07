package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/metrics/pkg/apis/metrics/v1beta1"
	metrics "k8s.io/metrics/pkg/client/clientset/versioned"
	"sigs.k8s.io/yaml"
)

type NamespaceInfo struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

type DeploymentInfo struct {
	Name           string    `json:"name"`
	Images         []string  `json:"images"`
	Replicas       int32     `json:"replicas"`
	ReadyReplicas  int32     `json:"readyReplicas"`
	LastUpdateTime time.Time `json:"lastUpdateTime"`
}

type ReplicaSetInfo struct {
	Name          string    `json:"name"`
	Images        []string  `json:"images"`
	Replicas      int32     `json:"replicas"`
	ReadyReplicas int32     `json:"readyReplicas"`
	Owner         string    `json:"owner"`
	CreationTime  time.Time `json:"creationTime"`
}

type StatefulSetInfo struct {
	Name           string    `json:"name"`
	Images         []string  `json:"images"`
	Replicas       int32     `json:"replicas"`
	ReadyReplicas  int32     `json:"readyReplicas"`
	LastUpdateTime time.Time `json:"lastUpdateTime"`
}

type ContainerInfo struct {
	Name  string `json:"name"`
	Image string `json:"image"`
	Ports []struct {
		Name          string `json:"name"`
		ContainerPort int32  `json:"containerPort"`
		Protocol      string `json:"protocol"`
	} `json:"ports"`
	Resources struct {
		Requests struct {
			CPU    string `json:"cpu"`
			Memory string `json:"memory"`
		} `json:"requests"`
		Limits struct {
			CPU    string `json:"cpu"`
			Memory string `json:"memory"`
		} `json:"limits"`
		Usage struct {
			CPU    string `json:"cpu"`
			Memory string `json:"memory"`
		} `json:"usage"`
	} `json:"resources"`
}

type PodInfo struct {
	Name          string          `json:"name"`
	Images        []string        `json:"images"`
	Containers    []ContainerInfo `json:"containers"`
	Status        string          `json:"status"`
	StatusMessage string          `json:"statusMessage"`
	StartTime     time.Time       `json:"startTime"`
	Owner         string          `json:"owner"`
	OwnerKind     string          `json:"ownerKind"`
}

type Workloads struct {
	Deployments  []DeploymentInfo  `json:"deployments"`
	ReplicaSets  []ReplicaSetInfo  `json:"replica_sets"`
	StatefulSets []StatefulSetInfo `json:"stateful_sets"`
	Pods         []PodInfo         `json:"pods"`
}

// Add new types for clean manifests
type CleanManifest struct {
	APIVersion string                 `json:"apiVersion" yaml:"apiVersion"`
	Kind       string                 `json:"kind" yaml:"kind"`
	Metadata   CleanMetadata          `json:"metadata" yaml:"metadata"`
	Spec       map[string]interface{} `json:"spec" yaml:"spec"`
}

type CleanMetadata struct {
	Name      string            `json:"name" yaml:"name"`
	Namespace string            `json:"namespace" yaml:"namespace"`
	Labels    map[string]string `json:"labels,omitempty" yaml:"labels,omitempty"`
}

type EventInfo struct {
	Type           string    `json:"type"`
	Reason         string    `json:"reason"`
	Message        string    `json:"message"`
	Count          int32     `json:"count"`
	FirstSeen      time.Time `json:"firstSeen"`
	LastSeen       time.Time `json:"lastSeen"`
	InvolvedObject string    `json:"involvedObject"`
}

type EventsCache struct {
	events    map[string][]EventInfo
	counts    map[string]int
	timestamp time.Time
	mutex     sync.RWMutex
}

var eventsCache = &EventsCache{
	events:    make(map[string][]EventInfo),
	counts:    make(map[string]int),
	timestamp: time.Time{},
}

func (ec *EventsCache) needsRefresh() bool {
	ec.mutex.RLock()
	defer ec.mutex.RUnlock()
	return time.Since(ec.timestamp) > 10*time.Second
}

func (ec *EventsCache) update(namespace string, events []EventInfo) {
	ec.mutex.Lock()
	defer ec.mutex.Unlock()

	ec.events = make(map[string][]EventInfo)
	ec.counts = make(map[string]int)
	ec.timestamp = time.Now()

	// Group events by resource
	for _, event := range events {
		key := event.InvolvedObject
		ec.events[key] = append(ec.events[key], event)
		ec.counts[key] = len(ec.events[key])
	}
}

func (ec *EventsCache) getEvents(key string) []EventInfo {
	ec.mutex.RLock()
	defer ec.mutex.RUnlock()
	return ec.events[key]
}

func (ec *EventsCache) getCount(key string) int {
	ec.mutex.RLock()
	defer ec.mutex.RUnlock()
	return ec.counts[key]
}

func refreshEventsCache(clientset *kubernetes.Clientset, namespace string) error {
	events, err := clientset.CoreV1().Events(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return err
	}

	var eventInfos []EventInfo
	for _, event := range events.Items {
		eventInfos = append(eventInfos, EventInfo{
			Type:           event.Type,
			Reason:         event.Reason,
			Message:        event.Message,
			Count:          event.Count,
			FirstSeen:      event.FirstTimestamp.Time,
			LastSeen:       event.LastTimestamp.Time,
			InvolvedObject: fmt.Sprintf("%s/%s", strings.ToLower(event.InvolvedObject.Kind), event.InvolvedObject.Name),
		})
	}

	// Sort events by last seen time, newest first
	sort.Slice(eventInfos, func(i, j int) bool {
		return eventInfos[i].LastSeen.After(eventInfos[j].LastSeen)
	})

	eventsCache.update(namespace, eventInfos)
	return nil
}

func getEvents(clientset *kubernetes.Clientset, namespace, resourceType, name string) ([]EventInfo, error) {
	if eventsCache.needsRefresh() {
		if err := refreshEventsCache(clientset, namespace); err != nil {
			return nil, err
		}
	}

	key := fmt.Sprintf("%s/%s", strings.ToLower(resourceType), name)
	return eventsCache.getEvents(key), nil
}

func getConfig() (*rest.Config, error) {
	if kubeconfig := os.Getenv("KUBECONFIG"); kubeconfig != "" {
		return clientcmd.BuildConfigFromFlags("", kubeconfig)
	}
	home := os.Getenv("HOME")
	if home == "" {
		return rest.InClusterConfig()
	}
	kubeconfigPath := filepath.Join(home, ".kube", "config")
	if _, err := os.Stat(kubeconfigPath); os.IsNotExist(err) {
		return rest.InClusterConfig()
	}
	return clientcmd.BuildConfigFromFlags("", kubeconfigPath)
}

func logRequest(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		handler.ServeHTTP(w, r)
		fmt.Printf("[%s] %s %s %v\n",
			time.Now().Format("2006-01-02 15:04:05"),
			r.Method,
			r.URL.Path,
			time.Since(start),
		)
	})
}

func serveStaticFiles(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, "./static/index.html")
}

func httpNamespaceHandler(clientset *kubernetes.Clientset) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		namespaces, err := getAllNamespaces(clientset)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		js, err := json.Marshal(namespaces)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write(js)
	}
}

func httpWorkloadsHandler(clientset *kubernetes.Clientset, metricsClient *metrics.Clientset) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		namespace := strings.TrimPrefix(r.URL.Path, "/api/workloads/")
		if namespace == "" {
			http.Error(w, "Namespace must be provided", http.StatusBadRequest)
			return
		}

		deploymentInfo, err := getDeploymentInfo(clientset, namespace)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		replicaSetInfo, err := getReplicaSetInfo(clientset, namespace)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		statefulSetInfo, err := getStatefulSetInfo(clientset, namespace)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		podInfo, err := getPodInfo(clientset, metricsClient, namespace)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		workloads := Workloads{
			Deployments:  deploymentInfo,
			ReplicaSets:  replicaSetInfo,
			StatefulSets: statefulSetInfo,
			Pods:         podInfo,
		}

		js, err := json.Marshal(workloads)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write(js)
	}
}

func getAllNamespaces(clientset *kubernetes.Clientset) ([]NamespaceInfo, error) {
	namespaces, err := clientset.CoreV1().Namespaces().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	var nsInfo []NamespaceInfo
	for _, namespace := range namespaces.Items {
		nsInfo = append(nsInfo, NamespaceInfo{
			Name:   namespace.Name,
			Status: string(namespace.Status.Phase),
		})
	}
	return nsInfo, nil
}

func getDeploymentInfo(clientset *kubernetes.Clientset, namespace string) ([]DeploymentInfo, error) {
	deployments, err := clientset.AppsV1().Deployments(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	var deploymentInfos []DeploymentInfo
	for _, deployment := range deployments.Items {
		lastUpdateTime := time.Time{}
		for _, condition := range deployment.Status.Conditions {
			if condition.Type == "Progressing" {
				lastUpdateTime = condition.LastUpdateTime.Time
				break
			}
		}

		images := make([]string, len(deployment.Spec.Template.Spec.Containers))
		for i, container := range deployment.Spec.Template.Spec.Containers {
			images[i] = container.Image
		}

		deploymentInfos = append(deploymentInfos, DeploymentInfo{
			Name:           deployment.Name,
			Images:         images,
			Replicas:       *deployment.Spec.Replicas,
			ReadyReplicas:  deployment.Status.ReadyReplicas,
			LastUpdateTime: lastUpdateTime,
		})
	}
	return deploymentInfos, nil
}

func getReplicaSetInfo(clientset *kubernetes.Clientset, namespace string) ([]ReplicaSetInfo, error) {
	replicaSets, err := clientset.AppsV1().ReplicaSets(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	var replicaSetInfos []ReplicaSetInfo
	for _, replicaSet := range replicaSets.Items {
		if replicaSet.Status.Replicas > 0 {
			owner := ""
			if len(replicaSet.OwnerReferences) > 0 {
				owner = replicaSet.OwnerReferences[0].Name
			}

			images := []string{}
			for _, container := range replicaSet.Spec.Template.Spec.Containers {
				images = append(images, container.Image)
			}

			replicaSetInfos = append(replicaSetInfos, ReplicaSetInfo{
				Name:          replicaSet.Name,
				Replicas:      replicaSet.Status.Replicas,
				ReadyReplicas: replicaSet.Status.ReadyReplicas,
				Owner:         owner,
				Images:        images,
				CreationTime:  replicaSet.ObjectMeta.CreationTimestamp.Time,
			})
		}
	}
	return replicaSetInfos, nil
}

func getStatefulSetInfo(clientset *kubernetes.Clientset, namespace string) ([]StatefulSetInfo, error) {
	statefulsets, err := clientset.AppsV1().StatefulSets(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	var statefulSetInfos []StatefulSetInfo
	for _, statefulset := range statefulsets.Items {
		lastUpdateTime := statefulset.ObjectMeta.CreationTimestamp.Time // Default to creation time

		// Try to get a more recent update time from conditions if available
		for _, condition := range statefulset.Status.Conditions {
			if condition.Type == appsv1.StatefulSetConditionType("Progressing") {
				lastUpdateTime = condition.LastTransitionTime.Time
				break
			}
		}

		images := make([]string, len(statefulset.Spec.Template.Spec.Containers))
		for i, container := range statefulset.Spec.Template.Spec.Containers {
			images[i] = container.Image
		}

		statefulSetInfos = append(statefulSetInfos, StatefulSetInfo{
			Name:           statefulset.Name,
			Images:         images,
			Replicas:       *statefulset.Spec.Replicas,
			ReadyReplicas:  statefulset.Status.ReadyReplicas,
			LastUpdateTime: lastUpdateTime,
		})
	}
	return statefulSetInfos, nil
}

func getPodInfo(clientset *kubernetes.Clientset, metricsClient *metrics.Clientset, namespace string) ([]PodInfo, error) {
	pods, err := clientset.CoreV1().Pods(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	// Get metrics for all pods in the namespace
	var podMetrics *v1beta1.PodMetricsList
	if metricsClient != nil {
		podMetrics, err = metricsClient.MetricsV1beta1().PodMetricses(namespace).List(context.TODO(), metav1.ListOptions{})
		if err != nil {
			fmt.Printf("Warning: Failed to get pod metrics: %v\n", err)
		}
	}

	// Create a map of pod name to metrics for quick lookup
	metricsMap := make(map[string]v1beta1.PodMetrics)
	if podMetrics != nil {
		for _, metric := range podMetrics.Items {
			metricsMap[metric.Name] = metric
		}
	}

	var podInfoList []PodInfo
	for _, pod := range pods.Items {
		owner := ""
		ownerKind := ""

		if len(pod.OwnerReferences) > 0 {
			for _, ownerRef := range pod.OwnerReferences {
				if ownerRef.Kind == "StatefulSet" || ownerRef.Kind == "ReplicaSet" {
					owner = ownerRef.Name
					ownerKind = ownerRef.Kind
					break
				}
			}
		}

		images := []string{}
		containers := []ContainerInfo{}

		// Get pod metrics if available
		podMetric, hasMetrics := metricsMap[pod.Name]

		for _, container := range pod.Spec.Containers {
			containerInfo := ContainerInfo{
				Name:  container.Name,
				Image: container.Image,
			}

			// Add ports information
			for _, port := range container.Ports {
				containerInfo.Ports = append(containerInfo.Ports, struct {
					Name          string `json:"name"`
					ContainerPort int32  `json:"containerPort"`
					Protocol      string `json:"protocol"`
				}{
					Name:          port.Name,
					ContainerPort: port.ContainerPort,
					Protocol:      string(port.Protocol),
				})
			}

			// Set resource requests
			if container.Resources.Requests != nil {
				containerInfo.Resources.Requests.CPU = container.Resources.Requests.Cpu().String()
				containerInfo.Resources.Requests.Memory = container.Resources.Requests.Memory().String()
			}

			// Set resource limits
			if container.Resources.Limits != nil {
				containerInfo.Resources.Limits.CPU = container.Resources.Limits.Cpu().String()
				containerInfo.Resources.Limits.Memory = container.Resources.Limits.Memory().String()
			}

			// Set current usage if metrics are available
			if hasMetrics {
				for _, containerMetric := range podMetric.Containers {
					if containerMetric.Name == container.Name {
						containerInfo.Resources.Usage.CPU = containerMetric.Usage.Cpu().String()
						containerInfo.Resources.Usage.Memory = containerMetric.Usage.Memory().String()
						break
					}
				}
			}

			containers = append(containers, containerInfo)
			images = append(images, container.Image)
		}

		// Get detailed pod status
		status := string(pod.Status.Phase)
		statusMessage := ""

		// Check container statuses
		var unreadyContainers []string
		for _, containerStatus := range pod.Status.ContainerStatuses {
			if !containerStatus.Ready {
				unreadyContainers = append(unreadyContainers, containerStatus.Name)
				// Check for detailed status
				if containerStatus.State.Waiting != nil {
					status = containerStatus.State.Waiting.Reason
					statusMessage = containerStatus.State.Waiting.Message
				} else if containerStatus.State.Terminated != nil {
					status = containerStatus.State.Terminated.Reason
					statusMessage = containerStatus.State.Terminated.Message
				}
			}
		}

		// Check pod conditions
		if len(unreadyContainers) > 0 {
			status = "ContainersNotReady"
			statusMessage = fmt.Sprintf("Containers with unready status: [%s]", strings.Join(unreadyContainers, ", "))
		}

		// Check for pod conditions
		for _, condition := range pod.Status.Conditions {
			if condition.Status == corev1.ConditionFalse {
				switch condition.Type {
				case corev1.PodScheduled:
					if condition.Reason == "Unschedulable" {
						status = "Unschedulable"
						statusMessage = condition.Message
					}
				case corev1.PodReady:
					if status == "Running" {
						status = "NotReady"
						statusMessage = condition.Message
					}
				case corev1.ContainersReady:
					if status == "Running" {
						status = "ContainersNotReady"
						statusMessage = condition.Message
					}
				}
			}
		}

		if ownerKind == "StatefulSet" || ownerKind == "ReplicaSet" {
			podInfoList = append(podInfoList, PodInfo{
				Name:          pod.Name,
				Status:        status,
				StatusMessage: statusMessage,
				StartTime:     pod.Status.StartTime.Time,
				Owner:         owner,
				OwnerKind:     ownerKind,
				Images:        images,
				Containers:    containers,
			})
		}
	}
	return podInfoList, nil
}

func getTimesForWorkload(meta metav1.ObjectMeta, conditions []appsv1.DeploymentCondition) (lastUpdateTime time.Time) {
	for _, condition := range conditions {
		if condition.Type == appsv1.DeploymentProgressing {
			lastUpdateTime = condition.LastUpdateTime.Time
			break
		}
	}
	return
}

func httpManifestHandler(clientset *kubernetes.Clientset) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/manifest/"), "/")
		if len(parts) != 3 {
			http.Error(w, "Invalid URL format. Expected: /api/manifest/{namespace}/{type}/{name}", http.StatusBadRequest)
			return
		}

		namespace := parts[0]
		resourceType := strings.ToLower(parts[1])
		name := parts[2]

		// Reject Pod manifest requests
		if resourceType == "pod" {
			http.Error(w, "Pod manifests are not available", http.StatusBadRequest)
			return
		}

		var cleanManifest CleanManifest
		var resourcePath string

		switch resourceType {
		case "deployment":
			obj, err := clientset.AppsV1().Deployments(namespace).Get(context.TODO(), name, metav1.GetOptions{})
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			resourcePath = fmt.Sprintf("deployment/%s", name)

			// Convert deployment spec to map
			specBytes, err := json.Marshal(obj.Spec)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			var specMap map[string]interface{}
			if err := json.Unmarshal(specBytes, &specMap); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}

			cleanManifest = CleanManifest{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Metadata: CleanMetadata{
					Name:      obj.Name,
					Namespace: obj.Namespace,
					Labels:    obj.Labels,
				},
				Spec: specMap,
			}

		case "statefulset":
			obj, err := clientset.AppsV1().StatefulSets(namespace).Get(context.TODO(), name, metav1.GetOptions{})
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			resourcePath = fmt.Sprintf("statefulset/%s", name)

			// Convert statefulset spec to map
			specBytes, err := json.Marshal(obj.Spec)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			var specMap map[string]interface{}
			if err := json.Unmarshal(specBytes, &specMap); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}

			cleanManifest = CleanManifest{
				APIVersion: "apps/v1",
				Kind:       "StatefulSet",
				Metadata: CleanMetadata{
					Name:      obj.Name,
					Namespace: obj.Namespace,
					Labels:    obj.Labels,
				},
				Spec: specMap,
			}

		default:
			http.Error(w, fmt.Sprintf("Unsupported resource type: %s", resourceType), http.StatusBadRequest)
			return
		}

		// Convert to YAML with proper indentation
		yamlData, err := yaml.Marshal(cleanManifest)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// Add a header comment with resource path
		header := fmt.Sprintf("# Resource: %s in namespace '%s'\n# Path: %s\n---\n",
			strings.Title(resourceType), namespace, resourcePath)

		w.Header().Set("Content-Type", "application/yaml")
		w.Write([]byte(header))
		w.Write(yamlData)
	}
}

func httpLogsHandler(clientset *kubernetes.Clientset) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/logs/"), "/")
		if len(parts) != 2 {
			http.Error(w, "Invalid URL format. Expected: /api/logs/{namespace}/{pod}", http.StatusBadRequest)
			return
		}

		namespace := parts[0]
		podName := parts[1]

		// First check if pod exists
		pod, err := clientset.CoreV1().Pods(namespace).Get(context.Background(), podName, metav1.GetOptions{})
		if err != nil {
			if strings.Contains(err.Error(), "not found") {
				http.Error(w, fmt.Sprintf("Pod %s not found in namespace %s", podName, namespace), http.StatusNotFound)
			} else {
				http.Error(w, fmt.Sprintf("Error accessing pod: %v", err), http.StatusInternalServerError)
			}
			return
		}

		// Get query parameters
		tailLines := 100 // default value
		if tailParam := r.URL.Query().Get("tail"); tailParam != "" {
			if val, err := strconv.Atoi(tailParam); err == nil && val > 0 {
				tailLines = val
			}
		}

		containerName := r.URL.Query().Get("container")
		if containerName == "" && len(pod.Spec.Containers) > 0 {
			// If no container specified and pod has containers, use the first one
			containerName = pod.Spec.Containers[0].Name
		}

		// Validate container exists in pod
		containerExists := false
		for _, container := range pod.Spec.Containers {
			if container.Name == containerName {
				containerExists = true
				break
			}
		}
		if !containerExists {
			http.Error(w, fmt.Sprintf("Container %s not found in pod %s", containerName, podName), http.StatusNotFound)
			return
		}

		// Set up request for logs
		podLogOpts := &corev1.PodLogOptions{
			Container: containerName,
			TailLines: int64Ptr(int64(tailLines)),
			Follow:    false,
		}

		req := clientset.CoreV1().Pods(namespace).GetLogs(podName, podLogOpts)
		podLogs, err := req.Stream(context.Background())
		if err != nil {
			if strings.Contains(err.Error(), "not found") {
				http.Error(w, fmt.Sprintf("Logs not available for container %s in pod %s", containerName, podName), http.StatusNotFound)
			} else {
				http.Error(w, fmt.Sprintf("Error getting logs: %v", err), http.StatusInternalServerError)
			}
			return
		}
		defer podLogs.Close()

		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("X-Content-Type-Options", "nosniff")

		_, err = io.Copy(w, podLogs)
		if err != nil {
			fmt.Printf("Error streaming logs: %v\n", err)
		}
	}
}

func int64Ptr(i int64) *int64 {
	return &i
}

func httpEventsHandler(clientset *kubernetes.Clientset) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/events/"), "/")
		if len(parts) != 3 {
			http.Error(w, "Invalid URL format. Expected: /api/events/{namespace}/{type}/{name}", http.StatusBadRequest)
			return
		}

		namespace := parts[0]
		resourceType := strings.ToLower(parts[1])
		name := parts[2]

		events, err := getEvents(clientset, namespace, resourceType, name)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		js, err := json.Marshal(events)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write(js)
	}
}

func httpEventsCountHandler(clientset *kubernetes.Clientset) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/events-count/"), "/")
		if len(parts) != 3 {
			http.Error(w, "Invalid URL format. Expected: /api/events-count/{namespace}/{type}/{name}", http.StatusBadRequest)
			return
		}

		namespace := parts[0]
		resourceType := strings.ToLower(parts[1])
		name := parts[2]

		if eventsCache.needsRefresh() {
			if err := refreshEventsCache(clientset, namespace); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}

		key := fmt.Sprintf("%s/%s", resourceType, name)
		count := eventsCache.getCount(key)

		response := struct {
			Count int `json:"count"`
		}{
			Count: count,
		}

		js, err := json.Marshal(response)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write(js)
	}
}

func main() {
	bindIP := os.Getenv("BIND_IP")
	if bindIP == "" {
		bindIP = "0.0.0.0"
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	if _, err := strconv.Atoi(port); err != nil {
		fmt.Printf("Invalid port number: %s\n", port)
		os.Exit(1)
	}

	config, err := getConfig()
	if err != nil {
		fmt.Printf("Error getting Kubernetes config: %v\n", err)
		os.Exit(1)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		fmt.Printf("Error creating Kubernetes client: %v\n", err)
		os.Exit(1)
	}

	metricsClient, err := metrics.NewForConfig(config)
	if err != nil {
		fmt.Printf("Warning: Failed to create metrics client. Resource usage information will not be available: %v\n", err)
	}

	mux := http.NewServeMux()

	mux.HandleFunc("GET /", serveStaticFiles)
	mux.HandleFunc("GET /api/namespaces", httpNamespaceHandler(clientset))
	mux.HandleFunc("GET /api/workloads/{namespace}", httpWorkloadsHandler(clientset, metricsClient))
	mux.HandleFunc("GET /api/manifest/{namespace}/{type}/{name}", httpManifestHandler(clientset))
	mux.HandleFunc("GET /api/logs/{namespace}/{pod}", httpLogsHandler(clientset))
	mux.HandleFunc("GET /api/events/{namespace}/{type}/{name}", httpEventsHandler(clientset))
	mux.HandleFunc("GET /api/events-count/{namespace}/{type}/{name}", httpEventsCountHandler(clientset))

	wrappedMux := logRequest(mux)

	addr := fmt.Sprintf("%s:%s", bindIP, port)
	fmt.Printf("Server starting on %s...\n", addr)
	if err := http.ListenAndServe(addr, wrappedMux); err != nil {
		fmt.Printf("Error starting server: %v\n", err)
		os.Exit(1)
	}
}
