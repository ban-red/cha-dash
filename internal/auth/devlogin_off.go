//go:build !devlogin

package auth

import "net/http"

// devLoginBuilt is true only in -tags devlogin builds; see devlogin.go.
const devLoginBuilt = false

func (s *Service) registerDevLogin(*http.ServeMux) {}
