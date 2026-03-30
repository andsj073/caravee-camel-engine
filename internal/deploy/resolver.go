package deploy

import (
	"bufio"
	"log/slog"
	"os"
	"regexp"
	"strings"
)

// ParseProperties parses a Java .properties format string into a flat map.
// Handles comments (#), blank lines, and key=value pairs.
func ParseProperties(content string) map[string]string {
	result := make(map[string]string)
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			result[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}
	return result
}

// ResolveProfile applies profile-key filtering.
// Returns base properties overridden by %profileKey. prefixed ones.
// If profileKey is empty, returns only non-prefixed properties.
func ResolveProfile(raw map[string]string, profileKey string) map[string]string {
	result := make(map[string]string)
	for k, v := range raw {
		if !strings.HasPrefix(k, "%") {
			result[k] = v
		}
	}
	if profileKey != "" {
		prefix := "%" + profileKey + "."
		for k, v := range raw {
			if strings.HasPrefix(k, prefix) {
				result[strings.TrimPrefix(k, prefix)] = v
			}
		}
	}
	return result
}


var envRefRe = regexp.MustCompile(`\$\{([^}]+)\}`)

// ResolveSecretRefs replaces ${ENV_VAR} in yamlContent using secrets map first,
// then falls back to os.Getenv (local secrets.env already merged into secrets by caller).
// Returns resolved YAML and list of unresolved secret refs.
func ResolveSecretRefs(yamlContent string, secrets map[string]string) (string, []string) {
	var unresolved []string
	seen := make(map[string]bool)
	resolved := envRefRe.ReplaceAllStringFunc(yamlContent, func(match string) string {
		key := match[2 : len(match)-1]
		if v, ok := secrets[key]; ok {
			return v
		}
		// Fallback: OS env (self-hosted only, not cloud-hosted)
		if v := os.Getenv(key); v != "" {
			return v
		}
		if !seen[key] {
			seen[key] = true
			unresolved = append(unresolved, key)
			slog.Warn("Unresolved secret ref in YAML", "key", key)
		}
		return match
	})
	return resolved, unresolved
}

var placeholderRe = regexp.MustCompile(`\{\{([^}]+)\}\}`)

// ResolvePlaceholders replaces {{property.name}} in yamlContent using props map.
// Returns resolved YAML and list of unresolved placeholder names.
func ResolvePlaceholders(yamlContent string, props map[string]string) (string, []string) {
	var unresolved []string
	seen := make(map[string]bool)
	resolved := placeholderRe.ReplaceAllStringFunc(yamlContent, func(match string) string {
		key := strings.TrimSpace(match[2 : len(match)-2])
		if v, ok := props[key]; ok {
			return v
		}
		if !seen[key] {
			seen[key] = true
			unresolved = append(unresolved, key)
			slog.Warn("Unresolved placeholder in YAML", "key", key)
		}
		return match
	})
	return resolved, unresolved
}
