package httpapi

import (
	"embed"
	"fmt"
	"html/template"
	"math"
	"path/filepath"
)

//go:embed templates/*.tmpl
var templatesFS embed.FS

func formatTokens(n int) string {
	if n < 10000 {
		return fmt.Sprintf("%d", n)
	}
	if n < 1000000 {
		v := float64(n) / 1000
		return fmt.Sprintf("%.1fK", math.Round(v*10)/10)
	}
	v := float64(n) / 1000000
	return fmt.Sprintf("%.2fM", math.Round(v*100)/100)
}

func mustLoadTemplate(path string) *template.Template {
	funcMap := template.FuncMap{
		"formatTokens": formatTokens,
	}
	t := template.Must(template.New(filepath.Base(path)).Funcs(funcMap).ParseFS(templatesFS, "templates/_partials.tmpl", path))
	page := t.Lookup(filepath.Base(path))
	if page == nil {
		panic("template not found: " + path)
	}
	return page
}

var adminLoginTemplate = mustLoadTemplate("templates/admin_login.tmpl")
var adminProvidersTemplate = mustLoadTemplate("templates/admin_providers.tmpl")
var adminModelsTemplate = mustLoadTemplate("templates/admin_models.tmpl")
var adminKeysTemplate = mustLoadTemplate("templates/admin_keys.tmpl")
var adminUsageTemplate = mustLoadTemplate("templates/admin_usage.tmpl")
var adminPlaygroundTemplate = mustLoadTemplate("templates/admin_playground.tmpl")
