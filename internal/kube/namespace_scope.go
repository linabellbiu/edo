package kube

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const scopedContinuationPrefix = "edo-rbac-v1:"

type scopedContinuation struct {
	Identity string `json:"identity"`
	Resource string `json:"resource"`
	Selector string `json:"selector"`
	Index    int    `json:"index"`
	Native   string `json:"native,omitempty"`
}

func scopedSelector(options metav1.ListOptions) string {
	encoded, _ := json.Marshal([]string{options.LabelSelector, options.FieldSelector, options.ResourceVersion, string(options.ResourceVersionMatch)})
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func scopedCursor(q *clusterQueries, resource string, options metav1.ListOptions, index int, native string) string {
	encoded, _ := json.Marshal(scopedContinuation{Identity: hex.EncodeToString(q.version[:]), Resource: resource, Selector: scopedSelector(options), Index: index, Native: native})
	return scopedContinuationPrefix + base64.RawURLEncoding.EncodeToString(encoded)
}

func parseScopedCursor(q *clusterQueries, resource string, options metav1.ListOptions, size int) (scopedContinuation, error) {
	if options.Continue == "" {
		return scopedContinuation{}, nil
	}
	if !strings.HasPrefix(options.Continue, scopedContinuationPrefix) || len(options.Continue) > 16384 {
		return scopedContinuation{}, ErrInvalidResource
	}
	encoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(options.Continue, scopedContinuationPrefix))
	var cursor scopedContinuation
	if err != nil || json.Unmarshal(encoded, &cursor) != nil || cursor.Identity != hex.EncodeToString(q.version[:]) || cursor.Resource != resource || cursor.Selector != scopedSelector(options) || cursor.Index < 0 || cursor.Index >= size || len(cursor.Native) > 8192 {
		return cursor, ErrInvalidResource
	}
	return cursor, nil
}

// Namespace-wide views are a bounded union of explicitly authorized namespace
// collections. Each underlying request retains its native continuation token;
// the envelope binds that token to the identity, policy, resource and filters.
func (s *Service) listScopedResourcePage(ctx context.Context, q *clusterQueries, def ResourceDefinition, options metav1.ListOptions, cacheLimit int64) (*unstructured.UnstructuredList, error) {
	namespaces := q.authorization.readNamespaces(def)
	if len(namespaces) == 0 {
		return nil, ErrRBACForbidden
	}
	cursor, err := parseScopedCursor(q, def.Resource, options, len(namespaces))
	if err != nil {
		return nil, err
	}
	limit := options.Limit
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	result := &unstructured.UnstructuredList{Items: []unstructured.Unstructured{}}
	for cursor.Index < len(namespaces) && int64(len(result.Items)) < limit {
		native := options
		native.Continue = cursor.Native
		native.Limit = limit - int64(len(result.Items))
		pageLimit := min(cacheLimit, native.Limit)
		page, err := s.listResourcePageForQuery(ctx, q, def, namespaces[cursor.Index], native, pageLimit)
		if err != nil {
			return nil, err
		}
		if int64(len(page.Items)) > native.Limit {
			return nil, ErrClusterUnreachable
		}
		result.Items = append(result.Items, page.Items...)
		cursor.Native = page.GetContinue()
		if cursor.Native == "" {
			cursor.Index++
		} else {
			break
		}
	}
	if cursor.Index < len(namespaces) {
		result.SetContinue(scopedCursor(q, def.Resource, options, cursor.Index, cursor.Native))
	}
	return result, nil
}

func (s *Service) listScopedNamespaceObjects(ctx context.Context, q *clusterQueries, options metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	if options.LabelSelector != "" || options.FieldSelector != "" {
		return nil, ErrRBACForbidden
	}
	namespaces := append([]string(nil), q.authorization.Namespaces...)
	slices.Sort(namespaces)
	namespaces = slices.Compact(namespaces)
	result := &unstructured.UnstructuredList{Items: []unstructured.Unstructured{}}
	if len(namespaces) == 0 {
		return result, nil
	}
	cursor, err := parseScopedCursor(q, "namespaces", options, len(namespaces))
	if err != nil {
		return nil, err
	}
	limit := options.Limit
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	definition, _ := ResourceType("namespaces")
	for cursor.Index < len(namespaces) && int64(len(result.Items)) < limit {
		object, err := q.dynamic.Resource(definition.gvr()).Get(ctx, namespaces[cursor.Index], metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		result.Items = append(result.Items, *object)
		cursor.Index++
	}
	if cursor.Index < len(namespaces) {
		result.SetContinue(scopedCursor(q, "namespaces", options, cursor.Index, ""))
	}
	return result, nil
}
