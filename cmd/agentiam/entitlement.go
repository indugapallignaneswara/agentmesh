package main

import (
	"fmt"
	"strings"
)

// entitlementFlag collects repeatable --entitlement key=value pairs into a map.
type entitlementFlag struct{ m map[string]string }

func (e *entitlementFlag) String() string { return fmt.Sprintf("%v", e.m) }

func (e *entitlementFlag) Set(v string) error {
	k, val, ok := strings.Cut(v, "=")
	if !ok || k == "" {
		return fmt.Errorf("entitlement must be key=value, got %q", v)
	}
	if e.m == nil {
		e.m = map[string]string{}
	}
	e.m[k] = val
	return nil
}
