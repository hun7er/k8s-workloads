package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

type NamespaceInfo struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

type DeploymentInfo struct {
	Name               string    `json:"name"`
	Images             []string  `json:"images"`
	ReadyReplicas      int32     `json:"readyReplicas"`
	HelmControllerTime time.Time `json:"helmControllerTime"`
	LastUpdateTime     time.Time `json:"lastUpdateTime"`
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
	Name               string    `json:"name"`
	Images             []string  `json:"images"`
	ReadyReplicas      int32     `json:"readyReplicas"`
	HelmControllerTime time.Time `json:"helmControllerTime"`
}

type PodInfo struct {
	Name      string    `json:"name"`
	Images    []string  `json:"images"`
	Status    string    `json:"status"`
	StartTime time.Time `json:"startTime"`
	Owner     string    `json:"owner"`
	OwnerKind string    `json:"ownerKind"`
}

type Workloads struct {
	Deployments  []DeploymentInfo  `json:"deployments"`
	ReplicaSets  []ReplicaSetInfo  `json:"replica_sets"`
	StatefulSets []StatefulSetInfo `json:"stateful_sets"`
	Pods         []PodInfo         `json:"pods"`
}

func main() {
	config, err := getConfig()
	if err != nil {
		panic(err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(err)
	}

	http.HandleFunc("/", serveStaticFiles)
	http.HandleFunc("/api/namespaces", httpNamespaceHandler(clientset))
	http.HandleFunc("/api/workloads/", httpWorkloadsHandler(clientset))

	fmt.Println("Server starting on port 32443...")
	http.ListenAndServe(":32443", nil)
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

func httpWorkloadsHandler(clientset *kubernetes.Clientset) http.HandlerFunc {
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

		podInfo, err := getPodInfo(clientset, namespace)
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
		helmControllerTime := getManagedFieldsTimeForHelmController(deployment.ObjectMeta.ManagedFields)
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
			Name:               deployment.Name,
			Images:             images,
			ReadyReplicas:      deployment.Status.ReadyReplicas,
			HelmControllerTime: helmControllerTime,
			LastUpdateTime:     lastUpdateTime,
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
		helmControllerTime := getManagedFieldsTimeForHelmController(statefulset.ObjectMeta.ManagedFields)

		images := make([]string, len(statefulset.Spec.Template.Spec.Containers))
		for i, container := range statefulset.Spec.Template.Spec.Containers {
			images[i] = container.Image
		}

		statefulSetInfos = append(statefulSetInfos, StatefulSetInfo{
			Name:               statefulset.Name,
			Images:             images,
			ReadyReplicas:      statefulset.Status.ReadyReplicas,
			HelmControllerTime: helmControllerTime,
		})
	}
	return statefulSetInfos, nil
}

func getPodInfo(clientset *kubernetes.Clientset, namespace string) ([]PodInfo, error) {
	pods, err := clientset.CoreV1().Pods(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, err
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
		for _, container := range pod.Spec.Containers {
			images = append(images, container.Image)
		}

		if ownerKind == "StatefulSet" || ownerKind == "ReplicaSet" {
			podInfoList = append(podInfoList, PodInfo{
				Name:      pod.Name,
				Status:    string(pod.Status.Phase),
				StartTime: pod.Status.StartTime.Time,
				Owner:     owner,
				OwnerKind: ownerKind,
				Images:    images,
			})
		}
	}
	return podInfoList, nil
}

func getTimesForWorkload(meta metav1.ObjectMeta, conditions []appsv1.DeploymentCondition) (helmControllerTime, lastUpdateTime time.Time) {
	helmControllerTime = getManagedFieldsTimeForHelmController(meta.ManagedFields)
	for _, condition := range conditions {
		if condition.Type == appsv1.DeploymentProgressing {
			lastUpdateTime = condition.LastUpdateTime.Time
			break
		}
	}
	return
}

func getManagedFieldsTimeForHelmController(managedFields []metav1.ManagedFieldsEntry) time.Time {
	for _, field := range managedFields {
		if field.Manager == "helm-controller" {
			return field.Time.Time
		}
	}
	return time.Time{}
}
