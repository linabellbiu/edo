package pipeline

import (
	"edo/internal/composeconfig"
)

func composeServiceScope(doc map[string]any, selected []string) ([]string, error) {
	services, err := composeconfig.ServiceScope(doc, selected)
	if err != nil {
		return nil, ErrInvalidComposeDelivery
	}
	return services, nil
}
