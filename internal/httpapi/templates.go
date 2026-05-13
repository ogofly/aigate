package httpapi

import (
	"bytes"
	"embed"
	"encoding/json"
	"html/template"
	"path/filepath"
	"strings"
)

//go:embed templates/*.tmpl
var templatesFS embed.FS

//go:embed assets/logo.svg
var adminLogoSVG []byte

//go:embed assets/favicon.svg
var adminFaviconSVG []byte

//go:embed assets/avatar.svg
var adminAvatarSVG []byte

func mustLoadTemplate(path string) *template.Template {
	t := template.Must(template.New("").Funcs(template.FuncMap{
		"maskID": maskIdentifier,
		"toJSON": toJSON,
	}).ParseFS(templatesFS, "templates/_partials.tmpl", path))
	page := t.Lookup(filepath.Base(path))
	if page == nil {
		panic("template not found: " + path)
	}
	return page
}

func maskIdentifier(value string) string {
	if value == "" {
		return "-"
	}
	if len(value) <= 10 {
		return strings.Repeat("*", len(value))
	}
	return value[:4] + "****" + value[len(value)-4:]
}

func toJSON(value any) template.JS {
	data, err := json.Marshal(value)
	if err != nil {
		return template.JS("null")
	}
	// Prevent embedded JSON from closing its surrounding script tag.
	data = bytes.ReplaceAll(data, []byte("</"), []byte("<\\/"))
	return template.JS(data)
}

var adminLoginTemplate = mustLoadTemplate("templates/admin_login.tmpl")
var adminProvidersTemplate = mustLoadTemplate("templates/admin_providers.tmpl")
var adminModelsTemplate = mustLoadTemplate("templates/admin_models.tmpl")
var adminKeysTemplate = mustLoadTemplate("templates/admin_keys.tmpl")
var adminUsageTemplate = mustLoadTemplate("templates/admin_usage.tmpl")
var adminPlaygroundTemplate = mustLoadTemplate("templates/admin_playground.tmpl")
