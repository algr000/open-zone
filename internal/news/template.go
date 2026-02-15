package news

import (
	"embed"
	"fmt"
	"text/template"
)

//go:embed templates/news.tmpl
var newsTemplatesFS embed.FS

func loadTemplate() (*template.Template, error) {
	b, err := newsTemplatesFS.ReadFile("templates/news.tmpl")
	if err != nil {
		return nil, fmt.Errorf("read embedded news template: %w", err)
	}
	t, err := template.New("news.tmpl").Option("missingkey=zero").Parse(string(b))
	if err != nil {
		return nil, fmt.Errorf("parse embedded news template: %w", err)
	}
	return t, nil
}
