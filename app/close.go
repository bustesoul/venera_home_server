package app

import "errors"

func (a *App) Close() error {
	if a == nil {
		return nil
	}
	a.stopEHBotService()
	var errs []error
	if a.metadataStore != nil {
		if err := a.metadataStore.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	for _, backend := range a.backends {
		if closer, ok := backend.(interface{ Close() error }); ok {
			if err := closer.Close(); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}
