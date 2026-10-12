package controllers

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
)

func testNode(cidr string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "n", Annotations: map[string]string{SimulatedNodeAnnotation: "true"}},
		Spec:       corev1.NodeSpec{PodCIDR: cidr},
		Status: corev1.NodeStatus{
			Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "172.18.0.2"}},
		},
	}
}

func TestRunningStatus(t *testing.T) {
	t.Parallel()

	always := corev1.ContainerRestartPolicyAlways
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "p", UID: "uid"},
		Spec: corev1.PodSpec{
			InitContainers: []corev1.Container{
				{Name: "setup", Image: "busybox"},
				{Name: "sidecar", Image: "envoy", RestartPolicy: &always},
			},
			Containers: []corev1.Container{{Name: "app", Image: "nginx"}},
		},
		Status: corev1.PodStatus{
			QOSClass: corev1.PodQOSBestEffort,
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodScheduled, Status: corev1.ConditionTrue},
				{Type: corev1.PodReady, Status: corev1.ConditionFalse},
			},
		},
	}

	status := RunningStatus(pod, testNode("10.244.0.0/24"), "10.244.0.2", metav1.Now())

	if status.Phase != corev1.PodRunning || status.HostIP != "172.18.0.2" || status.PodIP != "10.244.0.2" {
		t.Errorf("status = %+v", status)
	}

	if status.QOSClass != corev1.PodQOSBestEffort {
		t.Errorf("QOS class = %q, want it kept", status.QOSClass)
	}

	conditions := map[corev1.PodConditionType]corev1.ConditionStatus{}
	for _, c := range status.Conditions {
		if _, dup := conditions[c.Type]; dup {
			t.Errorf("condition %s twice", c.Type)
		}

		conditions[c.Type] = c.Status
	}

	for _, kind := range []corev1.PodConditionType{
		corev1.PodScheduled, corev1.PodReadyToStartContainers, corev1.PodInitialized,
		corev1.ContainersReady, corev1.PodReady,
	} {
		if conditions[kind] != corev1.ConditionTrue {
			t.Errorf("condition %s = %q, want True", kind, conditions[kind])
		}
	}

	setup, sidecar := status.InitContainerStatuses[0], status.InitContainerStatuses[1]
	if setup.State.Terminated == nil || setup.State.Terminated.ExitCode != 0 || setup.Ready {
		t.Errorf("init container = %+v, want completed", setup)
	}

	if sidecar.State.Running == nil || !sidecar.Ready {
		t.Errorf("sidecar = %+v, want running and ready", sidecar)
	}

	if app := status.ContainerStatuses[0]; app.State.Running == nil || !app.Ready || app.ImageID == "" {
		t.Errorf("container = %+v, want running and ready", app)
	}
}

func TestPodIPSkipsLivePodsAndReusesFinishedOnes(t *testing.T) {
	t.Parallel()

	indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	k := &kubelet{pods: corelisters.NewPodLister(indexer)}
	node := testNode("10.244.3.0/30")

	add := func(name, ip string, phase corev1.PodPhase) {
		t.Helper()

		err := indexer.Add(&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns"},
			Spec:       corev1.PodSpec{NodeName: "n"},
			Status:     corev1.PodStatus{PodIP: ip, Phase: phase},
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	pod := &corev1.Pod{Spec: corev1.PodSpec{NodeName: "n"}}

	ip, err := k.podIP(pod, node)
	if err != nil || ip != "10.244.3.2" {
		t.Fatalf("first ip = %q, %v; want 10.244.3.2", ip, err)
	}

	add("live", "10.244.3.2", corev1.PodRunning)

	ip, err = k.podIP(pod, node)
	if err != nil || ip != "10.244.3.3" {
		t.Fatalf("second ip = %q, %v; want 10.244.3.3", ip, err)
	}

	add("other", "10.244.3.3", corev1.PodRunning)

	if _, err = k.podIP(pod, node); err == nil {
		t.Fatal("a full /30 handed out another address")
	}

	add("done", "10.244.3.3", corev1.PodSucceeded)
	if err = indexer.Delete(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "ns"}}); err != nil {
		t.Fatal(err)
	}

	ip, err = k.podIP(pod, node)
	if err != nil || ip != "10.244.3.3" {
		t.Fatalf("reused ip = %q, %v; want 10.244.3.3", ip, err)
	}

	hostNetwork := &corev1.Pod{Spec: corev1.PodSpec{NodeName: "n", HostNetwork: true}}
	if ip, _ = k.podIP(hostNetwork, node); ip != "172.18.0.2" {
		t.Errorf("host-network ip = %q, want the node's", ip)
	}
}
