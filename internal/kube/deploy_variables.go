package kube

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"sort"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type releaseVariablesKey struct{}

func WithReleaseVariables(ctx context.Context, values map[string]string) context.Context {
	return context.WithValue(ctx, releaseVariablesKey{}, maps.Clone(values))
}
func variableSecretName(id string) string {
	hash := sha256.Sum256([]byte(id))
	return "edo-vars-" + hex.EncodeToString(hash[:12])
}
func applyReleaseVariables(ctx context.Context, client kubernetes.Interface, current *appsv1.Deployment, index int, deploymentID string) error {
	values, ok := ctx.Value(releaseVariablesKey{}).(map[string]string)
	if !ok {
		return nil
	}
	name := variableSecretName(deploymentID)
	data := map[string][]byte{}
	total := 0
	keys := make([]string, 0, len(values))
	for key, value := range values {
		total += len(value)
		if total > 256*1024 {
			return ErrDeploymentConfigInvalid
		}
		keys = append(keys, key)
		data[key] = []byte(value)
	}
	sort.Strings(keys)
	if len(keys) > 0 {
		immutable := true
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: current.Namespace, Labels: map[string]string{"edo.io/deployment-id": deploymentID}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: current.Name, UID: current.UID}}}, Immutable: &immutable, Type: corev1.SecretTypeOpaque, Data: data}
		_, err := client.CoreV1().Secrets(current.Namespace).Create(ctx, secret, metav1.CreateOptions{FieldManager: "edo"})
		if apierrors.IsAlreadyExists(err) {
			existing, e := client.CoreV1().Secrets(current.Namespace).Get(ctx, name, metav1.GetOptions{})
			if e != nil {
				return e
			}
			if existing.Immutable == nil || !*existing.Immutable || existing.Labels["edo.io/deployment-id"] != deploymentID || len(existing.OwnerReferences) != 1 || existing.OwnerReferences[0].UID != current.UID || !maps.EqualFunc(existing.Data, data, func(a, b []byte) bool { return string(a) == string(b) }) {
				return ErrDeploymentTargetChanged
			}
		} else if err != nil {
			return err
		}
	}
	old := variableSecretName(current.Spec.Template.Annotations["edo.io/deployment-id"])
	env := []corev1.EnvVar{}
	for _, item := range current.Spec.Template.Spec.Containers[index].Env {
		if _, replaced := values[item.Name]; replaced {
			continue
		}
		if item.ValueFrom != nil && item.ValueFrom.SecretKeyRef != nil && item.ValueFrom.SecretKeyRef.Name == old {
			continue
		}
		env = append(env, item)
	}
	for _, key := range keys {
		if strings.ContainsAny(key, "\x00\n\r") {
			return ErrDeploymentConfigInvalid
		}
		env = append(env, corev1.EnvVar{Name: key, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: name}, Key: key}}})
	}
	current.Spec.Template.Spec.Containers[index].Env = env
	return nil
}
