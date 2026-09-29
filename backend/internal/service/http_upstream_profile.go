package service

import (
	"context"
	"net/url"
)

// HTTPUpstreamProfile marks HTTP upstream requests that need provider-specific
// transport policy.
type HTTPUpstreamProfile string

const (
	HTTPUpstreamProfileDefault    HTTPUpstreamProfile = ""
	HTTPUpstreamProfileOpenAI     HTTPUpstreamProfile = "openai"
	HTTPUpstreamProfileGrok       HTTPUpstreamProfile = "grok"
	HTTPUpstreamProfileLongStream HTTPUpstreamProfile = "long_stream"
)

type httpUpstreamProfileContextKey struct{}
type httpRedirectValidatorContextKey struct{}
type httpUpstreamDisableRedirectsContextKey struct{}
type httpUpstreamPublicHostsOnlyContextKey struct{}

type HTTPRedirectValidator func(*url.URL) error

// WithHTTPUpstreamProfile injects an upstream transport profile into ctx.
func WithHTTPUpstreamProfile(ctx context.Context, profile HTTPUpstreamProfile) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if profile == HTTPUpstreamProfileDefault {
		return ctx
	}
	return context.WithValue(ctx, httpUpstreamProfileContextKey{}, profile)
}

// HTTPUpstreamProfileFromContext resolves the upstream transport profile from ctx.
func HTTPUpstreamProfileFromContext(ctx context.Context) HTTPUpstreamProfile {
	if ctx == nil {
		return HTTPUpstreamProfileDefault
	}
	profile, ok := ctx.Value(httpUpstreamProfileContextKey{}).(HTTPUpstreamProfile)
	if !ok {
		return HTTPUpstreamProfileDefault
	}
	switch profile {
	case HTTPUpstreamProfileOpenAI, HTTPUpstreamProfileGrok, HTTPUpstreamProfileLongStream:
		return profile
	default:
		return HTTPUpstreamProfileDefault
	}
}

func WithHTTPRedirectValidator(ctx context.Context, validator HTTPRedirectValidator) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if validator == nil {
		return ctx
	}
	return context.WithValue(ctx, httpRedirectValidatorContextKey{}, validator)
}

func ValidateHTTPRedirect(ctx context.Context, target *url.URL) error {
	if ctx == nil {
		return nil
	}
	validator, _ := ctx.Value(httpRedirectValidatorContextKey{}).(HTTPRedirectValidator)
	if validator == nil {
		return nil
	}
	return validator(target)
}

// WithHTTPUpstreamRedirectsDisabled prevents credential-bearing probes from
// following redirects through the shared upstream client.
func WithHTTPUpstreamRedirectsDisabled(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, httpUpstreamDisableRedirectsContextKey{}, true)
}

func HTTPUpstreamRedirectsDisabled(ctx context.Context) bool {
	return ctx != nil && ctx.Value(httpUpstreamDisableRedirectsContextKey{}) == true
}

// WithHTTPUpstreamPublicHostsOnly marks a request whose destination, and every
// redirect hop after it, must resolve to a public address. The shared upstream
// client enforces it regardless of the security.url_allowlist configuration;
// use it for fetches whose URL comes from an untrusted upstream response.
func WithHTTPUpstreamPublicHostsOnly(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, httpUpstreamPublicHostsOnlyContextKey{}, true)
}

func HTTPUpstreamPublicHostsOnly(ctx context.Context) bool {
	return ctx != nil && ctx.Value(httpUpstreamPublicHostsOnlyContextKey{}) == true
}
