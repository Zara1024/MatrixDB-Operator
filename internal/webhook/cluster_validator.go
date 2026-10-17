package webhook

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"

	matrixdbv1alpha1 "github.com/matrixdb-io/matrixdb-operator/api/v1alpha1"
)

var (
	universalDeserializer = serializer.NewCodecFactory(runtime.NewScheme()).UniversalDeserializer()
)

// ClusterValidator validates MatrixDBCluster creation and updates at API Server level.
type ClusterValidator struct{}

func (v *ClusterValidator) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body []byte
	if r.Body != nil {
		if data, err := io.ReadAll(r.Body); err == nil {
			body = data
		}
	}
	if len(body) == 0 {
		http.Error(w, "empty body", http.StatusBadRequest)
		return
	}

	admissionReview := admissionv1.AdmissionReview{}
	if _, _, err := universalDeserializer.Decode(body, nil, &admissionReview); err != nil {
		http.Error(w, fmt.Sprintf("could not decode admission review: %v", err), http.StatusBadRequest)
		return
	}

	response := admissionv1.AdmissionResponse{
		UID:     admissionReview.Request.UID,
		Allowed: true,
	}

	cluster := matrixdbv1alpha1.MatrixDBCluster{}
	if err := json.Unmarshal(admissionReview.Request.Object.Raw, &cluster); err != nil {
		response.Allowed = false
		response.Result = &metav1.Status{
			Message: fmt.Sprintf("could not unmarshal MatrixDBCluster: %v", err),
			Code:    http.StatusBadRequest,
		}
	} else {
		// Rule 1: Replicas must be at least 1
		if cluster.Spec.Replicas < 1 {
			response.Allowed = false
			response.Result = &metav1.Status{
				Message: fmt.Sprintf("admission webhook denied: spec.replicas must be at least 1, got %d", cluster.Spec.Replicas),
				Code:    http.StatusForbidden,
			}
		}

		// Rule 2: binlog_format STATEMENT is forbidden
		if cluster.Spec.MysqlConfig != nil {
			if format, ok := cluster.Spec.MysqlConfig["binlog_format"]; ok && format == "STATEMENT" {
				response.Allowed = false
				response.Result = &metav1.Status{
					Message: "admission webhook denied: binlog_format=STATEMENT is forbidden (MatrixDB requires ROW or MIXED for lossless semi-sync replication)",
					Code:    http.StatusForbidden,
				}
			}
		}

		// Rule 3: Storage size validation
		if cluster.Spec.Storage.Size == "" {
			response.Allowed = false
			response.Result = &metav1.Status{
				Message: "admission webhook denied: spec.storage.size cannot be empty",
				Code:    http.StatusForbidden,
			}
		}
	}

	admissionReview.Response = &response
	resp, err := json.Marshal(admissionReview)
	if err != nil {
		http.Error(w, fmt.Sprintf("could not encode response: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(resp)
}
