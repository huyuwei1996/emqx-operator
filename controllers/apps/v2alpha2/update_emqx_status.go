package v2alpha2

import (
	"context"
	"encoding/json"
	"strings"

	emperror "emperror.dev/errors"
	appsv2alpha2 "github.com/emqx/emqx-operator/apis/apps/v2alpha2"
	innerReq "github.com/emqx/emqx-operator/internal/requester"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type updateStatus struct {
	*EMQXReconciler
}

func (u *updateStatus) reconcile(ctx context.Context, instance *appsv2alpha2.EMQX, r innerReq.RequesterInterface) subResult {
	var existedSts *appsv1.StatefulSet = &appsv1.StatefulSet{}
	var existedRs *appsv1.ReplicaSet = &appsv1.ReplicaSet{}

	stsList := &appsv1.StatefulSetList{}
	_ = u.Client.List(ctx, stsList,
		client.InNamespace(instance.Namespace),
		client.MatchingLabels(appsv2alpha2.CloneAndAddLabel(
			instance.Spec.CoreTemplate.Labels,
			appsv2alpha2.PodTemplateHashLabelKey,
			instance.Status.CoreNodesStatus.CurrentRevision,
		)),
	)
	if len(stsList.Items) > 0 {
		existedSts = stsList.Items[0].DeepCopy()
	}

	instance.Status.CoreNodesStatus.Replicas = *instance.Spec.CoreTemplate.Spec.Replicas

	if isExistReplicant(instance) {
		if instance.Status.ReplicantNodesStatus == nil {
			instance.Status.ReplicantNodesStatus = &appsv2alpha2.EMQXNodesStatus{}
		}

		rsList := &appsv1.ReplicaSetList{}
		_ = u.Client.List(ctx, rsList,
			client.InNamespace(instance.Namespace),
			client.MatchingLabels(appsv2alpha2.CloneAndAddLabel(
				instance.Spec.ReplicantTemplate.Labels,
				appsv2alpha2.PodTemplateHashLabelKey,
				instance.Status.ReplicantNodesStatus.CurrentRevision,
			)),
		)
		if len(rsList.Items) > 0 {
			existedRs = rsList.Items[0].DeepCopy()
		}

		instance.Status.ReplicantNodesStatus.Replicas = *instance.Spec.ReplicantTemplate.Spec.Replicas
	}

	if r != nil {
		if emqxNodes, err := u.getEMQXNodes(ctx, instance, r); err != nil {
			u.EventRecorder.Event(instance, corev1.EventTypeWarning, "FailedToGetNodeStatuses", err.Error())
		} else {
			instance.Status.SetNodes(emqxNodes)
		}
	}
	newEMQXStatusMachine(instance).NextStatus(existedSts, existedRs)

	if err := u.Client.Status().Update(ctx, instance); err != nil {
		return subResult{err: emperror.Wrap(err, "failed to update status")}
	}
	return subResult{}
}

func (u *updateStatus) getEMQXNodes(ctx context.Context, instance *appsv2alpha2.EMQX, r innerReq.RequesterInterface) ([]appsv2alpha2.EMQXNode, error) {
	emqxNodes, err := getEMQXNodesByAPI(r)
	if err != nil {
		return nil, emperror.Wrap(err, "failed to get node statues by API")
	}

	list := &corev1.PodList{}
	_ = u.Client.List(ctx, list,
		client.InNamespace(instance.Namespace),
		client.MatchingLabels(instance.Labels),
	)
	for _, p := range list.Items {
		pod := p.DeepCopy()
		for _, node := range emqxNodes {
			host := strings.Split(node.Node[strings.Index(node.Node, "@")+1:], ":")[0]
			if (node.Role == "core" && strings.HasPrefix(host, pod.Name)) ||
				(node.Role == "replicant" && host == pod.Status.PodIP) {
				node.PodUID = pod.UID
				controllerRef := metav1.GetControllerOf(pod)
				if controllerRef == nil {
					continue
				}
				node.ControllerUID = controllerRef.UID
			}
		}
	}
	return emqxNodes, nil
}

func getEMQXNodesByAPI(r innerReq.RequesterInterface) ([]appsv2alpha2.EMQXNode, error) {
	resp, body, err := r.Request("GET", "api/v5/nodes", nil)
	if err != nil {
		return nil, emperror.Wrap(err, "failed to get API api/v5/nodes")
	}
	if resp.StatusCode != 200 {
		return nil, emperror.Errorf("failed to get API %s, status : %s, body: %s", "api/v5/nodes", resp.Status, body)
	}

	nodeStatuses := []appsv2alpha2.EMQXNode{}
	if err := json.Unmarshal(body, &nodeStatuses); err != nil {
		return nil, emperror.Wrap(err, "failed to unmarshal node statuses")
	}
	return nodeStatuses, nil
}