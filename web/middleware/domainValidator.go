// Package middleware provides HTTP middleware functions for the 3AX-UI web panel,
// including domain validation and URL redirection utilities.
package middleware

import (
	"net"
	"net/http"

	"github.com/gin-gonic/gin"
)

// stripBrackets removes surrounding square brackets from an IPv6 literal (e.g. "[::1]" → "::1").
func stripBrackets(h string) string {
	if len(h) > 1 && h[0] == '[' && h[len(h)-1] == ']' {
		return h[1 : len(h)-1]
	}
	return h
}

// DomainValidatorMiddleware returns a Gin middleware that validates the request domain.
// It extracts the host from the request, strips any port number, and compares it
// against the configured domain. Requests from unauthorized domains are rejected
// with HTTP 403 Forbidden status.
func DomainValidatorMiddleware(domain string) gin.HandlerFunc {
	// Normalise the configured domain once: strip port if present, strip IPv6 brackets.
	normalised := domain
	if h, _, err := net.SplitHostPort(domain); err == nil {
		normalised = h
	}
	normalised = stripBrackets(normalised)

	return func(c *gin.Context) {
		host := c.Request.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		host = stripBrackets(host)

		if host != normalised {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}

		c.Next()
	}
}
