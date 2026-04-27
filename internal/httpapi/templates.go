package httpapi

import (
	"embed"
	"html/template"
	"path/filepath"
)

//go:embed templates/*.tmpl
var templatesFS embed.FS

func mustLoadTemplate(path string) *template.Template {
	t := template.Must(template.ParseFS(templatesFS, "templates/_partials.tmpl", path))
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
