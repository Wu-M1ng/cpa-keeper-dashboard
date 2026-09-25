package main

import (
	"encoding/json"
	"html"
	"net/url"
	"regexp"
	"strings"
	"unicode"
)

const (
	redactedValue = "***"
	// Bound the work done in the usage callback. Oversized input is hidden
	// instead of cutting through an identifier before it can be recognized.
	maxPrivacyInputBytes = 16 * 1024
	maxPrivacyDepth      = 32
	credentialNames      = `authorization|proxy[-_]?authorization|(?:x[-_]?)?api[-_]?key|x[-_]?goog[-_]?api[-_]?key|management[-_]?key|access[-_]?token|refresh[-_]?token|id[-_]?token|auth[-_]?token|client[-_]?secret|secret[-_]?key|password|passwd|pwd|token|secret|key|cookie|set[-_]?cookie`
)

var (
	credentialNamePattern  = regexp.MustCompile(`(?i)^(?:` + credentialNames + `)$`)
	credentialValuePattern = regexp.MustCompile(`(?i)(\b(?:` + credentialNames + `)\b["']?\s*[:=]\s*)("(?:\\.|[^"\\])*(?:"|$)|'(?:\\.|[^'\\])*(?:'|$)|(?:bearer|basic)\s+[^\s"'<>;,}\]]+|[^\s"'<>;,}\]]+)`)
	authorizationPattern   = regexp.MustCompile(`(?i)(\b(?:bearer|basic)\s+)[^\s"'<>;,}\]]+`)
	cookieHeaderPattern    = regexp.MustCompile(`(?im)(\b(?:set-cookie|cookie)\s*:\s*)[^\r\n]*`)
	emailPattern           = regexp.MustCompile(`[\p{L}\p{N}._%+\-]+@[\p{L}\p{N}\-]+(?:\.[\p{L}\p{N}\-]+)+`)
	apiSecretPattern       = regexp.MustCompile(`(?i)\bsk-[a-z0-9][a-z0-9_\-]*`)
	jwtPattern             = regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+`)
	diagnosticURLPattern   = regexp.MustCompile(`(?i)\bhttps?://[^\s<>"']+`)
	percentEscapePattern   = regexp.MustCompile(`(?:%[0-9a-fA-F]{2})+`)
	jsonEscapePattern      = regexp.MustCompile(`(?:\\(?:u[0-9a-fA-F]{4}|["\\/bfnrt]))+`)
	diagnosticLineBreaks   = strings.NewReplacer("\r", " ", "\n", " ", "\t", " ")
)

// normalizePrivacyEncoding exposes identifiers hidden by ordinary transport
// encoding. PathUnescape preserves '+' in email addresses. If encoding is
// nested beyond the bounded number of passes, discard the value entirely.
func normalizePrivacyEncoding(value string) string {
	for pass := 0; pass < 8; pass++ {
		next := html.UnescapeString(value)
		next = percentEscapePattern.ReplaceAllStringFunc(next, func(encoded string) string {
			decoded, _ := url.PathUnescape(encoded)
			return decoded
		})
		next = jsonEscapePattern.ReplaceAllStringFunc(next, func(encoded string) string {
			var decoded string
			if json.Unmarshal([]byte(`"`+encoded+`"`), &decoded) == nil {
				return decoded
			}
			return encoded
		})
		if next == value {
			return value
		}
		value = next
	}
	return redactedValue
}

func redactSensitiveValues(value string) string {
	value = credentialValuePattern.ReplaceAllStringFunc(value, func(match string) string {
		parts := credentialValuePattern.FindStringSubmatch(match)
		replacement := redactedValue
		if strings.HasPrefix(parts[2], `"`) {
			replacement = `"` + redactedValue + `"`
		} else if strings.HasPrefix(parts[2], "'") {
			replacement = "'" + redactedValue + "'"
		}
		return parts[1] + replacement
	})
	value = authorizationPattern.ReplaceAllString(value, "${1}"+redactedValue)
	value = apiSecretPattern.ReplaceAllString(value, redactedValue)
	value = jwtPattern.ReplaceAllString(value, redactedValue)
	return emailPattern.ReplaceAllStringFunc(value, maskProviderCredential)
}

