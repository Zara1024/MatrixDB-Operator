package controller

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	matrixdbv1alpha1 "github.com/matrixdb-io/matrixdb-operator/api/v1alpha1"
	"github.com/matrixdb-io/matrixdb-operator/pkg/constants"
)

type ClusterCredentials struct {
	SecretName      string
	RootPassword    string
	ReplicaPassword string
}

func (r *MatrixDBClusterReconciler) reconcileSecret(ctx context.Context, cluster *matrixdbv1alpha1.MatrixDBCluster) (*ClusterCredentials, error) {
	secretName := cluster.Spec.SecretName
	if secretName == "" {
		secretName = fmt.Sprintf("%s-secret", cluster.Name)
	}

	secret := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Namespace: cluster.Namespace, Name: secretName}, secret)
	if err == nil {
		// Secret already exists, extract credentials
		rootPass := string(secret.Data[constants.SecretRootPasswordKey])
		replicaPass := string(secret.Data[constants.SecretReplicaPasswordKey])
		return &ClusterCredentials{
			SecretName:      secretName,
			RootPassword:    rootPass,
			ReplicaPassword: replicaPass,
		}, nil
	}

	if !errors.IsNotFound(err) {
		return nil, fmt.Errorf("failed to get secret %s: %w", secretName, err)
	}

	// Secret does not exist, generate high-entropy passwords
	rootPass, err := generateRandomPassword(20)
	if err != nil {
		return nil, fmt.Errorf("failed generating root password: %w", err)
	}
	replicaPass, err := generateRandomPassword(20)
	if err != nil {
		return nil, fmt.Errorf("failed generating replica password: %w", err)
	}

	newSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: cluster.Namespace,
			Labels: map[string]string{
				constants.LabelAppName:      "matrixdb",
				constants.LabelAppInstance: cluster.Name,
				constants.LabelClusterName:  cluster.Name,
				constants.LabelAppManagedBy: "matrixdb-operator",
			},
		},
		Type: corev1.SecretTypeOpaque,
		StringData: map[string]string{
			constants.SecretRootPasswordKey:    rootPass,
			constants.SecretReplicaPasswordKey: replicaPass,
		},
	}

	if err := controllerutil.SetControllerReference(cluster, newSecret, r.Scheme); err != nil {
		return nil, fmt.Errorf("failed setting controller reference on secret: %w", err)
	}

	if err := r.Create(ctx, newSecret); err != nil {
		return nil, fmt.Errorf("failed to create secret %s: %w", secretName, err)
	}

	r.Log.Info("Successfully generated cluster secret", "secret", secretName)
	return &ClusterCredentials{
		SecretName:      secretName,
		RootPassword:    rootPass,
		ReplicaPassword: replicaPass,
	}, nil
}

func generateRandomPassword(length int) (string, error) {
	bytes := make([]byte, length)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes)[:length], nil
}
