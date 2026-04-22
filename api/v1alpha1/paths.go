package v1alpha1

import "fmt"

// PostPath returns the first path that defines a POST operation in the
// embedded OpenAPI specification. Used by registration to derive the
// endpoint suffix that SPM will POST to.
func PostPath() (string, error) {
	spec, err := GetSwagger()
	if err != nil {
		return "", fmt.Errorf("loading OpenAPI spec: %w", err)
	}
	for _, p := range spec.Paths.InMatchingOrder() {
		if spec.Paths.Value(p).Post != nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("no POST path found in OpenAPI spec")
}

// PostPaths returns all paths that define a POST operation, keyed by the
// operation's first tag. This lets callers look up service-type-specific
// collection paths (e.g. "cluster" -> "/clusters", "vm" -> "/vms").
func PostPaths() (map[string]string, error) {
	spec, err := GetSwagger()
	if err != nil {
		return nil, fmt.Errorf("loading OpenAPI spec: %w", err)
	}
	result := make(map[string]string)
	for _, p := range spec.Paths.InMatchingOrder() {
		item := spec.Paths.Value(p)
		if item.Post != nil && len(item.Post.Tags) > 0 {
			result[item.Post.Tags[0]] = p
		}
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("no POST paths found in OpenAPI spec")
	}
	return result, nil
}