func redactDiagnostic(value string, depth int) string {
	if depth > maxPrivacyDepth {
		return redactedValue
	}
	// Decode valid JSON structurally so escaped property names are recognized
	// and useful error messages, numeric codes and JSON syntax survive.
	if json.Valid([]byte(value)) {
		decoder := json.NewDecoder(strings.NewReader(value))
		decoder.UseNumber()
		var decoded any
		if decoder.Decode(&decoded) == nil {
			cleaned := redactDiagnosticJSON(decoded, depth+1)
			if encoded, err := json.Marshal(cleaned); err == nil {
				return string(encoded)
			}
		}
	}
	value = normalizePrivacyEncoding(value)
	value = normalizePrivacyControls(value)
	value = diagnosticURLPattern.ReplaceAllStringFunc(value, func(match string) string {
		address := strings.TrimRight(match, ".,;)}")
		return sanitizeEndpoint(address) + match[len(address):]
	})
	// Process complete cookie header lines before removing control characters.
	value = cookieHeaderPattern.ReplaceAllString(value, "[redacted cookie] ")
	value = diagnosticLineBreaks.Replace(value)
	return redactSensitiveValues(value)
}

func redactDiagnosticJSON(value any, depth int) any {
	if depth > maxPrivacyDepth {
		return redactedValue
	}
	switch typed := value.(type) {
	case map[string]any:
		cleaned := make(map[string]any, len(typed))
		for key, child := range typed {
			key = normalizePrivacyControls(normalizePrivacyEncoding(key))
			if credentialNamePattern.MatchString(key) {
				cleaned[key] = redactedValue
			} else {
				cleaned[redactSensitiveValues(key)] = redactDiagnosticJSON(child, depth+1)
			}
		}
		return cleaned
	case []any:
		for i := range typed {
			typed[i] = redactDiagnosticJSON(typed[i], depth+1)
		}
		return typed
	case string:
		return strings.TrimSpace(cleanControlChars(redactDiagnostic(typed, depth+1), maxPrivacyInputBytes))
	default:
		return value
	}
}

func sanitizeFailure(value string) string {
	if value == "" {
		return ""
	}
	if len(value) > maxPrivacyInputBytes {
		return "[redacted: oversized diagnostic]"
	}
	value = redactDiagnostic(strings.TrimSpace(value), 0)
	// Redact before truncating: a cut through an email/token must not leave
	// a raw prefix that no longer matches a complete identifier.
	return strings.Clone(strings.TrimSpace(cleanControlChars(value, 512)))
}

// Keep line boundaries until header redaction finishes, but remove invisible
// characters before matching so a split token cannot be reconstructed later.
func normalizePrivacyControls(value string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, value)
}

func sanitizeEndpoint(value string) string {
	if len(value) > maxPrivacyInputBytes {
		return redactedValue
	}
	value = strings.TrimSpace(normalizePrivacyControls(normalizePrivacyEncoding(value)))
	if value == "" {
		return ""
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Opaque != "" ||
		(parsed.Scheme != "" && !strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https")) ||
		(parsed.Scheme != "" && parsed.Host == "") {
		// Never echo an unparseable URL: it can still contain userinfo, query
		// credentials or encoded sensitive path segments.
		return redactedValue
	}
	segments := strings.Split(parsed.Path, "/")
	hideNext := false
	for i, segment := range segments {
		if hideNext && segment != "" {
			segments[i] = redactedValue
			hideNext = false
			continue
		}
		segments[i] = redactSensitiveValues(segment)
		if segment != "" {
			hideNext = credentialNamePattern.MatchString(segment) || strings.EqualFold(segment, "bearer") || strings.EqualFold(segment, "basic")
		}
	}
	result := strings.Join(segments, "/")
	if parsed.Host != "" {
		scheme := parsed.Scheme
		if scheme == "" {
			scheme = "https"
		}
		result = scheme + "://" + redactSensitiveValues(parsed.Host) + result
	}
	return strings.Clone(cleanControlChars(result, 256))
}

// Asterisks alone do not establish that a value was redacted by this plugin.
func isMaskedProviderCredential(value string) bool {
	parts := strings.Split(value, redactedValue)
	if len(parts) != 2 || strings.ContainsAny(parts[0]+parts[1], "*\r\n\t") {
		return false
	}
	left, right := len([]rune(parts[0])), len([]rune(parts[1]))
	return (left == 3 && right == 2) || (left == 1 && right <= 1)
}
