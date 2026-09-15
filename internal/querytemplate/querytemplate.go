// Package querytemplate resolves configured startup query text.
package querytemplate

import (
	"bytes"
	"errors"
	"fmt"
	"text/template"
	"time"

	"github.com/bevicted/lognav/internal/config"
)

// Content is the resolved startup query and active snippet set. It never
// aliases or changes the configured source templates.
type Content struct {
	DefaultQuery string
	Snippets     []config.Snippet
}

// Resolve executes the configured startup query and active snippets using now.
// It resolves only snippet text, preserving descriptions and configured order.
func Resolve(cfg *config.Config, now time.Time) (Content, error) {
	if cfg == nil {
		return Content{}, errors.New("resolve query templates: nil config")
	}

	query, err := resolveText("icl.defaultQuery", cfg.ICL.DefaultQuery, now)
	if err != nil {
		return Content{}, err
	}

	snippets := make([]config.Snippet, 0, len(cfg.Core.ExtraSnippets)+len(cfg.Core.DefaultSnippets))
	for index, snippet := range cfg.Core.ExtraSnippets {
		resolved, err := resolveText(fmt.Sprintf("core.extraSnippets[%d].snippet", index), snippet.Snippet, now)
		if err != nil {
			return Content{}, err
		}
		snippet.Snippet = resolved
		snippets = append(snippets, snippet)
	}
	if cfg.Core.IncludeDefaultSnippets {
		for index, snippet := range cfg.Core.DefaultSnippets {
			resolved, err := resolveText(fmt.Sprintf("core.defaultSnippets[%d].snippet", index), snippet.Snippet, now)
			if err != nil {
				return Content{}, err
			}
			snippet.Snippet = resolved
			snippets = append(snippets, snippet)
		}
	}

	return Content{DefaultQuery: query, Snippets: snippets}, nil
}

// resolveText executes one configured source template with helpers bound to now.
func resolveText(source, text string, now time.Time) (string, error) {
	tmpl, err := template.New(source).Funcs(template.FuncMap{
		"date":      func(args ...any) (string, error) { return formatDate(now, args...) },
		"timestamp": func(args ...any) (string, error) { return formatTimestamp(now, args...) },
	}).Option("missingkey=error").Parse(text)
	if err != nil {
		return "", fmt.Errorf("%s: parse template: %w", source, err)
	}

	var out bytes.Buffer
	if err := tmpl.Execute(&out, nil); err != nil {
		return "", fmt.Errorf("%s: execute template: %w", source, err)
	}
	return out.String(), nil
}

// formatDate applies local calendar-day arithmetic to now before formatting.
func formatDate(now time.Time, args ...any) (string, error) {
	days, err := signedDayOffset(args)
	if err != nil {
		return "", err
	}
	return now.AddDate(0, 0, days).Format(time.DateOnly), nil
}

func signedDayOffset(args []any) (int, error) {
	switch len(args) {
	case 0:
		return 0, nil
	case 1:
		days, ok := args[0].(int)
		if !ok {
			return 0, errors.New("date offset must be a signed integer")
		}
		return days, nil
	default:
		return 0, errors.New("date accepts zero or one signed integer day offset")
	}
}

// formatTimestamp applies elapsed-duration arithmetic to now before formatting.
func formatTimestamp(now time.Time, args ...any) (string, error) {
	switch len(args) {
	case 0:
		return now.Format(time.RFC3339), nil
	case 1:
		duration, ok := args[0].(string)
		if !ok {
			return "", errors.New("timestamp duration must be a string")
		}
		delta, err := time.ParseDuration(duration)
		if err != nil {
			return "", fmt.Errorf("parse timestamp duration %q: %w", duration, err)
		}
		return now.Add(delta).Format(time.RFC3339), nil
	default:
		return "", errors.New("timestamp accepts zero or one Go duration string")
	}
}
