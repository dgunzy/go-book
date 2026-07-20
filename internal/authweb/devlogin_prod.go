//go:build !dev

package authweb

// registerDevRoutes is a no-op in production builds. The password-free
// dev-login exists only in binaries compiled with the `dev` build tag, so
// the shipped image never contains it.
func (h *Handler) registerDevRoutes() {}
